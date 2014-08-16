package mp

import (
	"time"
)

type Backend interface {
	initialize() chan playerEvent
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

type playerEvent int

const (
	PLAYER_EVENT_RESTART playerEvent = iota // playback has started or resumed from (e.g.) buffering
	PLAYER_EVENT_PAUSE
	PLAYER_EVENT_UNPAUSE
	PLAYER_EVENT_END  // playback of current file/stream has ended
	PLAYER_EVENT_QUIT // player has quit
)
