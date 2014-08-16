package mp

import (
	"time"
)

type Backend interface {
	initialize() chan State
	quit()
	play(string, time.Duration)
	pause()
	resume()
	setPosition(time.Duration)
	getPosition() time.Duration
	getVolume() int
	setVolume(int)
	stop()
}
