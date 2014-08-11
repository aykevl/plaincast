package mpv

// This library is a wrapper around libmpv that plays YouTube playlists.

// #include <mpv/client.h>
// #include <stdlib.h>
// #cgo LDFLAGS: -lmpv
//
// /* some helper functions for string arrays */
// char** makeCharArray(int size) {
//     return calloc(sizeof(char*), size);
// }
// void setArrayString(char** a, int i, char* s) {
//     a[i] = s;
// }
import "C"
import "unsafe"

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"youtube-receiver/apps/youtube/mp"
)

// MPV is an implementation of mp.MediaPlayer, using libmpv.
type MPV struct {
	handle *C.mpv_handle

	// Two channels to handle passing of the play state without causing race
	// conditions.
	playstateChan     chan mp.PlayState
	playstateChanPing chan bool

	stateChange     chan mp.StateChange
	volumeChange    chan int
	streams         map[string]string // map of YouTube ID to stream gotten from youtube-dl
	playerEventChan chan C.mpv_event_id
}

// checkError checks for libmpv errors and panics if it finds one
func checkError(status C.int) {
	if status < 0 {
		// this C string should not be freed (it is static)
		panic(fmt.Sprintf("mpv API error: %s (%d)", C.GoString(C.mpv_error_string(status)), int(status)))
	}
}

// New creates a new MPV instance and initializes the libmpv player
func New(stateChange chan mp.StateChange, volumeChange chan int) *MPV {
	mpv := MPV{}
	mpv.handle = C.mpv_create()

	mpv.setOptionFlag("no-resume-playback", true)
	mpv.setOptionFlag("no-video", true)
	mpv.setOptionString("softvol", "auto")
	mpv.setOptionInt("volume", 100)

	// Cache settings assume 128kbps audio stream (16kByte/s).
	// The default is a cache size of 25MB, these are somewhat more sensible
	// cache sizes IMO.
	// Additionally, it's 160K because for some reason mpv gets really slow
	// while loading long streams when it's below 160K.
	// Due to a bug in ffmpeg, the whole stream is loaded before playback
	// begins, unless we work around that using "ignidx". See:
	//   https://github.com/mpv-player/mpv/issues/579
	//   https://trac.ffmpeg.org/ticket/3842
	mpv.setOptionInt("cache-default", 160)      // 10 seconds
	mpv.setOptionInt("cache-pause-below", 8)    // ½  second
	mpv.setOptionInt("cache-pause-restart", 16) // 1  seconds

	// work around bug https://github.com/mpv-player/mpv/issues/579
	mpv.setOptionString("demuxer-lavf-o", "fflags=+ignidx")

	// some extra debugging information, but don't read from stdin
	mpv.setOptionFlag("terminal", true)
	mpv.setOptionFlag("no-input-terminal", true)
	mpv.setOptionFlag("quiet", true)

	mpv.playstateChan = make(chan mp.PlayState)
	mpv.playstateChanPing = make(chan bool)
	mpv.stateChange = stateChange
	mpv.volumeChange = volumeChange
	mpv.streams = make(map[string]string)
	mpv.playerEventChan = make(chan C.mpv_event_id)

	checkError(C.mpv_initialize(mpv.handle))

	go mpv.run()
	go mpv.playerEventHandler()

	return &mpv
}

// Quit quits the player
func (mpv *MPV) Quit() {
	// TODO: fix race conditions
	close(mpv.stateChange)
	close(mpv.volumeChange)
	mpv.sendCommand([]string{"quit"})
}

// setOptionFlag passes a boolean flag to mpv
func (mpv *MPV) setOptionFlag(key string, value bool) {
	cValue := C.int(0)
	if value {
		cValue = 1
	}

	mpv.setOption(key, C.MPV_FORMAT_FLAG, unsafe.Pointer(&cValue))
}

// setOptionInt passes an integer option to mpv
func (mpv *MPV) setOptionInt(key string, value int) {
	cValue := C.int64_t(value)
	mpv.setOption(key, C.MPV_FORMAT_INT64, unsafe.Pointer(&cValue))
}

// setOptionString passes a string option to mpv
func (mpv *MPV) setOptionString(key, value string) {
	cValue := C.CString(value)
	defer C.free(unsafe.Pointer(cValue))

	mpv.setOption(key, C.MPV_FORMAT_STRING, unsafe.Pointer(&cValue))
}

// setOption is a generic function to pass options to mpv
func (mpv *MPV) setOption(key string, format C.mpv_format, value unsafe.Pointer) {
	cKey := C.CString(key)
	defer C.free(unsafe.Pointer(cKey))

	checkError(C.mpv_set_option(mpv.handle, cKey, format, value))
}

// sendCommand sends a command to the libmpv player
func (mpv *MPV) sendCommand(command []string) {
	fmt.Println("MPV command:", command)

	cArray := C.makeCharArray(C.int(len(command) + 1))
	if cArray == nil {
		panic("got NULL from calloc")
	}
	defer C.free(unsafe.Pointer(cArray))

	for i, s := range command {
		cStr := C.CString(s)
		C.setArrayString(cArray, C.int(i), cStr)
		defer C.free(unsafe.Pointer(cStr))
	}

	checkError(C.mpv_command_async(mpv.handle, 0, cArray))
}

