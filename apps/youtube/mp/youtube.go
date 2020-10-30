package mp

import (
	"bufio"
	"io"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"
)

const pythonGrabber = `
try:
    import sys
    from pytube import YouTube

    while True:
        stream = ''
        try:
            url = sys.stdin.readline().strip()
            stream = YouTube(str(url)).streams.first().url
        except (KeyboardInterrupt, EOFError, IOError):
            break
        except:
            sys.stderr.write('Could not extract video\n')
        finally:
            try:
                sys.stdout.write(stream + '\n')
                sys.stdout.flush()
            except:
                pass

except (KeyboardInterrupt, EOFError, IOError):
    pass
`

// First (mkv-container) audio only with 100+kbps, then video with audio
// bitrate 100+ (where video has the lowest possible quality), then
// slightly lower quality audio.
// We do this because for some reason DASH aac audio (in the MP4 container)
// doesn't support seeking in any of the tested players (mpv using
// libavformat, and vlc, gstreamer and mplayer2 using their own demuxers).
// But the MKV container seems to have much better support.
// See:
//   https://github.com/mpv-player/mpv/issues/579
//   https://trac.ffmpeg.org/ticket/3842
const grabberFormats = "171/172/43/22/18"

type VideoGrabber struct {
	streams      map[string]*VideoURL // map of video ID to stream gotten from youtube-dl
	streamsMutex sync.Mutex
	cmd          *exec.Cmd
	cmdMutex     sync.Mutex
	cmdStdin     io.Writer
	cmdStdout    *bufio.Reader
}

func NewVideoGrabber() *VideoGrabber {
	vg := VideoGrabber{}
	vg.streams = make(map[string]*VideoURL)

	//cacheDir := *cacheDir
	//if cacheDir != "" {
	//	cacheDir = cacheDir + "/" + "youtube-dl"
	//}

	// Start the process in a separate goroutine.
	vg.cmdMutex.Lock()
	go func() {
		defer vg.cmdMutex.Unlock()

		vg.cmd = exec.Command("python3", "-c", pythonGrabber)//, grabberFormats, cacheDir)
		stdout, err := vg.cmd.StdoutPipe()
		if err != nil {
			logger.Fatal(err)
		}
		vg.cmdStdout = bufio.NewReader(stdout)
		vg.cmdStdin, err = vg.cmd.StdinPipe()
		if err != nil {
			logger.Fatal(err)
		}
		vg.cmd.Stderr = os.Stderr
		err = vg.cmd.Start()
		if err != nil {
			logger.Fatal("Could not start video stream grabber:", err)
		}

	}()

	return &vg
}

func (vg *VideoGrabber) Quit() {
	vg.cmdMutex.Lock()
	defer vg.cmdMutex.Unlock()

	err := vg.cmd.Process.Signal(os.Interrupt)
	if err != nil {
		logger.Fatal("could not send SIGINT:", err)
	}

	// Wait until exit, and free resources
	err = vg.cmd.Wait()
	if err != nil {
		if _, ok := err.(*exec.ExitError); !ok {
			logger.Fatal("process could not be stopped:", err)
		}
	}
}

// GetStream returns the stream for videoId, or an empty string if an error
// occured.
func (vg *VideoGrabber) GetStream(videoId string) string {
	return vg.getStream(videoId).GetURL()
}

func (vg *VideoGrabber) getStream(videoId string) *VideoURL {
	vg.streamsMutex.Lock()
	defer vg.streamsMutex.Unlock()

	if videoId == "" {
		panic("empty video ID")
	}

	stream, ok := vg.streams[videoId]
	if ok {
		if !stream.WillExpire() {
			return stream
		} else {
			logger.Println("Stream has expired for ID:", videoId)
		}
	}

	videoURL := "https://www.youtube.com/watch?v=" + videoId
	logger.Println("Fetching video stream for URL", videoURL)

	// Streams normally expire in 6 hour, give it a margin of one hour.
	stream = &VideoURL{videoId: videoId, expires: time.Now().Add(5 * time.Hour)}
	stream.fetchMutex.Lock()

	vg.streams[videoId] = stream

	go func() {
		vg.cmdMutex.Lock()
		defer vg.cmdMutex.Unlock()

		io.WriteString(vg.cmdStdin, videoURL+"\n")
		line, err := vg.cmdStdout.ReadString('\n')
		if err != nil {
			logger.Fatal("could not grab video:", err)
		}

		stream.url = line[:len(line)-1]
		stream.fetchMutex.Unlock()

		logger.Println("Got stream for", videoURL)

		expires, err := getExpiresFromURL(stream.url)
		if err != nil {
			logger.Warnln("failed to extract expires from video URL:", err)
		} else if expires.Before(stream.expires) {
			logger.Warnln("URL expires before the estimated expires!")
		}
	}()

	return stream
}

type VideoURL struct {
	videoId    string
	fetchMutex sync.RWMutex
	url        string
	expires    time.Time
}

func getExpiresFromURL(videoURL string) (time.Time, error) {
	u, err := url.Parse(videoURL)
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
func (u *VideoURL) WillExpire() bool {
	return !u.expires.IsZero() && u.expires.Before(time.Now().Add(time.Hour))
}

// Gets the video stream URL, possibly waiting until that video has been fetched
// or an error occurs. An empty string will be returned on error.
func (u *VideoURL) GetURL() string {
	u.fetchMutex.RLock()
	defer u.fetchMutex.RUnlock()

	return u.url
}

func (u *VideoURL) String() string {
	return "<VideoURL " + u.videoId + ">"
}
