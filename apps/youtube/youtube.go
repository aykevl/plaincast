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
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aykevl93/youtube-receiver/apps/youtube/mp"
	"github.com/aykevl93/youtube-receiver/config"
	"github.com/nu7hatch/gouuid"
)

// The YouTube app can play the audio track of YouTube videos, and is designed
// to be very lightweight (not running Chrome).
type YouTube struct {
	friendlyName     string
	running          bool
	rid              int // random number for outgoing messages
	uuid             string
	loungeToken      string
	sid              string
	gsessionid       string
	aid              int32 // int32 is thread-safe on ARM and Intel processors
	mp               *mp.MediaPlayer
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
	args    []interface{}
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
func (yt *YouTube) Start(postData string) {
	yt.running = true
	go yt.run(postData)
}

func (yt *YouTube) Stop() {
	// shut down everything about this app
	// WARNING: not thread-safe (some goroutines may still be busy with the
	// media player, or the media player may not have fully started).
	yt.running = false
	yt.mp.Quit()
	yt.mp = nil
	close(yt.outgoingMessages)
}

func (yt *YouTube) run(postData string) {
	log.Println("running YouTube:", postData)

	var err error

	// this appears to be a random number between 10000-99999
	yt.rid = rand.Intn(80000) + 10000

	c := config.Get()
	yt.uuid, err = c.GetString("apps.youtube.uuid", func () (string, error) {
		uuid, err := uuid.NewV4()
		if err != nil {
			return "", err
		}
		return uuid.String(), nil
	})
	if err != nil {
		panic(err)
	}
	yt.outgoingMessages = make(chan outgoingMessage, 3)
	yt.aid = -1

	stateChange := make(chan mp.StateChange)
	volumeChange := make(chan int)
	yt.mp = mp.New(stateChange, volumeChange)
	go yt.observeStateChange(stateChange)
	go yt.observeVolumeChange(volumeChange)

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

	yt.connect(values["pairingCode"][0])
}

func (yt *YouTube) Running() bool {
	return yt.running
}

func (yt *YouTube) connect(pairingCode string) {
	c := config.Get()

	screenId, err := c.GetString("apps.youtube.screenId", func() (string, error) {
		fmt.Println("Getting screen_id...")
		buf, err := httpGetBody("https://www.youtube.com/api/lounge/pairing/generate_screen_id")
		return string(buf), err
	})
	if err != nil {
		panic(err)
	}

	fmt.Println("Getting lounge token batch...")
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

	fmt.Println("Register pairing code...")
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
	fmt.Println("Getting first batch of messages")
	params := url.Values{
		"count": []string{"0"},
	}
	// TODO more fields should be query-escaped
	bindUrl := fmt.Sprintf("https://www.youtube.com/api/lounge/bc/bind?device=LOUNGE_SCREEN&id=%s&name=%s&loungeIdToken=%s&VER=8&RID=%d&zx=%s",
		yt.uuid, url.QueryEscape(yt.friendlyName), yt.loungeToken, yt.nextRid(), zx())
	resp, err := http.PostForm(bindUrl, params)
	if err != nil {
		panic(err)
	}

	yt.handleMessageStream(resp, true)

	// now yt.sid and yt.gsessionid should be defined, so sendMessages has
	// enough information to start

	go yt.sendMessages()

	for {
		bindUrl = fmt.Sprintf("https://www.youtube.com/api/lounge/bc/bind?device=LOUNGE_SCREEN&id=%s&name=%s&loungeIdToken=%s&VER=8&RID=rpc&SID=%s&CI=0&AID=%d&gsessionid=%s&TYPE=xmlhttp&zx=%s", yt.uuid, url.QueryEscape(yt.friendlyName), yt.loungeToken, yt.sid, yt.aid, yt.gsessionid, zx())

		timeBeforeGet := time.Now()

		resp, err = http.Get(bindUrl)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			yt.Stop()
			break
		}

		if resp.StatusCode != 200 {
			fmt.Fprintln(os.Stderr, "HTTP error while connecting to message channel:", resp.StatusCode)

			// most likely the YouTube server gives back an error in HTML form
			buf, err := ioutil.ReadAll(resp.Body)
			handle(err, "error while reading error message")
			fmt.Fprintf(os.Stderr, "Response body:\n%s\n\n", string(buf))

			yt.Stop()
			break
		}

		latency := time.Now().Sub(timeBeforeGet) / time.Millisecond * time.Millisecond
		fmt.Println(time.Now().Format("15:04:05.000"), "Connected to message channel in", latency)

		yt.handleMessageStream(resp, false)

		if !yt.running {
			// TODO this is a race condition
			break
		}
	}
}

func (yt *YouTube) handleMessageStream(resp *http.Response, singleBatch bool) {
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if line == "" && err == io.EOF {
				// The stream has terminated.
				return
			}

			fmt.Printf("error: %s (line: %#v)\n", err, line)

			// try again
			fmt.Println("Trying to reconnect to message channel...")
			return
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

		// TODO: This is a race condition.
		if !yt.running {
			break
		}

		messages := incomingMessagesJson{}
		json.Unmarshal(data, &messages)
		for _, message := range messages {
			yt.handleRawReceivedMessage(message)
		}

		if singleBatch {
			break
		}
	}
}

