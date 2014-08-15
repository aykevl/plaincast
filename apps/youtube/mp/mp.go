package mp

// This library is a wrapper around a media player that plays YouTube playlists.

import (
	"time"
)

// these are defined by the YouTube API
type State int

const (
	StateStopped State = iota
	StatePlaying
	StatePaused
	StateBuffering
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
