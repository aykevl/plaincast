package youtube

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aykevl/plaincast/apps/youtube/mp"
	"github.com/aykevl/plaincast/config"
	"github.com/aykevl/plaincast/log"
	"github.com/nu7hatch/gouuid"
)

var logger = log.New("youtube", "Log YouTube app")

// How often a new connection attempt should be done.
// With a starting delay of 500ms that exponentially increases, this is about 5
// minutes.
const RETRIES = 25

// Initial retry timeout in milliseconds. This timeout increases exponentially.
const RETRY_TIMEOUT = 500

// # Preventing race conditions & leaks
//
// There were a *lot* race conditions, but most have been fixed by now, using a
// new design. Many race conditions were triggered when Stop() was called.
// Stop() will hold a mutex for the `running` variable. As will the part
// receiving messages. As soon as Stop() is called, no more new messages will be
// received and run() will be stopped using a separate channel (`runQuit`).
// The overall exiting order looks like this:
//     Stop() + bind() -> run() -> backend (via player) -> player -> playerEvents -> outgoingMessages
//
// These are also all goroutines that will exist (a few possible exceptions
// aside that will manage their lifetime themselves).
// The player will ensure proper synchronisation so it won't call more than one
// method on the backend at any time, thus backend.Quit() will also be
// synchronous. Then it will close the main playerEvents channel to signal to
// playerEvents no more signals will be sent.

// The YouTube app can play the audio track of YouTube videos, and is designed
// to be very lightweight (not running Chrome).
type YouTube struct {
	systemName   string
	running      bool
	runningMutex sync.Mutex
	// TODO split everything under here into a separate struct, so re-running
	// the app won't clash with the previous run.
	rid              *RandomID // generates random numbers for outgoing messages
	runQuit          chan struct{}
	uuid             string
	loungeToken      string
	sendMutex        sync.Mutex
	sid              string
	gsessionid       string
	aid              int32 // int32 is thread-safe on ARM and Intel processors
	mp               *mp.MediaPlayer
	mpMutex          sync.Mutex // to quit the player safely
	incomingMessages chan incomingMessage
	outgoingMessages chan outgoingMessage
	pairingCodes     chan string
}

// JSON data structures for get_lounge_token_batch.
type loungeTokenBatchJson struct {
	Screens []screenTokenJson "screens"
}
type screenTokenJson struct {
	ScreenId    string "screenId"
	Expiration  int64  "expiration"
	LoungeToken string "loungeToken"
}

// JSON data structure for messages received over the message channel.
type incomingMessagesJson []incomingMessageJson
type incomingMessageJson []interface{}
type incomingMessage struct {
	index   int
	command string
	args    map[string]string
}

// A single outgoing message, to be fed to the outgoingMessages channel.
type outgoingMessage struct {
	command string
	args    map[string]string
}

// New returns a new YouTube object (app).
func New(systemName string) *YouTube {
	yt := YouTube{}
	yt.systemName = systemName
	yt.runQuit = make(chan struct{})
	return &yt
}

func (yt *YouTube) FriendlyName() string {
	return "YouTube"
}

func (yt *YouTube) Data(requestData string) string {
        if requestData == "screenid" {
		return yt.getScreenId()
	}

	return ""
}

// Start starts the YouTube app asynchronously.
// Attaches a new device if the app has already started.
func (yt *YouTube) Start(postData string) {
	yt.runningMutex.Lock()
	running := yt.running
	yt.runningMutex.Unlock()

	arguments, err := url.ParseQuery(postData)
	// TODO proper error handling
	if err != nil {
		panic(err)
	}

	if running {
		// Only use `pairingCode`, ignore `v` and `t` arguments.
		yt.pairingCodes <- arguments["pairingCode"][0]

	} else {
		yt.start(arguments)
	}
}

// Quit stops this app if it is running.
func (yt *YouTube) Quit() {
	// shut down everything about this app
	yt.runningMutex.Lock()
	defer yt.runningMutex.Unlock()

	if !yt.running {
		return
	}
	yt.running = false

	yt.runQuit <- struct{}{}
}

