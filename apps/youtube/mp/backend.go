package mp

import (
	"time"
)

type Backend interface {
	initialize() (chan State, int)
	quit()
	play(string, time.Duration, int)
	pause()
	resume()
	getPosition() (time.Duration, error)
	setPosition(time.Duration)
	setVolume(int)
	stop()
}
