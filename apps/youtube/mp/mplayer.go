// +build ignore

package mp

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"os/exec"
	"strings"
	"time"
)

type MPlayer struct {
	outputChan  chan string
	commandChan chan func(time.Duration)
	stdin       io.WriteCloser
	stdout      *bufio.Reader
	process     *exec.Cmd
}

func (mpl *MPlayer) initialize() chan State {
	mpl.process = exec.Command("mplayer", "--prefer-ipv4", "--cache=8192", "--slave", "--quiet", "--softvol", "--idle", "--input=nodefault-bindings:conf=/dev/null")

	stdin, err := mpl.process.StdinPipe()
	if err != nil {
		panic(err)
	}
	mpl.stdin = stdin

	stdout, err := mpl.process.StdoutPipe()
	if err != nil {
		panic(err)
	}
	mpl.stdout = bufio.NewReader(stdout)

	fmt.Println("Starting MPlayer...")
	err = mpl.process.Start()
	if err != nil {
		panic(err)
	}

	mpl.outputChan = make(chan string)
	mpl.commandChan = make(chan func(time.Duration))

	eventChan := make(chan State)

	go mpl.outputHandler()
	go mpl.run(eventChan)

	return eventChan
}

func (mpl *MPlayer) sendCommand(command string) {
	for _, part := range strings.Split(strings.TrimSpace(command), "\n") {
		fmt.Println("mplayer command:", part)
	}
	_, err := mpl.stdin.Write([]byte(command))
	if err != nil {
		panic(err)
	}
}

func (mpl *MPlayer) quit() {
	mpl.sendCommand("quit\n")
	time.Sleep(1) // let it close gracefully
	mpl.process.Process.Kill()
	mpl.process = nil
}

func (mpl *MPlayer) play(stream string, position time.Duration) {
	if strings.HasPrefix(stream, "https://") {
		// MPlayer2 doesn't support HTTPS, so using our built-in proxy.
		stream = "http://localhost:8008/proxy/" + stream[len("https://"):]
	}

	if position == 0 {
		mpl.sendCommand(fmt.Sprintf("stop\nloadfile \"%s\"\nget_time_length\nget_time_position\n", stream))
	} else {
		mpl.sendCommand(fmt.Sprintf("stop\nloadfile \"%s\"\nget_time_length\nseek %.3f 2\nget_time_position\n", stream, position.Seconds()))
	}
}

func (mpl *MPlayer) pause() {
	mpl.sendCommand("pause\nget_time_pos\nget_property pause\n")
}

func (mpl *MPlayer) resume() {
	mpl.sendCommand("pause\nget_property pause\n")
}

func (mpl *MPlayer) getPosition() time.Duration {
	ch := make(chan time.Duration)
	mpl.commandChan <- func(position time.Duration) {
		ch <- position
	}
	return <-ch
}

func (mpl *MPlayer) setPosition(position time.Duration) {
	mpl.sendCommand(fmt.Sprintf("seek %.3f 2\nget_time_pos\n", position.Seconds()))
}

func (mpl *MPlayer) setVolume(volume int) {
	mpl.sendCommand(fmt.Sprintf("volume %d 1\nget_time_length\n", volume))
}

func (mpl *MPlayer) stop() {
	mpl.sendCommand("stop\n")
	// TODO this doesn't send back that the video has actually stopped...
}

func (mpl *MPlayer) outputHandler() {
	for {
		line, err := mpl.stdout.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				// process exited gracefully
				close(mpl.outputChan)
				return
			}
			panic(err)
		}
		line = strings.TrimSpace(line[:len(line)-1])

		mpl.outputChan <- line
	}
}

func (mpl *MPlayer) run(eventChan chan State) {
	var startTime *time.Time
	var position time.Duration
	var length time.Duration

	for {
		select {
		case c := <-mpl.commandChan:
			if startTime != nil {
				position = time.Now().Sub(*startTime)
			}
			c(position)

		case line, ok := <-mpl.outputChan:
			if !ok {
				// channel has closed, thus process has exited, thus this
				// goroutine can/must exit.
				return
			}

			fmt.Println(time.Now().Format("15:04:05.000"), "mplayer:", line)

			if line == "Starting playback..." {
				t := time.Now().Add(-position)
				startTime = &t
				eventChan <- STATE_PLAYING

			} else if strings.HasPrefix(line, "ANS_TIME_POSITION=") {
				p, err := time.ParseDuration(line[len("ANS_TIME_POSITION="):] + "s")
				if err != nil {
					panic(err)
				}
				position = p

				if startTime != nil {
					t := time.Now().Add(-position)
					startTime = &t
				}

			} else if strings.HasPrefix(line, "ANS_LENGTH=") {
				l, err := time.ParseDuration(line[len("ANS_LENGTH="):] + "s")
				if err != nil {
					panic(err)
				}
				length = l

			} else if strings.HasPrefix(line, "ANS_pause=") {
				paused := line[len("ANS_pause="):]
				switch paused {
				case "yes":
					if startTime != nil {
						// the player just got the 'paused' signal
						position = time.Now().Sub(*startTime)
						startTime = nil
					}
					eventChan <- STATE_PAUSED
				case "no":
					if startTime == nil {
						t := time.Now().Add(-position)
						startTime = &t
					}
					eventChan <- STATE_PLAYING
				default:
					panic("unknown response: " + line)
				}

			} else if line == "" {
				// The player may have stopped...
				// This is a very dirty hack. At the end of the stream,
				// mplayer2 writes a newline to the console. But we don't know
				// for sure this newline is due to mplayer2 stopping or not,
				// so we use a few heuristics here that aren't exactly perfect.

				if startTime == nil {
					continue
				}

				// using 5s+5% as cut-off: I hope that number works.
				// That's 8s for a 60s movie and 11 seconds for 120s movie.
				// If this doesn't work always, this MPlayer wrapper is one big hack anyway...
				endOffset := startTime.Add(length).Sub(time.Now())
				if math.Abs(endOffset.Seconds()) > (5.0 + length.Seconds()*0.05) {
					continue
				}

				// assume the stream has finished playing

				startTime = nil
				position = 0
				eventChan <- STATE_STOPPED
			}
		}
	}
}
