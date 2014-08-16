package mp

// This library is a wrapper around a media player that plays YouTube playlists.

import (
	"time"
)

// these are defined by the YouTube API
type State int

const (
	STATE_STOPPED State = iota
	STATE_PLAYING
	STATE_PAUSED
	STATE_BUFFERING
)

type PlayState struct {
	Playlist []string
	Index    int
	Position time.Duration // position in playing video
	State    State
	Volume   int
}

type StateChange struct {
	State    State
	Position time.Duration
}