func (yt *YouTube) handleRawReceivedMessage(rawMessage incomingMessageJson) {
	message := incomingMessage{}
	message.index = int(rawMessage[0].(float64))
	message.command = rawMessage[1].([]interface{})[0].(string)
	message.args = make([]interface{}, len(rawMessage[1].([]interface{}))-1)
	for i := 0; i < len(message.args); i++ {
		message.args[i] = rawMessage[1].([]interface{})[i+1]
	}

	yt.handleReceivedMessage(&message)
}

func (yt *YouTube) handleReceivedMessage(message *incomingMessage) {
	if message.index <= int(yt.aid) {
		fmt.Println("old command:", message.index, message.command, message.args)
		return
	}
	yt.aid++
	if yt.aid != int32(message.index) {
		panic("missing some messages, message number=" + strconv.Itoa(message.index))
	}

	if !yt.running {
		fmt.Println("WARNING: got message after exit:", message.command)
		return
	}

	receiveTime := time.Now()

	switch message.command {
	case "noop":
		// no-op, ignore
	case "c":
		yt.sid = message.args[0].(string)
	case "S":
		yt.gsessionid = message.args[0].(string)
	case "remoteConnected":
		arguments := message.args[0].(map[string]interface{})
		fmt.Printf("Remote connected: %s (%s)\n", arguments["name"].(string), arguments["user"].(string))
	case "remoteDisconnected":
		arguments := message.args[0].(map[string]interface{})
		fmt.Printf("Remote disconnected: %s (%s)\n", arguments["name"].(string), arguments["user"].(string))
	case "getVolume":
		go func() {
			yt.sendVolume(yt.mp.GetPlaystate().Volume)
		}()
	case "setVolume":
		delta, ok := message.args[0].(map[string]interface{})["delta"]
		if ok {
			delta, err := strconv.Atoi(delta.(string))
			if err != nil {
				panic(err)
			}
			go func() {
				volumeChan := yt.mp.ChangeVolume(delta)
				yt.sendVolume(<-volumeChan)
			}()
		} else {
			volume, err := strconv.Atoi(message.args[0].(map[string]interface{})["volume"].(string))
			if err != nil {
				panic(err)
			}
			yt.mp.SetVolume(volume)
			yt.sendVolume(volume)
		}
	case "getPlaylist":
		yt.sendPlaylist()
	case "setPlaylist":
		go func() {
			arguments := message.args[0].(map[string]interface{})

			playlist := strings.Split(arguments["videoIds"].(string), ",")

			index, err := strconv.Atoi(arguments["currentIndex"].(string))
			if err != nil {
				panic(err)
			}

			position, err := time.ParseDuration(arguments["currentTime"].(string) + "s")
			if err != nil {
				panic(err)
			}

			yt.mp.SetPlaystate(playlist, index, position)
		}()
	case "updatePlaylist":
		go func() {
			arguments := message.args[0].(map[string]interface{})
			playlist := strings.Split(arguments["videoIds"].(string), ",")
			yt.mp.UpdatePlaylist(playlist)
		}()
	case "setVideo":
		arguments := message.args[0].(map[string]interface{})

		videoId := arguments["videoId"].(string)
		position, err := time.ParseDuration(arguments["currentTime"].(string) + "s")
		if err != nil {
			panic(err)
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
		position, err := time.ParseDuration(message.args[0].(map[string]interface{})["newTime"].(string) + "s")
		if err != nil {
			panic(err)
		}
		yt.mp.Seek(position)
	case "stopVideo":
		yt.mp.Stop()
	default:
		fmt.Println("unknown command:", message.index, message.command, message.args)
		return
	}

	if message.command != "noop" { // ignore verbose no-op
		fmt.Println(receiveTime.Format("15:04:05.000"), "command:", message.index, message.command, message.args)
	}
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
			yt.uuid, url.QueryEscape(yt.friendlyName), yt.loungeToken, yt.sid, yt.nextRid(), yt.aid, yt.gsessionid, zx()), values)
		if err != nil {
			panic(err)
		}

		latency := time.Now().Sub(timeBeforeSend) / time.Millisecond * time.Millisecond
		fmt.Println(time.Now().Format("15:04:05.000"), "send msg:", latency, message.command, message.args)

		count += 1
	}
}

func (yt *YouTube) observeStateChange(ch chan mp.StateChange) {
	for change := range ch {
		if change.State == mp.STATE_BUFFERING || change.State == mp.STATE_STOPPED {
			yt.sendNowPlaying()
		}
		yt.outgoingMessages <- outgoingMessage{"onStateChange", map[string]string{"currentTime": strconv.FormatFloat(change.Position.Seconds(), 'f', 3, 64), "state": strconv.Itoa(int(change.State))}}
	}
}

func (yt *YouTube) observeVolumeChange(ch chan int) {
	for volume := range ch {
		yt.sendVolume(volume)
	}
}

func (yt *YouTube) nextRid() int {
	yt.rid += 1
	return yt.rid
}
