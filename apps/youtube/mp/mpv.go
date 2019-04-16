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
	"errors"
	"flag"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aykevl/plaincast/config"
	"github.com/aykevl/plaincast/log"
)

var MPV_PROPERTY_UNAVAILABLE = errors.New("mpv: property unavailable")

// MPV is an implementation of Backend, using libmpv.
type MPV struct {
	handle       *C.mpv_handle
	running      bool
	runningMutex sync.Mutex
	mainloopExit chan struct{}
}

var mpvLogger = log.New("mpv", "Log MPV wrapper output")
var logLibMPV = flag.Bool("log-libmpv", false, "Log output of libmpv")
var flagPCM = flag.String("ao-pcm", "", "Write audio to a file, 48kHz stereo format S16")
var httpPort string

// New creates a new MPV instance and initializes the libmpv player
func (mpv *MPV) initialize() (chan State, int) {

 	httpPort = flag.Lookup("http-port").Value.String()

	if mpv.handle != nil || mpv.running {
		panic("already initialized")
	}

	mpv.mainloopExit = make(chan struct{})
	mpv.running = true

	mpv.handle = C.mpv_create()

	conf := config.Get()
	initialVolume, err := conf.GetInt("player.mpv.volume", func() (int, error) {
		return INITIAL_VOLUME, nil
	})
	if err != nil {
		// should not happen
		panic(err)
	}

	mpv.setOptionFlag("resume-playback", false)
	//mpv.setOptionString("softvol", "yes")
	//mpv.setOptionString("ao", "pulse")
	mpv.setOptionInt("volume", initialVolume)

	// Disable video in three ways.
	mpv.setOptionFlag("video", false)
	mpv.setOptionString("vo", "null")
	mpv.setOptionString("vid", "no")


        if *flagPCM != "" {
	logger.Println("Writing sound to file: %s", *flagPCM)
	mpv.setOptionString("audio-channels", "stereo")
	mpv.setOptionString("audio-samplerate", "48000")
	mpv.setOptionString("audio-format", "s16")
	mpv.setOptionString("ao", "pcm")
	mpv.setOptionString("ao-pcm-file", *flagPCM)
        }

	// Cache settings assume 128kbps audio stream (16kByte/s).
	// The default is a cache size of 25MB, these are somewhat more sensible
	// cache sizes IMO.
	mpv.setOptionInt("cache-default", 160) // 10 seconds
	mpv.setOptionInt("cache-seek-min", 16) // 1 second

	// Some extra debugging information, but don't read from stdin.
	// libmpv has a problem with signal handling, though: when `terminal` is
	// true, Ctrl+C doesn't work correctly anymore and program output is
	// disabled.
	mpv.setOptionFlag("terminal", *logLibMPV)
	mpv.setOptionFlag("input-terminal", false)
	mpv.setOptionFlag("quiet", true)

	mpv.checkError(C.mpv_initialize(mpv.handle))

	eventChan := make(chan State)

	go mpv.eventHandler(eventChan)

	return eventChan, initialVolume
}