// getProperty returns the MPV player property as a string
// Warning: this function can take an unbounded time. Call inside a new goroutine to prevent blocking / deadlocks.
func (mpv *MPV) getProperty(name string) string {
	cName := C.CString(name)
	defer C.free(unsafe.Pointer(cName))

	var cValue *C.char
	checkError(C.mpv_get_property(mpv.handle, cName, C.MPV_FORMAT_STRING, unsafe.Pointer(&cValue)))
	defer C.mpv_free(unsafe.Pointer(cValue))

	return C.GoString(cValue)
}

// setProperty sets the MPV player property
func (mpv *MPV) setProperty(name, value string) {
	cName := C.CString(name)
	defer C.free(unsafe.Pointer(cName))
	cValue := C.CString(value)
	defer C.free(unsafe.Pointer(cValue))

	// setProperty can take an unbounded time, don't block here using _async
	checkError(C.mpv_set_property_async(mpv.handle, 0, cName, C.MPV_FORMAT_STRING, unsafe.Pointer(&cValue)))
}

// GetPlaystate returns the play state synchronously (may block)
func (mpv *MPV) GetPlaystate() *mp.PlayState {
	mpv.playstateChanPing <- false
	ps := <-mpv.playstateChan
	return &ps
}

func (mpv *MPV) changePlaystate(callback func(*mp.PlayState)) {
	mpv.playstateChanPing <- true
	ps := <-mpv.playstateChan
	callback(&ps)
	mpv.playstateChan <- ps
}

// SetPlaystate changes the play state to the specified arguments
// This function doesn't block, but changes may not be immediately applied.
func (mpv *MPV) SetPlaystate(playlist []string, index int, position time.Duration) {
	go mpv.changePlaystate(func(ps *mp.PlayState) {
		ps.Playlist = playlist
		ps.Index = index
		ps.Position = position

		if len(ps.Playlist) > 0 {
			mpv.startPlaying(ps)
		} else {
			mpv.Stop()
		}
	})
}

func (mpv *MPV) startPlaying(ps *mp.PlayState) {
	mpv.setPlayState(ps, mp.StateBuffering)

	videoId := ps.Playlist[ps.Index]
	position := ps.Position

	// do not use the playstate inside this goroutine
	go func() {
		streamUrl := mpv.getYouTubeStream(videoId)
		if position == 0 {
			mpv.sendCommand([]string{"loadfile", streamUrl, "replace", "pause=no"})
		} else {
			mpv.sendCommand([]string{"loadfile", streamUrl, "replace", "pause=yes"})
			mpv.sendCommand([]string{"seek", fmt.Sprintf("%.3f", position.Seconds()), "absolute"})
			mpv.setProperty("pause", "no")
		}
	}()
}

func (mpv *MPV) getYouTubeStream(videoId string) string {
	if stream, ok := mpv.streams[videoId]; ok {
		return stream
	}

	youtubeUrl := "http://www.youtube.com/watch?v=" + videoId

	fmt.Println("Fetching YouTube stream...", youtubeUrl)
	cmd := exec.Command("youtube-dl", "-g", "-f", "bestaudio", youtubeUrl)
	cmd.Stderr = os.Stderr
	output, err := cmd.Output()
	fmt.Println("Got stream.")
	if err != nil {
		panic(err)
	}
	stream := strings.TrimSpace(string(output))
	mpv.streams[videoId] = stream
	return stream
}

func (mpv *MPV) setPlayState(ps *mp.PlayState, state mp.State) {
	if state == ps.State {
		fmt.Printf("WARNING: state %d did not change\n", state)
	}
	ps.State = state

	go func() {
		mpv.stateChange <- mp.StateChange{ps.State, ps.Position}
	}()
}

func (mpv *MPV) UpdatePlaylist(playlist []string) {
	go mpv.changePlaystate(func(ps *mp.PlayState) {

		if len(ps.Playlist) == 0 {

			if ps.State == mp.StatePlaying {
				// just to be sure
				panic("empty playlist while playing")
			}
			ps.Playlist = playlist

			if ps.Index >= len(playlist) {
				// this appears to be the normal behavior of YouTube
				ps.Index = len(playlist) - 1
			}

			if ps.State == mp.StateStopped {
				ps.Position = 0
				mpv.startPlaying(ps)
			}

		} else {
			videoId := ps.Playlist[ps.Index]
			ps.Playlist = playlist
			mpv.setPlaylistIndex(ps, videoId)
		}
	})
}

func (mpv *MPV) SetVideo(videoId string, position time.Duration) {
	go mpv.changePlaystate(func(ps *mp.PlayState) {
		mpv.setPlaylistIndex(ps, videoId)
		ps.Position = position
		mpv.startPlaying(ps)
	})
}