func (yt *YouTube) init(arguments url.Values, stateChange chan mp.StateChange) {
	var err error

	yt.rid = NewRandomID()

	yt.uuid, err = config.Get().GetString("apps.youtube.uuid", func() (string, error) {
		uuid, err := uuid.NewV4()
		if err != nil {
			return "", err
		}
		return uuid.String(), nil
	})
	if err != nil {
		panic(err)
	}
	yt.incomingMessages = make(chan incomingMessage, 5)
	yt.outgoingMessages = make(chan outgoingMessage, 5)

	// This is a goroutine that receives messages from YouTube and starts a
	// goroutine to send messages to YouTube.
	go yt.connect()

	if pairingCodes, ok := arguments["pairingCode"]; ok {
		go func() {
			yt.pairingCodes <- pairingCodes[0]
		}()
	}

	yt.mp = mp.New(stateChange)

	video, ok := arguments["v"]
	if ok && len(video[0]) > 0 {
		videoId := video[0]

		position, err := time.ParseDuration(arguments["t"][0] + "s")
		if err != nil {
			panic(err)
		}

		yt.mp.SetPlaystate([]string{videoId}, 0, position, "")
	}
}

func (yt *YouTube) start(arguments url.Values) {
	yt.runningMutex.Lock()
	defer yt.runningMutex.Unlock()
	yt.running = true

	// Of all values, this one should not be initialized inside a goroutine
	// because that's a race condition.
	yt.pairingCodes = make(chan string)

	go yt.run(arguments)
}

func (yt *YouTube) run(arguments url.Values) {
	stateChange := make(chan mp.StateChange)
	volumeChan := make(chan int, 1)
	playlistChan := make(chan mp.PlaylistState)
	nowPlayingChan := make(chan mp.PlaylistState, 1)
	// nowPlayingChan will ask for a signal inside playerEvents.

	// This goroutine handles all signals coming from the media player.
	go yt.playerEvents(stateChange, volumeChan, playlistChan, nowPlayingChan)

	yt.init(arguments, stateChange)

	for {
		select {
		case message := <-yt.incomingMessages:

			// Only print a message for less-verbose output.
			switch message.command {
			//case "remoteConnected", "remoteDisconnected":
			default:
				logger.Printf("command: %d %s %#v\n", message.index, message.command, message.args)
			}

			switch message.command {
			case "remoteConnected":
				logger.Printf("Remote connected: %s (%s)\n", message.args["name"], message.args["user"])
			case "remoteDisconnected":
				logger.Printf("Remote disconnected: %s (%s)\n", message.args["name"], message.args["user"])
			case "loungeStatus":
			case "getVolume":
				yt.mp.RequestVolume(volumeChan)
			case "setVolume":
				delta, ok := message.args["delta"]
				if ok {
					delta, err := strconv.Atoi(delta)
					if err != nil {
						logger.Warnln("volume delta could not be parsed:", err)
						break
					}
					yt.mp.ChangeVolume(delta, volumeChan)
				} else {
					volume, err := strconv.Atoi(message.args["volume"])
					if err != nil {
						logger.Warnln("volume could not be parsed:", err)
						break
					}
					yt.mp.SetVolume(volume, volumeChan)
				}
			case "getPlaylist":
				yt.mp.RequestPlaylist(playlistChan)
			case "setPlaylist":
				playlist := strings.Split(message.args["videoIds"], ",")

				index, err := strconv.Atoi(message.args["currentIndex"])
				if err != nil {
					logger.Warnln("currentIndex could not be parsed:", err)
					break
				}

				position, err := time.ParseDuration(message.args["currentTime"] + "s")
				if err != nil {
					if message.args["currentTime"] == "" {
						logger.Warnln("currentTime was empty")
					} else {
						logger.Warnln("currentTime could not be parsed:", err)
					}
					// position is 0s, nothing too problematic
				}

				if index < 0 || len(playlist) == 0 || index >= len(playlist) {
					logger.Warnln("setPlaylist got invalid parameters")
					break
				}

				yt.mp.SetPlaystate(playlist, index, position, message.args["listId"])
			case "updatePlaylist":
				playlist := strings.Split(message.args["videoIds"], ",")
				yt.mp.UpdatePlaylist(playlist, message.args["listId"])
				yt.outgoingMessages <- outgoingMessage{"confirmPlaylistUpdate", map[string]string{"updated": "true"}}
			case "setVideo":
				videoId := message.args["videoId"]
				position, err := time.ParseDuration(message.args["currentTime"] + "s")
				if err != nil {
					logger.Warnln("could not parse currentTime:", err)
					break
				}

				yt.mp.SetVideo(videoId, position)
			case "getNowPlaying":
				yt.mp.RequestPlaylist(nowPlayingChan)
			case "getSubtitlesTrack":
				// Just send out an empty message. It looks like the Android
				// YouTube client doesn't care too much about this message
				// anyway. Usually `getSubtitlesTrack` is only sent on
				// connection, and not asked (or sent) when switching videos,
				// which is kinda odd to me. When a video is playing while this
				// message is sent, the videoId is sent with it, and some other
				// stuff like `languageCode` to indicate the currently playing
				// subtitles track. Again, this is not updated when the video
				// changes.
				// No subtitles are visible anyway on a headless Chromecast
				// installation, and the Android client doesn't seem to change
				// it's behavior much when leaving out this message.
				yt.outgoingMessages <- outgoingMessage{"onSubtitlesTrackChanged", map[string]string{"videoId": ""}}
			case "pause":
				yt.mp.Pause()
			case "play":
				yt.mp.Play()
			case "seekTo":
				position, err := time.ParseDuration(message.args["newTime"] + "s")
				if err != nil {
					logger.Warnln("could not parse newTime for seekTo:", err)
					break
				}
				yt.mp.Seek(position)
			case "stopVideo":
				yt.mp.Stop()
			}

		case <-yt.runQuit:
			// The YouTube app has been stopped.

			yt.mpMutex.Lock()
			yt.mp.Quit()
			yt.mp = nil
			yt.mpMutex.Unlock()

			return
		}
	}
}