// Function quit quits the player.
// WARNING: This MUST be the last call on this media player.
func (mpv *MPV) quit() {
	mpv.runningMutex.Lock()
	if !mpv.running {
		panic("quit called twice")
	}
	mpv.running = false
	mpv.runningMutex.Unlock()

	// Wake up the event handler mainloop, probably sending the MPV_EVENT_NONE
	// signal.
	// See mpv_wait_event below: this doesn't work yet (it uses a workaround
	// now).
	//C.mpv_wakeup(handle)

	// Wait until the mainloop has exited.
	<-mpv.mainloopExit

	// Actually destroy the MPV player. This blocks until the player has been
	// fully brought down.
	handle := mpv.handle
	mpv.handle = nil // make it easier to catch race conditions
	C.mpv_terminate_destroy(handle)
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
	// Print command, but without the stream
	cmd := make([]string, len(command))
	copy(cmd, command)
	if command[0] == "loadfile" {
		cmd[1] = "<stream>"
	}
	logger.Println("MPV command:", cmd)

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
// Warning: this function can take an unbounded time. Call inside a new
// goroutine to prevent blocking / deadlocks.
func (mpv *MPV) getProperty(name string) (float64, error) {
	logger.Printf("MPV get property: %s\n", name)

	cName := C.CString(name)
	defer C.free(unsafe.Pointer(cName))

	var cValue C.double
	status := C.mpv_get_property(mpv.handle, cName, C.MPV_FORMAT_DOUBLE, unsafe.Pointer(&cValue))
	if status == C.MPV_ERROR_PROPERTY_UNAVAILABLE {
		return 0, MPV_PROPERTY_UNAVAILABLE
	} else if status != 0 {
		return 0, errors.New("mpv: " + C.GoString(C.mpv_error_string(status)))
	}

	return float64(cValue), nil
}

// setProperty sets the MPV player property
func (mpv *MPV) setProperty(name, value string) {
	logger.Printf("MPV set property: %s=%s\n", name, value)

	cName := C.CString(name)
	defer C.free(unsafe.Pointer(cName))
	cValue := C.CString(value)
	defer C.free(unsafe.Pointer(cValue))

	// setProperty can take an unbounded time, don't block here using _async
	// TODO: use some form of error handling. Sometimes, it is impossible to
	// know beforehand whether setting a property will cause an error.
	// Importantly, catch the 'property unavailable' error.
	mpv.checkError(C.mpv_set_property_async(mpv.handle, 1, cName, C.MPV_FORMAT_STRING, unsafe.Pointer(&cValue)))
}

func (mpv *MPV) play(stream string, position time.Duration, volume int) {
	options := "pause=no"

	if position != 0 {
		options += fmt.Sprintf(",start=%.3f", position.Seconds())
	}

	if volume >= 0 {
		options += fmt.Sprintf(",volume=%d", volume)
	}

	// The proxy is a workaround for misbehaving libav/libnettle that appear to
	// try to read the whole HTTP response before closing the connection. Go has
	// a better HTTPS implementation, which is used here as a workaround.
	// This libav/libnettle combination is in use on Debian jessie. FFmpeg
	// doesn't have a problem with it.
	if !strings.HasPrefix(stream, "https://") {
		logger.Panic("Stream does not start with https://...")
	}
	mpv.sendCommand([]string{"loadfile", "http://localhost:" + httpPort + "/proxy/" + stream[len("https://"):], "replace", options})
}

func (mpv *MPV) pause() {
	mpv.setProperty("pause", "yes")
}

func (mpv *MPV) resume() {
	mpv.setProperty("pause", "no")
}

func (mpv *MPV) getDuration() (time.Duration, error) {
	duration, err := mpv.getProperty("duration")
	if err == MPV_PROPERTY_UNAVAILABLE {
		return 0, PROPERTY_UNAVAILABLE
	} else if err != nil {
		// should not happen
		panic(err)
	}

	return time.Duration(duration * float64(time.Second)), nil
}

func (mpv *MPV) getPosition() (time.Duration, error) {
	position, err := mpv.getProperty("time-pos")
	if err == MPV_PROPERTY_UNAVAILABLE {
		return 0, PROPERTY_UNAVAILABLE
	} else if err != nil {
		// should not happen
		panic(err)
	}

	if position < 0 {
		// Sometimes, the position appears to be slightly off.
		position = 0
	}

	return time.Duration(position * float64(time.Second)), nil
}

func (mpv *MPV) setPosition(position time.Duration) {
	mpv.sendCommand([]string{"seek", fmt.Sprintf("%.3f", position.Seconds()), "absolute"})
}

func (mpv *MPV) getVolume() int {
	volume, err := mpv.getProperty("volume")
	if err != nil {
		// should not happen
		panic(err)
	}

	return int(volume + 0.5)
}

func (mpv *MPV) setVolume(volume int) {
	mpv.setProperty("volume", strconv.Itoa(volume))
	config.Get().SetInt("player.mpv.volume", volume)
}

func (mpv *MPV) stop() {
	mpv.sendCommand([]string{"stop"})
}

// playerEventHandler waits for libmpv player events and sends them on a channel
func (mpv *MPV) eventHandler(eventChan chan State) {
	for {
		// wait until there is an event (negative timeout means infinite timeout)
		// The timeout is 1 second to work around libmpv bug #1372 (mpv_wakeup
		// does not actually wake up mpv_wait_event). It keeps checking every
		// second whether MPV has exited.
		// TODO revert this as soon as the fix for that bug lands in a stable
		// release. Check for the problematic versions and keep the old behavior
		// for older MPV versions.
		event := C.mpv_wait_event(mpv.handle, 1)
		if event.event_id != C.MPV_EVENT_NONE {
			logger.Printf("MPV event: %s (%d)\n", C.GoString(C.mpv_event_name(event.event_id)), int(event.event_id))
		}

		if event.error != 0 {
			panic("MPV API error")
		}

		mpv.runningMutex.Lock()
		running := mpv.running
		mpv.runningMutex.Unlock()

		if !running {
			close(eventChan)
			mpv.mainloopExit <- struct{}{}
			return
		}

		switch event.event_id {
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