func (mpv *MPV) setPlaylistIndex(ps *mp.PlayState, videoId string) {
	newIndex := -1
	for i, v := range ps.Playlist {
		if v == videoId {
			if newIndex >= 0 {
				fmt.Fprintln(os.Stderr, "WARNING: videoId exists twice in playlist")
				break
			}
			newIndex = i
			// no 'break' so duplicate video entries can be checked
		}
	}

	if newIndex == -1 {
		// don't know how to proceed
		panic("current video does not exist in new playlist")
	}

	ps.Index = newIndex
}

// Resume resumes playback when it was paused
func (mpv *MPV) Resume() {
	go mpv.changePlaystate(func(ps *mp.PlayState) {
		if ps.State != mp.StatePaused {
			fmt.Printf("Warning: resume while in state %d\n", ps.State)
		}

		mpv.setProperty("pause", "no")
	})
}

// Pause pauses the currently playing video
func (mpv *MPV) Pause() {
	go mpv.changePlaystate(func(ps *mp.PlayState) {
		if ps.State != mp.StatePlaying {
			fmt.Printf("Warning: pause while in state %d\n", ps.State)
		}

		mpv.setProperty("pause", "yes")
	})
}

// Seek jumps to the specified position
func (mpv *MPV) Seek(position time.Duration) {
	go mpv.changePlaystate(func(ps *mp.PlayState) {
		if ps.State != mp.StatePaused {
			fmt.Printf("Warning: state is not paused while seeking (state: %d)\n", ps.State)
		}

		mpv.sendCommand([]string{"seek", fmt.Sprintf("%.3f", position.Seconds()), "absolute"})
		mpv.updatePosition(ps)
	})
}

// updatePosition gets the current position from the player
func (mpv *MPV) updatePosition(ps *mp.PlayState) {
	// Also give 0 position on buffering because we can't ask mpv where we are
	// while we are fetching the YouTube stream.
	if ps.State == mp.StateStopped || ps.State == mp.StateBuffering {
		ps.Position = 0
		return
	}
	position, err := time.ParseDuration(mpv.getProperty("time-pos") + "s")
	if err != nil {
		panic(err)
	}
	ps.Position = position
}

func (mpv *MPV) SetVolume(volume int) {
	mpv.changePlaystate(func(ps *mp.PlayState) {
		ps.Volume = volume
		mpv.setProperty("volume", strconv.Itoa(volume))
	})
}

func (mpv *MPV) Stop() {
	go mpv.changePlaystate(func(ps *mp.PlayState) {
		ps.Playlist = []string{}
		// Do not set ps.Index to 0, it may be needed for UpdatePlaylist:
		// Stop is called before UpdatePlaylist when removing the currently
		// playing video from the playlist.
		ps.Position = 0
		ps.State = mp.StateStopped
		mpv.sendCommand([]string{"stop"})
	})
}

func (mpv *MPV) run() {
	ps := mp.PlayState{}

	ps.Volume = -1
	volumeInitialized := false

	for {
		select {
		case replace := <-mpv.playstateChanPing:
			mpv.updatePosition(&ps)
			mpv.playstateChan <- ps
			if replace {
				ps = <-mpv.playstateChan
			}

		case event := <-mpv.playerEventChan:
			switch event {
			case C.MPV_EVENT_PLAYBACK_RESTART:
				mpv.setPlayState(&ps, mp.StatePlaying)

				if !volumeInitialized {
					volumeInitialized = true

					volume, err := strconv.ParseFloat(mpv.getProperty("volume"), 64)
					if err != nil {
						panic(err)
					}

					ps.Volume = int(volume + 0.5)
					mpv.volumeChange <- ps.Volume
				}

			case C.MPV_EVENT_END_FILE:
				if ps.Index+1 < len(ps.Playlist) {
					// there are more videos, play the next
					ps.Index++
					ps.Position = 0
					mpv.startPlaying(&ps)
				} else {
					// signal that the video has stopped playing
					// this resets the position but keeps the playlist
					ps.Position = 0
					mpv.setPlayState(&ps, mp.StateStopped)
				}

			case C.MPV_EVENT_PAUSE:
				mpv.setPlayState(&ps, mp.StatePaused)

			case C.MPV_EVENT_UNPAUSE:
				mpv.setPlayState(&ps, mp.StatePlaying)
			}
		}
	}
}

// playerEventHandler waits for libmpv player events and sends them on a channel
func (mpv *MPV) playerEventHandler() {
	for {
		// wait until there is an event (negative timeout means infinite timeout)
		event := C.mpv_wait_event(mpv.handle, -1)
		fmt.Printf("MPV event: %s %s (%d)\n", time.Now(), C.GoString(C.mpv_event_name(event.event_id)), int(event.event_id))
		if event.event_id == C.MPV_EVENT_SHUTDOWN {
			mpv.handle = nil
			break
		}

		mpv.playerEventChan <- event.event_id
	}
}