func (yt *YouTube) playerEvents(stateChange chan mp.StateChange, volumeChan chan int, playlistChan, nowPlayingChan chan mp.PlaylistState) {
	for {
		select {
		case change, ok := <-stateChange:
			if !ok {
				// player has quit
				close(yt.outgoingMessages)
				return
			}

			if change.State == mp.STATE_BUFFERING || change.State == mp.STATE_STOPPED {
				// Only access yt.mp when it is certain it isn't being quit.
				// yt.mp is nil when it is being stopped.
				yt.mpMutex.Lock()
				if yt.mp != nil {
					yt.mp.RequestPlaylist(nowPlayingChan)
				}
				yt.mpMutex.Unlock()
			}

			if change.State == mp.STATE_SEEKING {
				// YouTube only knows buffering, not seeking
				change.State = mp.STATE_BUFFERING
			}

			yt.outgoingMessages <- outgoingMessage{"onStateChange", map[string]string{
				"currentTime":       strconv.FormatFloat(change.Position.Seconds(), 'f', 3, 64),
				"duration":          strconv.FormatFloat(change.Duration.Seconds(), 'f', 3, 64),
				"seekableStartTime": "0",
				"seekableEndTime":   strconv.FormatFloat(change.Duration.Seconds(), 'f', 3, 64),
				"state":             strconv.Itoa(int(change.State)),
			}}

		case volume := <-volumeChan:
			yt.outgoingMessages <- outgoingMessage{"onVolumeChanged", map[string]string{
				"volume": strconv.Itoa(volume),
				"muted":  "false",
			}}

		case ps := <-playlistChan:
			message := outgoingMessage{"nowPlayingPlaylist", map[string]string{}}
			if len(ps.Playlist) > 0 {
				message.args["videoIds"] = strings.Join(ps.Playlist, ",")
				message.args["videoId"] = ps.Playlist[ps.Index]
				message.args["currentTime"] = strconv.FormatFloat(ps.Position.Seconds(), 'f', 3, 64)
				message.args["duration"] = strconv.FormatFloat(ps.Duration.Seconds(), 'f', 3, 64)
				message.args["state"] = strconv.Itoa(int(ps.State))
				message.args["currentIndex"] = strconv.Itoa(ps.Index)
				//message.args["listId"] = ""
			}
			yt.outgoingMessages <- message
		case ps := <-nowPlayingChan:
			message := outgoingMessage{"nowPlaying", map[string]string{}}
			if len(ps.Playlist) > 0 {
				message.args = map[string]string{
					"videoId":           ps.Playlist[ps.Index],
					"currentTime":       strconv.FormatFloat(ps.Position.Seconds(), 'f', 3, 64),
					"duration":          strconv.FormatFloat(ps.Duration.Seconds(), 'f', 3, 64),
					"seekableStartTime": "0",
					"seekableEndTime":   strconv.FormatFloat(ps.Duration.Seconds(), 'f', 3, 64),
					"state":             strconv.Itoa(int(ps.State)),
					"currentIndex":      strconv.Itoa(ps.Index),
					"listId":            ps.ListId,
				}
			}
			yt.outgoingMessages <- message
		}
	}
}

