package mp

import (
	"time"
)

// media player interface

type MediaPlayer interface {
	GetPlaystate() *PlayState
	SetPlaystate(playlist []string, index int, position time.Duration)
	UpdatePlaylist(playlist []string)
	SetVideo(videoId string, position time.Duration)
	Resume()
	Pause()
	Seek(position time.Duration)
	SetVolume(volume int)
	Stop()
	Quit()
}

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
