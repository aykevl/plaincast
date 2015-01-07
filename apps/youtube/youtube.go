package youtube

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aykevl93/plaincast/apps/youtube/mp"
	"github.com/aykevl93/plaincast/config"
	"github.com/nu7hatch/gouuid"
)

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
	friendlyName string
	running      bool
	runningMutex sync.Mutex
	// TODO split everything under here into a separate struct, so re-running
	// the app won't clash with the previous run.
	rid              chan int // sends random numbers for outgoing messages
	ridQuit          chan struct{}
	runQuit          chan struct{}
	uuid             string
	loungeToken      string
	sid              string
	gsessionid       string
	aid              int32 // int32 is thread-safe on ARM and Intel processors
	mp               *mp.MediaPlayer
	mpMutex          sync.Mutex // to quit the player safely
	incomingMessages chan incomingMessage
	outgoingMessages chan outgoingMessage
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
func New(friendlyName string) *YouTube {
	yt := YouTube{}
	yt.friendlyName = friendlyName
	yt.runQuit = make(chan struct{})
	return &yt
}

// Start starts the YouTube app asynchronously.
// Does nothing when the app has already started.
func (yt *YouTube) Start(postData string) {
	yt.runningMutex.Lock()
	defer yt.runningMutex.Unlock()

	if yt.running {
		return
	}
	yt.running = true

	go yt.run(postData)
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
	yt.ridQuit <- struct{}{}
}