func (yt *YouTube) Running() bool {
	yt.runningMutex.Lock()
	defer yt.runningMutex.Unlock()
	return yt.running
}

func (yt *YouTube) connect() {
	yt.loadLoungeToken()

	// Start sending/receiving channel.
	// There should now be enough information.
	yt.bind()
}

func (yt *YouTube) loadLoungeToken() {
	params := url.Values{
		"screen_ids": []string{yt.getScreenId()},
	}
	logger.Println("Getting lounge token batch...")
	response, err := httpPostFormBody("https://www.youtube.com/api/lounge/pairing/get_lounge_token_batch", params)
	if err != nil {
		// TODO exit the app or something when this happens, don't panic
		logger.Panic(err)
	}
	loungeTokenBatch := loungeTokenBatchJson{}
	json.Unmarshal(response, &loungeTokenBatch)
	yt.loungeToken = loungeTokenBatch.Screens[0].LoungeToken
}

func (yt *YouTube) getScreenId() string {
	screenId, err := config.Get().GetString("apps.youtube.screenId", func() (string, error) {
		logger.Println("Getting screen_id...")
		response, err := httpGetBody("https://www.youtube.com/api/lounge/pairing/generate_screen_id")
		return string(response), err
	})
	if err != nil {
		// TODO use proper error handling
		logger.Panic(err)
	}

	return screenId
}

func (yt *YouTube) openChannel(initial bool) *http.Response {
	if initial {
		yt.rid.Restart()

		logger.Println("Getting first batch of messages")
	}

	doInitial := initial

	// How often the connection was retried. Zero on normal operation.
	// The retry timeout increases exponentially with the number of failures.
	retries := 0

	for {
		yt.sendMutex.Lock()
		aid := yt.aid
		yt.sendMutex.Unlock()

		var bindUrl string
		// TODO more fields should be query-escaped
		if !doInitial {
			// normal reconnect
			bindUrl = fmt.Sprintf("https://www.youtube.com/api/lounge/bc/bind?device=LOUNGE_SCREEN&id=%s&name=%s&loungeIdToken=%s&VER=8&RID=rpc&SID=%s&CI=0&AID=%d&gsessionid=%s&TYPE=xmlhttp&zx=%s",
				yt.uuid, url.QueryEscape(yt.systemName), yt.loungeToken, yt.sid, aid, yt.gsessionid, zx())
		} else if yt.sid == "" {
			// first connection
			bindUrl = fmt.Sprintf("https://www.youtube.com/api/lounge/bc/bind?device=LOUNGE_SCREEN&id=%s&name=%s&loungeIdToken=%s&VER=8&RID=%d&zx=%s",
				yt.uuid, url.QueryEscape(yt.systemName), yt.loungeToken, yt.rid.Next(), zx())
		} else {
			// connection after a 400 Unknown SID error
			bindUrl = fmt.Sprintf("https://www.youtube.com/api/lounge/bc/bind?device=LOUNGE_SCREEN&id=%s&name=%s&loungeIdToken=%s&OSID=%s&OAID=%d&VER=8&RID=%d&zx=%s",
				yt.uuid, url.QueryEscape(yt.systemName), yt.loungeToken, yt.sid, aid, yt.rid.Next(), zx())
		}

		timeBeforeGet := time.Now()

		var resp *http.Response
		var err error
		if doInitial {
			params := url.Values{
				"count": []string{"0"},
			}
			resp, err = http.PostForm(bindUrl, params)
		} else {
			resp, err = http.Get(bindUrl)
		}

		if err != nil {
			if err == io.EOF {
				if !yt.errorRetryTimeout(&retries, "EOF on bind", err) {
					yt.Quit()
					break
				}
				// reconnect
				continue
			} else if _, ok := err.(net.Error); ok && err.(net.Error).Timeout() {
				logger.Warnln("timeout while connecting to message channel, retrying in 30s...")
				time.Sleep(30 * time.Second)
				continue
			}
			logger.Errln("Unknown error:", err)
			yt.Quit()
			break
		}

		if resp.Status == "400 Unknown SID" {
			logger.Println("error:", resp.Status, ". Reconnecting the message channel...")
			// Restart the Channel API connection
			doInitial = true
			continue

		} else if resp.Status == "400 Bad Request" {
			// Most likely this is also an "Unknown SID" error.
			buf, err := ioutil.ReadAll(resp.Body)
			if err != nil {
				logger.Errln("could not read error message after 400 Bad Request:", err)
				yt.Quit()
				break
			}

			if strings.Index(string(buf), "<TITLE>Unknown SID</TITLE>") < 0 {
				// Some other error
				logger.Errln("response body:")
				os.Stdout.Write(buf)
				os.Stdout.Sync()
				yt.Quit()
				break
			}

			// Let's try again, similar to "400 Unknown SID"
			logger.Println("error: 400 Bad Request (Unknown SID). Reconnecting the message channel...")
			doInitial = true
			continue

		} else if resp.Status == "410 Gone" {
			if !yt.errorRetryTimeout(&retries, "got 410 Gone on reconnect", nil) {
				yt.Quit()
				break
			}

			// Restart Channel API connection from the beginning
			yt.sendMutex.Lock()
			yt.sid = ""
			yt.loadLoungeToken()
			yt.sendMutex.Unlock()
			doInitial = true
			continue

		} else if resp.StatusCode == 502 {
			if !yt.errorRetryTimeout(&retries, "got HTTP error 502 on reconnect", nil) {
				yt.Quit()
				break
			}
			continue

		} else if resp.StatusCode != 200 {
			logger.Errln("HTTP error while connecting to message channel:", resp.Status)

			// most likely the YouTube server gives back an error in HTML form
			printHTTPError(resp)

			yt.Quit()
			break
		}

		if !initial {
			latency := time.Now().Sub(timeBeforeGet) / time.Millisecond * time.Millisecond
			logger.Println("Connected to message channel in", latency)
		}

		if doInitial {
			yt.sendMutex.Lock()
			yt.aid = -1
			yt.sendMutex.Unlock()
		}

		return resp
	}

	return nil
}

