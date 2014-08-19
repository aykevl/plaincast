// +build ignore

package mp

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
	"strconv"
	"time"
)

// MPV is an implementation of Backend, using libmpv.
type MPV struct {
	handle *C.mpv_handle
}

// New creates a new MPV instance and initializes the libmpv player
func (mpv *MPV) initialize() chan State {
	if mpv.handle != nil {
		panic("already initialized")
	}

	mpv.handle = C.mpv_create()

	mpv.setOptionFlag("no-resume-playback", true)
	mpv.setOptionFlag("no-video", true)
	mpv.setOptionString("softvol", "yes")
	mpv.setOptionInt("volume", INITIAL_VOLUME)

	// Cache settings assume 128kbps audio stream (16kByte/s).
	// The default is a cache size of 25MB, these are somewhat more sensible
	// cache sizes IMO.
	mpv.setOptionInt("cache-default", 160)      // 10 seconds
	mpv.setOptionInt("cache-pause-below", 8)    // ½  second
	mpv.setOptionInt("cache-pause-restart", 16) // 1  seconds

	// some extra debugging information, but don't read from stdin
	mpv.setOptionFlag("terminal", true)
	mpv.setOptionFlag("no-input-terminal", true)
	mpv.setOptionFlag("quiet", true)

	mpv.checkError(C.mpv_initialize(mpv.handle))

	eventChan := make(chan State)

	go mpv.eventHandler(eventChan)

	return eventChan
}

// Quit quits the player
func (mpv *MPV) quit() {
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

	mpv.checkError(C.mpv_set_option(mpv.handle, cKey, format, value))
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

	mpv.checkError(C.mpv_command_async(mpv.handle, 0, cArray))
}

// getProperty returns the MPV player property as a string
// Warning: this function can take an unbounded time. Call inside a new goroutine to prevent blocking / deadlocks.
func (mpv *MPV) getProperty(name string) string {
	cName := C.CString(name)
	defer C.free(unsafe.Pointer(cName))

	var cValue *C.char
	mpv.checkError(C.mpv_get_property(mpv.handle, cName, C.MPV_FORMAT_STRING, unsafe.Pointer(&cValue)))
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
	mpv.checkError(C.mpv_set_property_async(mpv.handle, 0, cName, C.MPV_FORMAT_STRING, unsafe.Pointer(&cValue)))
}

func (mpv *MPV) play(stream string, position time.Duration) {
	if position == 0 {
		mpv.sendCommand([]string{"loadfile", stream, "replace", "pause=no"})
	} else {
		mpv.sendCommand([]string{"loadfile", stream, "replace", "pause=yes"})
		mpv.sendCommand([]string{"seek", fmt.Sprintf("%.3f", position.Seconds()), "absolute"})
		mpv.setProperty("pause", "no")
	}
}

func (mpv *MPV) pause() {
	mpv.setProperty("pause", "yes")
}

func (mpv *MPV) resume() {
	mpv.setProperty("pause", "no")
}

func (mpv *MPV) getPosition() time.Duration {
	position, err := time.ParseDuration(mpv.getProperty("time-pos") + "s")
	if err != nil {
		// should never happen
		panic(err)
	}
	return position
}

func (mpv *MPV) setPosition(position time.Duration) {
	mpv.sendCommand([]string{"seek", fmt.Sprintf("%.3f", position.Seconds()), "absolute"})
}

func (mpv *MPV) getVolume() int {
	volume, err := strconv.ParseFloat(mpv.getProperty("volume"), 64)
	if err != nil {
		// should never happen
		panic(err)
	}

	return int(volume + 0.5)
}

func (mpv *MPV) setVolume(volume int) {
	mpv.setProperty("volume", strconv.Itoa(volume))
}

func (mpv *MPV) stop() {
	mpv.sendCommand([]string{"stop"})
}

// playerEventHandler waits for libmpv player events and sends them on a channel
func (mpv *MPV) eventHandler(eventChan chan State) {
loop:
	for {
		// wait until there is an event (negative timeout means infinite timeout)
		event := C.mpv_wait_event(mpv.handle, -1)
		fmt.Printf("MPV event: %s %s (%d)\n", time.Now(), C.GoString(C.mpv_event_name(event.event_id)), int(event.event_id))

		switch event.event_id {
		case C.MPV_EVENT_SHUTDOWN:
			close(eventChan)
			mpv.handle = nil
			break loop
		case C.MPV_EVENT_PLAYBACK_RESTART:
			eventChan <- STATE_PLAYING
		case C.MPV_EVENT_END_FILE:
			eventChan <- STATE_STOPPED
		case C.MPV_EVENT_PAUSE:
			eventChan <- STATE_PAUSED
		case C.MPV_EVENT_UNPAUSE:
			eventChan <- STATE_PLAYING
		}
	}
}

// checkError checks for libmpv errors and panics if it finds one
func (mpv *MPV) checkError(status C.int) {
	if status < 0 {
		// this C string should not be freed (it is static)
		panic(fmt.Sprintf("mpv API error: %s (%d)", C.GoString(C.mpv_error_string(status)), int(status)))
	}
}