func (yt *YouTube) init(postData string, stateChange chan mp.StateChange) {
	var err error

	yt.rid, yt.ridQuit = yt.ridIterator()

	c := config.Get()
	yt.uuid, err = c.GetString("apps.youtube.uuid", func() (string, error) {
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
	yt.aid = -1

	values, err := url.ParseQuery(postData)
	if err != nil {
		panic(err)
	}

	// This is a goroutine that starts two other goroutines: one that receives
	// messages from YouTube and the other that sends all messages to YouTube.
	// It exits after the message channel has been successfully set up.
	go yt.connect(values["pairingCode"][0])

	yt.mp = mp.New(stateChange)

	video, ok := values["v"]
	if ok && len(video[0]) > 0 {
		videoId := video[0]

		position, err := time.ParseDuration(values["t"][0] + "s")
		if err != nil {
			panic(err)
		}

		yt.mp.SetPlaystate([]string{videoId}, 0, position)
	}
}

func (yt *YouTube) run(postData string) {
	log.Println("running YouTube:", postData)

	stateChange := make(chan mp.StateChange)
	volumeChan := make(chan int)
	playlistChan := make(chan mp.PlaylistState)
	nowPlayingChan := make(chan mp.PlaylistState, 1)
	// nowPlayingChan will ask for a signal inside playerEvents.

	// This goroutine handles all signals coming from the media player.
	go yt.playerEvents(stateChange, volumeChan, playlistChan, nowPlayingChan)

	yt.init(postData, stateChange)

	for {
		select {
		case message := <-yt.incomingMessages:
			log.Println("command:", message.index, message.command, message.args)

			switch message.command {
			case "remoteConnected":
				log.Printf("Remote connected: %s (%s)\n", message.args["name"], message.args["user"])
			case "remoteDisconnected":
				log.Printf("Remote disconnected: %s (%s)\n", message.args["name"], message.args["user"])
			case "loungeStatus":
				// pass
				break
			case "getVolume":
				yt.mp.RequestVolume(volumeChan)
			case "setVolume":
				delta, ok := message.args["delta"]
				if ok {
					delta, err := strconv.Atoi(delta)
					if err != nil {
						log.Println("WARNING: volume delta could not be parsed:", err)
						break
					}
					yt.mp.ChangeVolume(delta, volumeChan)
				} else {
					volume, err := strconv.Atoi(message.args["volume"])
					if err != nil {
						log.Println("WARNING: volume could not be parsed:", err)
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
					log.Println("WARNING: currentIndex could not be parsed:", err)
					break
				}

				position, err := time.ParseDuration(message.args["currentTime"] + "s")
				if err != nil {
					log.Println("WARNING: currentTime could not be parsed:", err)
					break
				}

				if index < 0 || len(playlist) == 0 || index >= len(playlist) {
					log.Println("WARNING: setPlaylist got invalid parameters")
					break
				}

				yt.mp.SetPlaystate(playlist, index, position)
			case "updatePlaylist":
				playlist := strings.Split(message.args["videoIds"], ",")
				yt.mp.UpdatePlaylist(playlist)
			case "setVideo":
				videoId := message.args["videoId"]
				position, err := time.ParseDuration(message.args["currentTime"] + "s")
				if err != nil {
					log.Println("WARNING: could not parse currentTime:", err)
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
					log.Println("WARNING: could not parse newTime for seekTo:", err)
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
			yt.outgoingMessages <- outgoingMessage{"onStateChange", map[string]string{"currentTime": strconv.FormatFloat(change.Position.Seconds(), 'f', 3, 64), "state": strconv.Itoa(int(change.State))}}

		case volume := <-volumeChan:
			yt.outgoingMessages <- outgoingMessage{"onVolumeChanged", map[string]string{"volume": strconv.Itoa(volume), "muted": "false"}}

		case ps := <-playlistChan:
			if len(ps.Playlist) > 0 {
				yt.outgoingMessages <- outgoingMessage{"nowPlayingPlaylist", map[string]string{"video_ids": strings.Join(ps.Playlist, ","), "video_id": ps.Playlist[ps.Index], "current_time": strconv.FormatFloat(ps.Position.Seconds(), 'f', 3, 64), "state": strconv.Itoa(int(ps.State))}}
			} else {
				yt.outgoingMessages <- outgoingMessage{"nowPlayingPlaylist", map[string]string{}}
			}
		case ps := <-nowPlayingChan:
			if len(ps.Playlist) > 0 {
				yt.outgoingMessages <- outgoingMessage{"nowPlaying", map[string]string{"video_id": ps.Playlist[ps.Index], "current_time": strconv.FormatFloat(ps.Position.Seconds(), 'f', 3, 64), "state": strconv.Itoa(int(ps.State))}}
			} else {
				yt.outgoingMessages <- outgoingMessage{"nowPlaying", map[string]string{}}
			}
		}
	}
}

func (yt *YouTube) Running() bool {
	yt.runningMutex.Lock()
	defer yt.runningMutex.Unlock()
	return yt.running
}

func (yt *YouTube) connect(pairingCode string) {
	c := config.Get()

	screenId, err := c.GetString("apps.youtube.screenId", func() (string, error) {
		log.Println("Getting screen_id...")
		buf, err := httpGetBody("https://www.youtube.com/api/lounge/pairing/generate_screen_id")
		return string(buf), err
	})
	if err != nil {
		panic(err)
	}

	log.Println("Getting lounge token batch...")
	params := url.Values{
		"screen_ids": []string{screenId},
	}
	data, err := httpPostFormBody("https://www.youtube.com/api/lounge/pairing/get_lounge_token_batch", params)
	if err != nil {
		panic(err)
	}
	loungeTokenBatch := loungeTokenBatchJson{}
	json.Unmarshal(data, &loungeTokenBatch)
	yt.loungeToken = loungeTokenBatch.Screens[0].LoungeToken

	// Start sending/receiving channel.
	// There should now be enough information.
	go yt.bind()

	// Register the pairing code: that can be done after sending and receiving
	// message channels have been set up.
	log.Println("Register pairing code...")
	params = url.Values{
		"access_type":  []string{"permanent"},
		"pairing_code": []string{pairingCode},
		"screen_id":    []string{screenId},
	}
	_, err = httpPostFormBody("https://www.youtube.com/api/lounge/pairing/register_pairing_code", params)
	if err != nil {
		panic(err)
	}
}

func (yt *YouTube) bind() {
	log.Println("Getting first batch of messages")
	params := url.Values{
		"count": []string{"0"},
	}
	// TODO more fields should be query-escaped
	bindUrl := fmt.Sprintf("https://www.youtube.com/api/lounge/bc/bind?device=LOUNGE_SCREEN&id=%s&name=%s&loungeIdToken=%s&VER=8&RID=%d&zx=%s",
		yt.uuid, url.QueryEscape(yt.friendlyName), yt.loungeToken, <-yt.rid, zx())
	resp, err := http.PostForm(bindUrl, params)
	if err != nil {
		panic(err)
	}

	if yt.handleMessageStream(resp, true) {
		// YouTube closed while connecting
		return
	}

	// now yt.sid and yt.gsessionid should be defined, so sendMessages has
	// enough information to start

	go yt.sendMessages()

	for {
		bindUrl = fmt.Sprintf("https://www.youtube.com/api/lounge/bc/bind?device=LOUNGE_SCREEN&id=%s&name=%s&loungeIdToken=%s&VER=8&RID=rpc&SID=%s&CI=0&AID=%d&gsessionid=%s&TYPE=xmlhttp&zx=%s", yt.uuid, url.QueryEscape(yt.friendlyName), yt.loungeToken, yt.sid, yt.aid, yt.gsessionid, zx())

		timeBeforeGet := time.Now()

		resp, err = http.Get(bindUrl)
		if err != nil {
			log.Println("ERROR:", err)
			yt.Quit()
			break
		}

		if resp.StatusCode != 200 {
			log.Println("HTTP error while connecting to message channel:", resp.StatusCode)

			// most likely the YouTube server gives back an error in HTML form
			buf, err := ioutil.ReadAll(resp.Body)
			handle(err, "error while reading error message")
			log.Printf("Response body:\n%s\n\n", string(buf))

			yt.Quit()
			break
		}

		latency := time.Now().Sub(timeBeforeGet) / time.Millisecond * time.Millisecond
		log.Println("Connected to message channel in", latency)

		if yt.handleMessageStream(resp, false) {
			break
		}
	}
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

			log.Printf("error: %s (line: %#v)\n", err, line)

			// try again
			log.Println("Trying to reconnect to message channel...")
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

	if message.index <= int(yt.aid) {
		log.Println("old command:", message.index, message.command, message.args)
		return false
	}
	yt.aid++
	if message.index != int(yt.aid) {
		panic("missing some messages, message number=" + strconv.Itoa(message.index))
	}

	message.command = rawMessage[1].([]interface{})[0].(string)

	args := make([]interface{}, len(rawMessage[1].([]interface{}))-1)

	for i := 0; i < len(args); i++ {
		args[i] = rawMessage[1].([]interface{})[i+1]
	}

	yt.runningMutex.Lock()
	defer yt.runningMutex.Unlock()
	if !yt.running {
		return true
	}

	switch message.command {
	case "noop":
		// no-op, ignore
	case "c":
		if len(args) == 0 {
			log.Println("WARNING: no argument")
			break
		}
		sid, ok := args[0].(string)
		if !ok {
			log.Println("WARNING: SID does not have the right type")
		} else {
			yt.sid = sid
		}
	case "S":
		if len(args) == 0 {
			log.Println("WARNING: no argument")
			break
		}
		gsessionid, ok := args[0].(string)
		if !ok {
			log.Println("WARNING: gsessionid does not have the right type")
		} else {
			yt.gsessionid = gsessionid
		}
	default:
		if len(args) > 0 {
			// convert map[string]interface{} into map[string]string
			argsMap, ok := args[0].(map[string]interface{})
			if !ok {
				log.Println("WARNING: message values are not a map", message.command)
			}
			message.args = make(map[string]string, len(argsMap))
			for k, v := range argsMap {
				message.args[k], ok = v.(string)
				if !ok {
					log.Println("WARNING: message", message.command, "does not have string value for key", k)
				}
			}
		}
		yt.incomingMessages <- message
	}

	return false
}

func (yt *YouTube) sendMessages() {
	count := 0
	for message := range yt.outgoingMessages {
		// TODO collect multiple messages to send them in one batch
		values := url.Values{
			"count":    []string{"1"},
			"ofs":      []string{strconv.Itoa(count)},
			"req0__sc": []string{message.command},
		}
		for k, v := range message.args {
			values.Set("req0_"+k, v)
		}

		timeBeforeSend := time.Now()

		_, err := httpPostFormBody(fmt.Sprintf("https://www.youtube.com/api/lounge/bc/bind?device=LOUNGE_SCREEN&id=%s&name=%s&loungeIdToken=%s&VER=8&SID=%s&RID=%d&AID=%d&gsessionid=%s&zx=%s",
			yt.uuid, url.QueryEscape(yt.friendlyName), yt.loungeToken, yt.sid, <-yt.rid, yt.aid, yt.gsessionid, zx()), values)
		if err != nil {
			panic(err)
		}

		latency := time.Now().Sub(timeBeforeSend) / time.Millisecond * time.Millisecond
		log.Println("send msg:", latency, message.command, message.args)

		count += 1
	}
}

func (yt *YouTube) ridIterator() (chan int, chan struct{}) {
	// this appears to be a random number between 10000-99999
	rid := rand.Intn(80000) + 10000
	c := make(chan int)
	quit := make(chan struct{})

	go func() {
		for {
			select {
			case c <- rid:
				rid++
			case <-quit:
				return
			}
		}
	}()

	return c, quit
}