func (yt *YouTube) bind() {

	resp := yt.openChannel(true)
	if resp == nil || yt.handleMessageStream(resp, true) {
		return
	}

	// now yt.sid and yt.gsessionid should be defined, so sendMessages has
	// enough information to start

	go yt.sendMessages()

	// Loop to keep the connection open
	for {
		resp := yt.openChannel(false)
		if resp == nil {
			break
		}
		if yt.handleMessageStream(resp, false) {
			break
		}
	}
}

// errorRetryTimeout waits an exponential amount of time to retry something, or
// gives up after a number of retries. It returns true when it should retry, or
// false otherwise.
func (yt *YouTube) errorRetryTimeout(retries *int, message string, err error) bool {
	*retries++
	retryTimeout := time.Duration((*retries)*(*retries)) * RETRY_TIMEOUT * time.Millisecond
	ending := ""
	if err != nil {
		ending = ": " + err.Error()
	}
	if *retries > RETRIES {
		logger.Errf("%s, giving up%s\n", message, ending)
		return false
	}
	logger.Warnf("%s, retrying in %s%s\n", message, retryTimeout, ending)
	time.Sleep(retryTimeout)
	return true
}

func (yt *YouTube) handleMessageStream(resp *http.Response, singleBatch bool) bool {
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if line == "" && err == io.EOF {
				// The stream has terminated.
				return false // try again
			}

			logger.Printf("error: %s (line: %#v)\n", err, line)

			// try again
			logger.Println("Trying to reconnect to message channel...")
			return false
		}

		length, err := strconv.Atoi(line[:len(line)-1])
		if err != nil {
			panic(err)
		}

		data := make([]byte, length)
		_, err = io.ReadFull(reader, data)
		if err != nil {
			panic(err)
		}

		messages := incomingMessagesJson{}
		json.Unmarshal(data, &messages)
		for _, message := range messages {
			if yt.handleRawReceivedMessage(message) {
				return true
			}
		}

		if singleBatch {
			break
		}
	}

	return false
}

