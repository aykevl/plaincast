package youtube

import (
	"bufio"
	"encoding/json"
	"fmt"
	"github.com/aykevl93/youtube-receiver/apps/youtube/mp"
	"github.com/nu7hatch/gouuid"
	"io"
	"io/ioutil"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

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

// JSON data structures for get_lounge_token_batch
type loungeTokenBatchJson struct {
	Screens []screenTokenJson "screens"
}
type screenTokenJson struct {
	ScreenId    string "screenId"
	Expiration  int64  "expiration"
	LoungeToken string "loungeToken"
}

type incomingMessagesJson []incomingMessageJson
type incomingMessageJson []interface{}
type incomingMessage struct {
	index   int
	command string
	args    []interface{}
}

type outgoingMessage struct {
	command string
	args    map[string]string
}

func New(friendlyName string) *YouTube {
	yt := YouTube{}
	yt.friendlyName = friendlyName
	return &yt
}

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
	fmt.Println("running YouTube:", postData)

	// this appears to be a random number between 10000-99999
	yt.rid = rand.Intn(80000) + 10000
	uuid, err := uuid.NewV4()
	if err != nil {
		panic(err)
	}
	yt.uuid = uuid.String()
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
	fmt.Println("Getting screen_id...")
	screenId := string(mustGet("https://www.youtube.com/api/lounge/pairing/generate_screen_id"))

	fmt.Println("Getting lounge token batch...")
	params := url.Values{
		"screen_ids": []string{screenId},
	}
	data := mustPostForm("https://www.youtube.com/api/lounge/pairing/get_lounge_token_batch", params)
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
	mustPostForm("https://www.youtube.com/api/lounge/pairing/register_pairing_code", params)
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
	// now yt.sid and yt.gsessionid should be defined

	go yt.sendMessages()

	for {
		fmt.Println("Connecting to message channel...")
		bindUrl = fmt.Sprintf("https://www.youtube.com/api/lounge/bc/bind?device=LOUNGE_SCREEN&id=%s&name=%s&loungeIdToken=%s&VER=8&RID=rpc&SID=%s&CI=0&AID=%d&gsessionid=%s&TYPE=xmlhttp&zx=%s", yt.uuid, url.QueryEscape(yt.friendlyName), yt.loungeToken, yt.sid, yt.aid, yt.gsessionid, zx())
		resp, err = http.Get(bindUrl)
		if err != nil {
			panic(err)
		}

		fmt.Println("Connected.")
		yt.handleMessageStream(resp, false)

		if !yt.running {
			break
		}
	}
}

func (yt *YouTube) handleMessageStream(resp *http.Response, singleBatch bool) {
	defer resp.Body.Close()

	for {
		reader := bufio.NewReader(resp.Body)
		line, err := reader.ReadString('\n')
		if err == io.EOF {
			// end of stream
			return
		}
		if err != nil {
			panic(err)
		}

		length, err := strconv.Atoi(line[:len(line)-1])
		if err != nil {
			// most likely the YouTube server gives back an error in HTML form
			buf, err2 := ioutil.ReadAll(reader)
			if err != nil {
				panic(err2)
			}
			fmt.Printf("Got this while waiting for a new message:\n%s\n\n", line+string(buf))

			panic(err)
		}

		data := make([]byte, length)
		_, err = io.ReadFull(reader, data)
		if err != nil {
			panic(err)
		}

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
	yt.aid = int32(message.index)

	if !yt.running {
		fmt.Println("WARNING: got message after exit:", message.command)
		return
	}

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
		// sometimes the YouTube app doesn't ask for the playlist. This sends the playlist proactively.
		yt.sendPlaylist()
	case "remoteDisconnected":
		arguments := message.args[0].(map[string]interface{})
		fmt.Printf("Remote disconnected: %s (%s)\n", arguments["name"].(string), arguments["user"].(string))
	case "getVolume":
		go func() {
			yt.sendVolume(yt.mp.GetPlaystate().Volume)
		}()
	case "setVolume":
		delta, err := strconv.Atoi(message.args[0].(map[string]interface{})["delta"].(string))
		if err != nil {
			panic(err)
		}
		go func() {
			volumeChan := yt.mp.ChangeVolume(delta)
			yt.sendVolume(<-volumeChan)
		}()
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
		yt.mp.Resume()
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

	fmt.Println(time.Now().Format("15:04:05.000"), "command:", message.index, message.command, message.args)
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
		fmt.Println(time.Now().Format("15:04:05.000"), "send msg:", message.command, message.args)
		mustPostForm(fmt.Sprintf("https://www.youtube.com/api/lounge/bc/bind?device=LOUNGE_SCREEN&id=%s&name=%s&loungeIdToken=%s&VER=8&SID=%s&RID=%d&AID=%d&gsessionid=%s&zx=%s", yt.uuid, url.QueryEscape(yt.friendlyName), yt.loungeToken, yt.sid, yt.nextRid(), yt.aid, yt.gsessionid, zx()), values)
		count += 1
	}
}

func (yt *YouTube) observeStateChange(ch chan mp.StateChange) {
	for change := range ch {
		if change.State == mp.STATE_BUFFERING {
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
