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
	go func() {
		p.player.quit()
	}()

	// TODO: fix race conditions
	close(p.stateChange)
	close(p.volumeChange)
}

func (p *MediaPlayer) GetPosition() time.Duration {
	ps := p.GetPlaystate()

	var position time.Duration
	if ps.State == STATE_STOPPED {
		position = 0
	} else if ps.State == STATE_BUFFERING {
		position = ps.bufferingPosition
	} else {
		position = p.player.getPosition()
	}

	if position < 0 {
		panic("got position < 0")
	}

	return position
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
		if ps.State == STATE_BUFFERING && ps.bufferingPosition == position && playlist[index] == ps.Playlist[ps.Index] {
			// just in case something else has changed, update the playlist
			p.updatePlaylist(ps, playlist)
			return
		}
		ps.Playlist = playlist
		ps.Index = index

		if len(ps.Playlist) > 0 {
			p.startPlaying(ps, position)
		} else {
			p.Stop()
		}
	})
}

func (p *MediaPlayer) startPlaying(ps *PlayState, position time.Duration) {
	p.setPlayState(ps, STATE_BUFFERING, position)

	videoId := ps.Playlist[ps.Index]

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

	youtubeUrl := "https://www.youtube.com/watch?v=" + videoId

	fmt.Println("Fetching YouTube stream...", youtubeUrl)
	// First (mkv-container) audio only, then video with audio bitrate 100+
	// (where video has the lowest possible quality), then slightly lower
	// quality audio.
	// We do this because for some reason DASH aac audio (in the MP4 container)
	// doesn't support seeking in any of the tested players (mpv using
	// libavformat, and vlc, gstreamer and mplayer2 using their own demuxers).
	// But the MKV container seems to have much better support.
	// See:
	//   https://github.com/mpv-player/mpv/issues/579
	//   https://trac.ffmpeg.org/ticket/3842
	cmd := exec.Command("youtube-dl", "-g", "-f", "171/172/43/22/18", youtubeUrl)
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

// setPlayState sets updates the PlayState and sends events.
// position may be -1: in that case it will be updated.
func (p *MediaPlayer) setPlayState(ps *PlayState, state State, position time.Duration) {
	if state == ps.State {
		fmt.Printf("WARNING: state %d did not change\n", state)
	}
	ps.State = state

	if state == STATE_BUFFERING {
		ps.bufferingPosition = position
	} else {
		ps.bufferingPosition = -1
	}

	go func() {
		if position == -1 {
			position = p.GetPosition()
		}

		p.stateChange <- StateChange{state, position}
	}()
}

func (p *MediaPlayer) UpdatePlaylist(playlist []string) {
	go p.changePlaystate(func(ps *PlayState) {
		p.updatePlaylist(ps, playlist)
	})
}

func (p *MediaPlayer) updatePlaylist(ps *PlayState, playlist []string) {
	if len(ps.Playlist) == 0 {

		if ps.State == STATE_PLAYING {
			// just to be sure
			panic("empty playlist while playing")
		}
		ps.Playlist = playlist

		if ps.Index >= len(playlist) {
			// this appears to be the normal behavior of YouTube
			ps.Index = len(playlist) - 1
		}

		if ps.State == STATE_STOPPED {
			p.startPlaying(ps, 0)
		}

	} else {
		videoId := ps.Playlist[ps.Index]
		ps.Playlist = playlist
		p.setPlaylistIndex(ps, videoId)
	}
}

func (p *MediaPlayer) SetVideo(videoId string, position time.Duration) {
	go p.changePlaystate(func(ps *PlayState) {
		p.setPlaylistIndex(ps, videoId)
		p.startPlaying(ps, position)
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
		if ps.State != STATE_PLAYING {
			fmt.Printf("Warning: pause while in state %d\n", ps.State)
		}

		p.player.pause()
	})
}

// Resume resumes playback when it was paused
func (p *MediaPlayer) Resume() {
	go p.changePlaystate(func(ps *PlayState) {
		if ps.State != STATE_PAUSED {
			fmt.Printf("Warning: resume while in state %d\n", ps.State)
		}

		p.player.resume()
	})
}

// Seek jumps to the specified position
func (p *MediaPlayer) Seek(position time.Duration) {
	go p.changePlaystate(func(ps *PlayState) {
		if ps.State != STATE_PAUSED {
			fmt.Printf("Warning: state is not paused while seeking (state: %d)\n", ps.State)
		}

		p.player.setPosition(position)
	})
}

func (p *MediaPlayer) SetVolume(volume int) {
	go p.changePlaystate(func(ps *PlayState) {
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
		p.player.stop()
	})
}

func (p *MediaPlayer) run(playerEventChan chan State) {
	ps := PlayState{}

	ps.Volume = -1

	for {
		select {
		case replace := <-p.playstateChanPing:
			p.playstateChan <- ps
			if replace {
				ps = <-p.playstateChan
			}

		case event, ok := <-playerEventChan:
			if !ok {
				// player has quit, and closed channel
				return
			}

			if event == ps.State {
				// status hasn't changed
				// especially libvlc may send multiple events, ignore those
				continue
			}

			switch event {
			case STATE_PLAYING:
				p.setPlayState(&ps, STATE_PLAYING, -1)

				if ps.Volume == -1 {
					ps.Volume = INITIAL_VOLUME
					p.player.setVolume(ps.Volume)
					p.volumeChange <- ps.Volume
				}

			case STATE_PAUSED:
				p.setPlayState(&ps, STATE_PAUSED, -1)

			case STATE_STOPPED:
				if ps.State == STATE_BUFFERING {
					// Especially VLC may keep sending 'stopped' events
					// while the next track is already buffering.
					// Ignore those events.
					continue
				}
				if ps.Index+1 < len(ps.Playlist) {
					// there are more videos, play the next
					ps.Index++
					p.startPlaying(&ps, 0)
				} else {
					// signal that the video has stopped playing
					// this resets the position but keeps the playlist
					p.setPlayState(&ps, STATE_STOPPED, 0)
				}
			}
		}
	}
}