func (yt *YouTube) handleRawReceivedMessage(rawMessage incomingMessageJson) bool {
	message := incomingMessage{}
	message.index = int(rawMessage[0].(float64))

	if message.index != int(yt.aid+1) {
		if message.index <= int(yt.aid) {
			logger.Warnln("old command:", message.index, message.command, message.args)
			return false
		} else {
			logger.Errf("missing some messages, message number=%d, expected number=%d\n", message.index, yt.aid)
		}
	}
	yt.aid = int32(message.index)

	message.command = rawMessage[1].([]interface{})[0].(string)

	args := make([]interface{}, len(rawMessage[1].([]interface{}))-1)

	for i := 0; i < len(args); i++ {
		args[i] = rawMessage[1].([]interface{})[i+1]
	}

	yt.runningMutex.Lock()
	running := yt.running
	yt.runningMutex.Unlock()
	if !running {
		return true
	}

	switch message.command {
	case "noop":
		// no-op, ignore
	case "c":
		if len(args) == 0 {
			logger.Warnln("no argument to 'c' command")
			break
		}
		sid, ok := args[0].(string)
		if !ok {
			logger.Warnln("SID is not a string")
		} else {
			yt.sid = sid
		}
	case "S":
		if len(args) == 0 {
			logger.Warnln("no argument to 'S' command")
			break
		}
		gsessionid, ok := args[0].(string)
		if !ok {
			logger.Warnln("gsessionid is not a string")
		} else {
			yt.gsessionid = gsessionid
		}
	default:
		if len(args) > 0 {
			// convert map[string]interface{} into map[string]string
			argsMap, ok := args[0].(map[string]interface{})
			if !ok {
				logger.Warnln("message values are not a map", message.command)
			}
			message.args = make(map[string]string, len(argsMap))
			for k, v := range argsMap {
				if s, ok := v.(string); !ok {
					logger.Warnln("message", message.command, "does not have string value for key", k)
				} else {
					message.args[k] = s
				}
			}
		}
		yt.incomingMessages <- message
	}

	return false
}

func (yt *YouTube) sendMessages() {
	queuedMessages := make([]outgoingMessage, 0, 3)
	count := 0

	var deadline time.Time
	deadlineStart := make(chan struct{})
	deadlineEnd := make(chan struct{})
	go func() {
		for _ = range deadlineStart {
			// It looks like 10ms is a good default. HTTP latency appears to be
			// relatively independent of the machine performance, so I guess it is bound
			// by the speed of light...
			time.Sleep(1 * time.Millisecond)
			deadlineEnd <- struct{}{}
		}
	}()
	defer close(deadlineStart)

	for {
		select {
		case message, ok := <-yt.outgoingMessages:
			if !ok {
				// This is the sign the sendMessages goroutine should quit.
				return
			}

			queuedMessages = append(queuedMessages, message)

			if deadline.IsZero() {
				deadline = time.Now()
				deadlineStart <- struct{}{}
				continue
			}

		case <-deadlineEnd:
			values := url.Values{
				"count": []string{strconv.Itoa(len(queuedMessages))}, // the amount of messages in this POST
				"ofs":   []string{strconv.Itoa(count)},               // which index the first message has
			}
			for i, message := range queuedMessages {
				req := "req" + strconv.Itoa(i) + "_"
				values.Set(req+"_sc", message.command)
				for k, v := range message.args {
					values.Set(req+k, v)
				}
				logger.Println("send msg:", message.command, message.args)
			}

			timeBeforeSend := time.Now()

			retries := 0
			for {
				yt.sendMutex.Lock()
				_, err := httpPostFormBody(fmt.Sprintf("https://www.youtube.com/api/lounge/bc/bind?device=LOUNGE_SCREEN&id=%s&name=%s&loungeIdToken=%s&VER=8&SID=%s&RID=%d&AID=%d&gsessionid=%s&zx=%s",
					yt.uuid, url.QueryEscape(yt.systemName), yt.loungeToken, yt.sid, yt.rid.Next(), yt.aid, yt.gsessionid, zx()), values)
				yt.sendMutex.Unlock()

				if err != nil {
					if !yt.errorRetryTimeout(&retries, "could not send message", err) {
						yt.Quit()
						return
					}
					continue
				}

				retries = 0
				break
			}

			prepareLatency := timeBeforeSend.Sub(deadline) / time.Millisecond * time.Millisecond
			httpLatency := time.Now().Sub(timeBeforeSend) / time.Millisecond * time.Millisecond
			logger.Printf("messages sent: %d (prepare %s, http latency %s)\n", len(queuedMessages), prepareLatency, httpLatency)

			count += len(queuedMessages)
			queuedMessages = queuedMessages[:0]

			deadline = time.Time{}

		case pairingCode := <-yt.pairingCodes:
			// Register the pairing code: that can be done after sending and
			// receiving message channels have been set up.
			logger.Println("Registering pairing code...")
			params := url.Values{
				"access_type":  []string{"permanent"},
				"pairing_code": []string{pairingCode},
				"screen_id":    []string{yt.getScreenId()},
			}
			_, err := httpPostFormBody("https://www.youtube.com/api/lounge/pairing/register_pairing_code", params)
			if err != nil {
				logger.Warnln("could not register pairing code:", err)
			}
		}
	}
}
