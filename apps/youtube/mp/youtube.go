package mp

import (
	"bufio"
	"io"
	"log"
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
    from youtube_dl import YoutubeDL
    from youtube_dl.utils import DownloadError

    if len(sys.argv) != 2:
        sys.stderr.write('provide one argument with the format string')
        os.exit(1)

    yt = YoutubeDL({
        'geturl': True,
        'format': sys.argv[1],
        'quiet': True,
        'simulate': True})

    sys.stderr.write('YouTube-DL started.\n')

    while True:
        stream = ''
        try:
            url = raw_input()
            stream = yt.extract_info(url, ie_key='Youtube')['url']
        except (KeyboardInterrupt, EOFError, IOError):
            break
        except DownloadError, why:
            # error message has already been printed
            sys.stderr.write('Could not extract video, try updating youtube-dl.\n')
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

	// Start the process in a separate goroutine.
	vg.cmdMutex.Lock()
	go func() {
		defer vg.cmdMutex.Unlock()

		vg.cmd = exec.Command("python", "-c", pythonGrabber, grabberFormats)
		stdout, err := vg.cmd.StdoutPipe()
		if err != nil {
			log.Fatal(err)
		}
		vg.cmdStdout = bufio.NewReader(stdout)
		vg.cmdStdin, err = vg.cmd.StdinPipe()
		if err != nil {
			log.Fatal(err)
		}
		vg.cmd.Stderr = os.Stderr
		err = vg.cmd.Start()
		if err != nil {
			log.Fatal("Could not start video stream grabber:", err)
		}

	}()

	return &vg
}

func (vg *VideoGrabber) Quit() {
	vg.cmdMutex.Lock()
	defer vg.cmdMutex.Unlock()

	err := vg.cmd.Process.Signal(os.Interrupt)
	if err != nil {
		log.Fatal(err)
	}

	// Wait until exit, and free resources
	err = vg.cmd.Wait()
	if err != nil {
		if _, ok := err.(*exec.ExitError); !ok {
			log.Fatal(err)
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
			log.Println("Stream has expired for ID:", videoId)
		}
	}

	videoURL := "https://www.youtube.com/watch?v=" + videoId
	log.Println("Fetching video stream for URL", videoURL)

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
			log.Fatal("could not grab video:", err)
		}

		stream.url = line[:len(line)-1]
		stream.fetchMutex.Unlock()

		log.Println("Got stream for", videoURL)

		expires, err := getExpiresFromURL(stream.url)
		if err != nil {
			log.Println("WARNING: failed to extract expires from video URL:", err)
		} else if expires.Before(stream.expires) {
			log.Println("WARNING: URL expires before the estimated expires!")
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
