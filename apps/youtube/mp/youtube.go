package mp

import (
	"bytes"
	"log"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

type VideoGrabber struct {
	streams      map[string]*VideoUrl // map of video ID to stream gotten from youtube-dl
	streamsMutex sync.Mutex
	cmd          *exec.Cmd
	cmdMutex     sync.Mutex
	fetchMutex   sync.Mutex
}

func NewVideoGrabber() *VideoGrabber {
	vg := VideoGrabber{}
	vg.streams = make(map[string]*VideoUrl)
	return &vg
}

// GetStream returns the stream for videoId, or an empty string if an error
// occured.
func (vg *VideoGrabber) GetStream(videoId string) string {
	return vg.getStream(videoId, true)
}

func (vg *VideoGrabber) getStream(videoId string, killCurrent bool) string {
	vg.streamsMutex.Lock()

	stream, ok := vg.streams[videoId]
	if ok {
		vg.streamsMutex.Unlock()

		if !stream.WillExpire() {

			return stream.GetUrl()
		} else {
			log.Println("Stream has expired for ID:", videoId)
		}

		vg.streamsMutex.Lock()
	}

	videoUrl := "https://www.youtube.com/watch?v=" + videoId
	log.Println("Fetching video stream for URL", videoUrl)

	stream = &VideoUrl{videoId: videoId}
	stream.fetchMutex.Lock()

	go func() {
		defer stream.fetchMutex.Unlock()

		vg.cmdMutex.Lock()
		defer vg.cmdMutex.Unlock()

		if killCurrent {
			if vg.cmd != nil {
				err := vg.cmd.Process.Signal(os.Interrupt)
				if err != nil {
					log.Println("ERROR: could not stop command:", err)
				} else {
					log.Println("Interrupted video grabber")
				}

				vg.cmd = nil
			}
		} else {
			vg.fetchMutex.Lock()
			defer vg.fetchMutex.Unlock()
		}

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
		cmd := exec.Command("youtube-dl", "-g", "-f", "171/172/43/22/18", videoUrl)
		var buf bytes.Buffer
		cmd.Stdout = &buf
		cmd.Stderr = os.Stderr

		vg.cmd = cmd

		err := cmd.Start()
		if err != nil {
			log.Println("Failed to start video stream fetcher:", err)
			return
		}

		vg.cmdMutex.Unlock()
		err = cmd.Wait()
		if err != nil {
			log.Printf("Failed to fetch video %s: %s", videoUrl, err)
			vg.cmdMutex.Lock()
			vg.cmd = nil
			return
		}
		vg.cmdMutex.Lock()

		vg.cmd = nil

		log.Println("Got stream for", videoUrl)

		if cmd != vg.cmd {
			// this should not happen
			panic("vg.cmd has changed")
		}

		stream.url = strings.TrimSpace(string(buf.Bytes()))

		stream.expires, err = getExpiresFromUrl(stream.url)
		if err != nil {
			log.Println("WARNING: failed to extract expires from video URL:", err)
		}
	}()

	vg.streams[videoId] = stream

	vg.streamsMutex.Unlock()

	return stream.GetUrl()
}

// CacheStream will start fetching the stream in the background.
func (vg VideoGrabber) CacheStream(videoId string) {
	go vg.getStream(videoId, false)
}

type VideoUrl struct {
	videoId    string
	fetchMutex sync.RWMutex
	url        string
	expires    time.Time
}

func getExpiresFromUrl(videoUrl string) (time.Time, error) {
	u, err := url.Parse(videoUrl)
	if err != nil {
		return time.Time{}, err
	}

	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return time.Time{}, err
	}

	seconds, err := strconv.ParseInt(query.Get("expire"), 10, 64)
	if err != nil {
		return time.Time{}, err
	}

	return time.Unix(seconds, 0), nil
}

// WillExpire returns true if this stream will expire within an hour.
// This function may block until the video has been fetched or an error occurs.
func (u *VideoUrl) WillExpire() bool {
	u.fetchMutex.RLock()
	defer u.fetchMutex.RUnlock()

	return !u.expires.IsZero() && u.expires.Before(time.Now().Add(time.Hour))
}

// Gets the video stream URL, possibly waiting until that video has been fetched
// or an error occurs. An empty string will be returned on error.
func (u *VideoUrl) GetUrl() string {
	u.fetchMutex.RLock()
	defer u.fetchMutex.RUnlock()

	return u.url
}

func (u *VideoUrl) String() string {
	return "<VideoUrl " + u.videoId + ">"
}
