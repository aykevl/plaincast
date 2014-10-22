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

// The YouTube app can play the audio track of YouTube videos, and is designed
// to be very lightweight (not running Chrome).
type YouTube struct {
	friendlyName     string
	running          bool
	runningMutex     sync.Mutex
	rid              chan int // sends random numbers for outgoing messages
	ridQuit          chan struct{}
	uuid             string
	loungeToken      string
	sid              string
	gsessionid       string
	aid              int32 // int32 is thread-safe on ARM and Intel processors
	mp               *mp.MediaPlayer
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

// Stop stops this app if it is running.
func (yt *YouTube) Stop() {
	// shut down everything about this app
	yt.runningMutex.Lock()
	defer yt.runningMutex.Unlock()

	if !yt.running {
		return
	}
	yt.running = false

	// WARNING: not thread-safe (some goroutines may still be busy with the
	// media player, or the media player may not have fully started).
	yt.mp.Quit()
	yt.mp = nil

	// TODO this may panic if there are still goroutines sending on this channel
	close(yt.outgoingMessages)

	yt.ridQuit <- struct{}{}
}

func (yt *YouTube) init(postData string, stateChange chan mp.StateChange, volumeChange chan int) {
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

	yt.mp = mp.New(stateChange, volumeChange)

	values, err := url.ParseQuery(postData)
	if err != nil {
		panic(err)
	}

	video, ok := values["v"]
	if ok && len(video[0]) > 0 {
		videoId := video[0]

		position, err := time.ParseDuration(values["t"][0] + "s")
		if err != nil {
			panic(err)
		}

		yt.mp.SetPlaystate([]string{videoId}, 0, position)
	}

	go yt.connect(values["pairingCode"][0])
}

func (yt *YouTube) run(postData string) {
	log.Println("running YouTube:", postData)

	stateChange := make(chan mp.StateChange)
	volumeChange := make(chan int)

	yt.init(postData, stateChange, volumeChange)

	for {
	selectStmt:
		select {
		case message := <-yt.incomingMessages:
			switch message.command {
			case "remoteConnected":
				log.Printf("Remote connected: %s (%s)\n", message.args["name"], message.args["user"])
			case "remoteDisconnected":
				log.Printf("Remote disconnected: %s (%s)\n", message.args["name"], message.args["user"])
			case "getVolume":
				go func() {
					yt.sendVolume(yt.mp.GetPlaystate().Volume)
				}()
			case "setVolume":
				delta, ok := message.args["delta"]
				if ok {
					delta, err := strconv.Atoi(delta)
					if err != nil {
						log.Println("WARNING: volume delta could not be parsed:", err)
						break
					}
					go func() {
						volumeChan := yt.mp.ChangeVolume(delta)
						yt.sendVolume(<-volumeChan)
					}()
				} else {
					volume, err := strconv.Atoi(message.args["volume"])
					if err != nil {
						log.Println("WARNING: volume could not be parsed:", err)
						break
					}
					yt.mp.SetVolume(volume)
					yt.sendVolume(volume)
				}
			case "getPlaylist":
				yt.sendPlaylist()
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

				go yt.mp.SetPlaystate(playlist, index, position)
			case "updatePlaylist":
				playlist := strings.Split(message.args["videoIds"], ",")
				go yt.mp.UpdatePlaylist(playlist)
			case "setVideo":
				videoId := message.args["videoId"]
				position, err := time.ParseDuration(message.args["currentTime"] + "s")
				if err != nil {
					log.Println("WARNING: could not parse currentTime:", err)
					break
				}

				yt.mp.SetVideo(videoId, position)
			case "getNowPlaying":
				yt.sendNowPlaying()
			case "getSubtitlesTrack":
				go func() {
					ps := yt.mp.GetPlaystate()
					videoId := ""
					if len(ps.Playlist) > 0 {
						videoId = ps.Playlist[ps.Index]
					}
					// this appears to be the right way, but I'm not sure it should send this message as there
					// are obviously no subtitles when there is no screen
					yt.outgoingMessages <- outgoingMessage{"onSubtitlesTrackChanged", map[string]string{"videoId": videoId}}
				}()
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
			default:
				log.Println("unknown command:", message.index, message.command, message.args)
				break selectStmt
			}

			log.Println("command:", message.index, message.command, message.args)

		case change := <-stateChange:
			if change.State == mp.STATE_BUFFERING || change.State == mp.STATE_STOPPED {
				yt.sendNowPlaying()
			}
			yt.outgoingMessages <- outgoingMessage{"onStateChange", map[string]string{"currentTime": strconv.FormatFloat(change.Position.Seconds(), 'f', 3, 64), "state": strconv.Itoa(int(change.State))}}

		case volume := <-volumeChange:
			yt.sendVolume(volume)
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

	// there is enough information now to set up the message channel
	go yt.bind()

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
			yt.Stop()
			break
		}

		if resp.StatusCode != 200 {
			log.Println("HTTP error while connecting to message channel:", resp.StatusCode)

			// most likely the YouTube server gives back an error in HTML form
			buf, err := ioutil.ReadAll(resp.Body)
			handle(err, "error while reading error message")
			log.Printf("Response body:\n%s\n\n", string(buf))

			yt.Stop()
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
		sid, ok := args[0].(string)
		if !ok {
			log.Println("WARNING: SID does not have the right type")
		} else {
			yt.sid = sid
		}
	case "S":
		gsessionid, ok := args[0].(string)
		if !ok {
			log.Println("WARNING: gsessionid does not have the right type")
		} else {
			yt.gsessionid = gsessionid
		}
	default:
		if len(args) > 0 {
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

func (yt *YouTube) sendVolume(volume int) {
	yt.outgoingMessages <- outgoingMessage{"onVolumeChanged", map[string]string{"volume": strconv.Itoa(volume), "muted": "false"}}
}

func (yt *YouTube) sendPlaylist() {
	go func() {
		ps := yt.mp.GetPlaystate()
		if len(ps.Playlist) > 0 {
			yt.outgoingMessages <- outgoingMessage{"nowPlayingPlaylist", map[string]string{"video_ids": strings.Join(ps.Playlist, ","), "video_id": ps.Playlist[ps.Index], "current_time": strconv.FormatFloat(yt.mp.GetPosition().Seconds(), 'f', 3, 64), "state": strconv.Itoa(int(ps.State))}}
		} else {
			yt.outgoingMessages <- outgoingMessage{"nowPlayingPlaylist", map[string]string{}}
		}
	}()
}

func (yt *YouTube) sendNowPlaying() {
	go func() {
		ps := yt.mp.GetPlaystate()
		if len(ps.Playlist) > 0 {
			yt.outgoingMessages <- outgoingMessage{"nowPlaying", map[string]string{"video_id": ps.Playlist[ps.Index], "current_time": strconv.FormatFloat(yt.mp.GetPosition().Seconds(), 'f', 3, 64), "state": strconv.Itoa(int(ps.State))}}
		} else {
			yt.outgoingMessages <- outgoingMessage{"nowPlaying", map[string]string{}}
		}
	}()
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
