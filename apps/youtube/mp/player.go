package mp

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

type MediaPlayer struct {
	player       Backend
	stateChange  chan StateChange
	volumeChange chan int

	// Two channels to handle passing of the play state without causing race
	// conditions.
	playstateChan     chan PlayState
	playstateChanPing chan bool

	streams map[string]string // map of YouTube ID to stream gotten from youtube-dl
}

func New(stateChange chan StateChange, volumeChange chan int) *MediaPlayer {
	p := MediaPlayer{}

	p.playstateChan = make(chan PlayState)
	p.playstateChanPing = make(chan bool)
	p.stateChange = stateChange
	p.volumeChange = volumeChange
	p.streams = make(map[string]string)

	p.player = &MPV{}
	playerEventChan := p.player.initialize()

	go p.run(playerEventChan)

	return &p
}

func (p *MediaPlayer) Quit() {
	p.player.quit()

	// TODO: fix race conditions
	close(p.stateChange)
	close(p.volumeChange)
}

// GetPlaystate returns the play state synchronously (may block)
func (p *MediaPlayer) GetPlaystate() *PlayState {
	p.playstateChanPing <- false
	ps := <-p.playstateChan
	return &ps
}

// changePlaystate changes the play state inside a callback
// The *PlayState argument in the callback is the PlayState that can be
// changed, but it can only be accessed inside the callback (outside of
// it, race conditions can occur).
func (p *MediaPlayer) changePlaystate(callback func(*PlayState)) {
	p.playstateChanPing <- true
	ps := <-p.playstateChan
	callback(&ps)
	p.playstateChan <- ps
}

// SetPlaystate changes the play state to the specified arguments
// This function doesn't block, but changes may not be immediately applied.
func (p *MediaPlayer) SetPlaystate(playlist []string, index int, position time.Duration) {
	go p.changePlaystate(func(ps *PlayState) {
		ps.Playlist = playlist
		ps.Index = index
		ps.Position = position

		if len(ps.Playlist) > 0 {
			p.startPlaying(ps)
		} else {
			p.Stop()
		}
	})
}

func (p *MediaPlayer) startPlaying(ps *PlayState) {
	p.setPlayState(ps, StateBuffering)

	videoId := ps.Playlist[ps.Index]
	position := ps.Position

	// do not use the playstate inside this goroutine to prevent race conditions
	go func() {
		streamUrl := p.getYouTubeStream(videoId)
		p.player.play(streamUrl, position)
	}()
}

func (p *MediaPlayer) getYouTubeStream(videoId string) string {
	if stream, ok := p.streams[videoId]; ok {
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
	p.streams[videoId] = stream
	return stream
}

func (p *MediaPlayer) setPlayState(ps *PlayState, state State) {
	if state == ps.State {
		fmt.Printf("WARNING: state %d did not change\n", state)
	}
	ps.State = state

	go func() {
		p.stateChange <- StateChange{ps.State, ps.Position}
	}()
}

func (p *MediaPlayer) UpdatePlaylist(playlist []string) {
	go p.changePlaystate(func(ps *PlayState) {

		if len(ps.Playlist) == 0 {

			if ps.State == StatePlaying {
				// just to be sure
				panic("empty playlist while playing")
			}
			ps.Playlist = playlist

			if ps.Index >= len(playlist) {
				// this appears to be the normal behavior of YouTube
				ps.Index = len(playlist) - 1
			}

			if ps.State == StateStopped {
				ps.Position = 0
				p.startPlaying(ps)
			}

		} else {
			videoId := ps.Playlist[ps.Index]
			ps.Playlist = playlist
			p.setPlaylistIndex(ps, videoId)
		}
	})
}

func (p *MediaPlayer) SetVideo(videoId string, position time.Duration) {
	go p.changePlaystate(func(ps *PlayState) {
		p.setPlaylistIndex(ps, videoId)
		ps.Position = position
		p.startPlaying(ps)
	})
}

func (p *MediaPlayer) setPlaylistIndex(ps *PlayState, videoId string) {
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

// Pause pauses the currently playing video
func (p *MediaPlayer) Pause() {
	go p.changePlaystate(func(ps *PlayState) {
		if ps.State != StatePlaying {
			fmt.Printf("Warning: pause while in state %d\n", ps.State)
		}

		p.player.pause()
	})
}

// Resume resumes playback when it was paused
func (p *MediaPlayer) Resume() {
	go p.changePlaystate(func(ps *PlayState) {
		if ps.State != StatePaused {
			fmt.Printf("Warning: resume while in state %d\n", ps.State)
		}

		p.player.resume()
	})
}

// Seek jumps to the specified position
func (p *MediaPlayer) Seek(position time.Duration) {
	go p.changePlaystate(func(ps *PlayState) {
		if ps.State != StatePaused {
			fmt.Printf("Warning: state is not paused while seeking (state: %d)\n", ps.State)
		}

		p.player.seek(position)
		p.updatePosition(ps)
	})
}

// updatePosition gets the current position from the player
func (p *MediaPlayer) updatePosition(ps *PlayState) {
	// Also give 0 position on buffering because we can't ask the player where
	// we are while we are fetching the YouTube stream.
	if ps.State == StateStopped || ps.State == StateBuffering {
		ps.Position = 0
		return
	}

	ps.Position = p.player.getPosition()
}

func (p *MediaPlayer) SetVolume(volume int) {
	p.changePlaystate(func(ps *PlayState) {
		ps.Volume = volume
		p.player.setVolume(volume)
	})
}

func (p *MediaPlayer) Stop() {
	go p.changePlaystate(func(ps *PlayState) {
		ps.Playlist = []string{}
		// Do not set ps.Index to 0, it may be needed for UpdatePlaylist:
		// Stop is called before UpdatePlaylist when removing the currently
		// playing video from the playlist.
		ps.Position = 0
		ps.State = StateStopped
		p.player.stop()
	})
}

func (p *MediaPlayer) run(playerEventChan chan playerEvent) {
	ps := PlayState{}

	ps.Volume = -1
	volumeInitialized := false

	for {
		select {
		case replace := <-p.playstateChanPing:
			p.updatePosition(&ps)
			p.playstateChan <- ps
			if replace {
				ps = <-p.playstateChan
			}

		case event := <-playerEventChan:
			switch event {
			case PLAYER_EVENT_RESTART:
				p.setPlayState(&ps, StatePlaying)

				if !volumeInitialized {
					volumeInitialized = true

					ps.Volume = p.player.getVolume()
					p.volumeChange <- ps.Volume
				}

			case PLAYER_EVENT_END:
				if ps.Index+1 < len(ps.Playlist) {
					// there are more videos, play the next
					ps.Index++
					ps.Position = 0
					p.startPlaying(&ps)
				} else {
					// signal that the video has stopped playing
					// this resets the position but keeps the playlist
					ps.Position = 0
					p.setPlayState(&ps, StateStopped)
				}

			case PLAYER_EVENT_PAUSE:
				p.setPlayState(&ps, StatePaused)

			case PLAYER_EVENT_UNPAUSE:
				p.setPlayState(&ps, StatePlaying)
			}
		}
	}
}
