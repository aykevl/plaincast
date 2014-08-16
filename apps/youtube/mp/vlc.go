package mp

// #include <vlc/vlc.h>
// #include <stdlib.h>
// #cgo LDFLAGS: -lvlc
//
// extern void vlc_callback_helper_go(struct libvlc_event_t *event, void *userdata);
//
// static inline void callback_helper(const struct libvlc_event_t *event, void *userdata) {
//     /* wrap it here to get rid of the 'const' parameter which doesn't exist in Go */
//     vlc_callback_helper_go((struct libvlc_event_t*)event, userdata);
// }
// static inline libvlc_callback_t callback_helper_var() {
//     return callback_helper;
// }
import "C"
import "unsafe"

import (
	"fmt"
	"time"
)

type VLC struct {
	commandChan chan func(*vlcInstance)
}

// this data is separate to ensure it is only used synchronously
type vlcInstance struct {
	instance  *C.libvlc_instance_t
	player    *C.libvlc_media_player_t
	eventChan chan State
}

type vlcEvent struct {
	id        int
	eventType C.libvlc_event_type_t
	callback  func()
}

// store event data here so the garbage collector doesn't trash them
var vlcEvents = make(map[int]*vlcEvent)
var vlcNextEventId int

//export vlc_callback_helper_go
func vlc_callback_helper_go(event *C.struct_libvlc_event_t, userdata unsafe.Pointer) {
	eventData := (*vlcEvent)(userdata)
	fmt.Println("vlc event:", time.Now().Format("15:04:05.000"), C.GoString(C.libvlc_event_type_name(C.libvlc_event_type_t(event._type))))
	eventData.callback() // Yeah! We're finally running our callback!
}

func (v *VLC) initialize() chan State {

	i := vlcInstance{}
	i.instance = C.libvlc_new(0, nil)
	if i.instance == nil {
		panic("C.libvlc_new returned NULL")
	}
	i.player = C.libvlc_media_player_new(i.instance)
	if i.player == nil {
		panic("C.libvlc_media_player_new returned NULL")
	}

	v.commandChan = make(chan func(*vlcInstance))
	i.eventChan = make(chan State)

	go v.run(&i)

	return i.eventChan
}

func (v *VLC) run(i *vlcInstance) {

	for {
		select {
		case c, ok := <-v.commandChan:
			if !ok {
				C.libvlc_media_player_release(i.player)
				i.player = nil
				C.libvlc_release(i.instance)
				i.instance = nil
				// channel is closed when player must quit

				close(i.eventChan)
				return
			}
			c(i)
		}
	}
}

func (v *VLC) quit() {
	// signal the end of the player
	// Don't allow new commands to be sent
	close(v.commandChan)
}

func (v *VLC) play(stream string, position time.Duration) {
	v.commandChan <- func(i *vlcInstance) {
		cStream := C.CString(stream)
		defer C.free(unsafe.Pointer(cStream))

		media := C.libvlc_media_new_location(i.instance, cStream)
		defer C.libvlc_media_release(media)

		C.libvlc_media_player_set_media(i.player, media)

		eventManager := C.libvlc_media_player_event_manager(i.player)
		// all empty event handlers are there just to trigger the log
		v.addEvent(eventManager, C.libvlc_MediaPlayerMediaChanged, func() {})
		v.addEvent(eventManager, C.libvlc_MediaPlayerOpening, func() {})
		v.addEvent(eventManager, C.libvlc_MediaPlayerBuffering, func() {})
		v.addEvent(eventManager, C.libvlc_MediaPlayerPlaying, func() {
			i.eventChan <- STATE_PLAYING
		})
		v.addEvent(eventManager, C.libvlc_MediaPlayerPaused, func() {
			i.eventChan <- STATE_PAUSED
		})
		v.addEvent(eventManager, C.libvlc_MediaPlayerStopped, func() {})
		v.addEvent(eventManager, C.libvlc_MediaPlayerEndReached, func() {
			i.eventChan <- STATE_STOPPED
		})

		// TODO seek to position if needed

		v.checkError(C.libvlc_media_player_play(i.player))
	}
}

func (v *VLC) pause() {
	v.commandChan <- func(i *vlcInstance) {
		C.libvlc_media_player_set_pause(i.player, 1)
	}
}

func (v *VLC) resume() {
	v.commandChan <- func(i *vlcInstance) {
		C.libvlc_media_player_set_pause(i.player, 0)
	}
}

func (v *VLC) getPosition() time.Duration {
	posChan := make(chan time.Duration)
	v.commandChan <- func(i *vlcInstance) {
		position := C.libvlc_media_player_get_time(i.player)
		if position == -1 {
			panic("there is no media while getting position")
		}
		posChan <- time.Duration(position) * time.Millisecond
	}
	return <-posChan
}

func (v *VLC) setPosition(position time.Duration) {
	v.commandChan <- func(i *vlcInstance) {
		C.libvlc_media_player_set_time(i.player, C.libvlc_time_t(position.Seconds()*1000+0.5))
	}
}

func (v *VLC) getVolume() int {
	volumeChan := make(chan int)
	v.commandChan <- func(i *vlcInstance) {
		volume := C.libvlc_audio_get_volume(i.player)
		volumeChan <- int(volume)
	}
	return <-volumeChan
}

func (v *VLC) setVolume(volume int) {
	v.commandChan <- func(i *vlcInstance) {
		v.checkError(C.libvlc_audio_set_volume(i.player, C.int(volume)))
	}
}

func (v *VLC) stop() {
	v.commandChan <- func(i *vlcInstance) {
		C.libvlc_media_player_stop(i.player)
	}
}

func (v *VLC) checkError(status C.int) {
	if status < 0 {
		panic(status)
	}
}

func (v *VLC) addEvent(manager *C.libvlc_event_manager_t, eventType C.libvlc_event_type_t, callback func()) {
	id := vlcNextEventId
	vlcNextEventId++

	event := &vlcEvent{id, eventType, callback}
	vlcEvents[id] = event

	v.checkError(C.libvlc_event_attach(manager, eventType, C.callback_helper_var(), unsafe.Pointer(event)))
}
