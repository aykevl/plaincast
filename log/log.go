package log

import (
	"flag"
	"fmt"
	"os"
	"time"

	"golang.org/x/crypto/ssh/terminal"
)

var loggers = make(map[string]*Logger)

type Logger struct {
	name    string
	enabled bool
}

const TIME_FORMAT = "15:04:05.000"

const (
	LOGLEVEL_INFO = iota + 1
	LOGLEVEL_WARN
	LOGLEVEL_ERR
)

var isTerminal = terminal.IsTerminal(int(os.Stdout.Fd()))

var flagLoglevel = flag.String("loglevel", "warn", "Baseline loglevel (info, warn, err)")

var loglevel = 0

func getLoglevel() int {
	if !flag.Parsed() {
		panic("log called before flag.Parse()")
	}

	if loglevel == 0 {
		switch *flagLoglevel {
		case "info", "i":
			loglevel = LOGLEVEL_INFO
		case "warn", "warning", "w":
			loglevel = LOGLEVEL_WARN
		case "err", "error", "e":
			loglevel = LOGLEVEL_ERR
		default:
			fmt.Println("Error in parsing 'loglevel' flag: unknown value")
			os.Exit(1)
		}
	}

	return loglevel
}

// New creates a new logger that can be enabled or disabled via program flags.
// Loggers must be created before flags are parsed.
func New(name string, description string) *Logger {
	if _, ok := loggers[name]; ok {
		panic("redefining logger")
	}

	if flag.Parsed() {
		panic("flags were already parsed")
	}

	l := &Logger{
		name:    name,
		enabled: false, // value does not matter
	}
	flag.BoolVar(&l.enabled, "log-"+name, false, description)
	loggers[name] = l
	return l
}

func (l *Logger) write(s string, loglevel int) {
	if loglevel < getLoglevel() && !l.enabled {
		return
	}

	s = fmt.Sprintf("[%s] %s", l.name, s)

	if isTerminal {
		switch loglevel {
		case LOGLEVEL_INFO:
			// don't color output
		case LOGLEVEL_WARN:
			// yellow
			s = fmt.Sprintf("\x1b[33m%s\x1b[0m", s)
		case LOGLEVEL_ERR:
			// light red
			s = fmt.Sprintf("\x1b[91m%s\x1b[0m", s)
		default:
			// must not happen
			panic("unknown loglevel")
		}
	}

	s = fmt.Sprintf("%s %s", time.Now().Format(TIME_FORMAT), s)

	fmt.Print(s)
}

func (l *Logger) Printf(format string, v ...interface{}) {
	l.write(fmt.Sprintf(format, v...), LOGLEVEL_INFO)
}

func (l *Logger) Println(v ...interface{}) {
	l.write(fmt.Sprintln(v...), LOGLEVEL_INFO)
}

func (l *Logger) Warnf(format string, v ...interface{}) {
	l.write(fmt.Sprintf(format, v...), LOGLEVEL_WARN)
}

func (l *Logger) Warnln(v ...interface{}) {
	l.write(fmt.Sprintln(v...), LOGLEVEL_WARN)
}

func (l *Logger) Errf(format string, v ...interface{}) {
	l.write(fmt.Sprintf(format, v...), LOGLEVEL_ERR)
}

func (l *Logger) Errln(v ...interface{}) {
	l.write(fmt.Sprintln(v...), LOGLEVEL_ERR)
}

func (l *Logger) Fatal(v ...interface{}) {
	l.write(fmt.Sprint(v...), LOGLEVEL_ERR)
	os.Exit(1)
}

func (l *Logger) Fatalf(format string, v ...interface{}) {
	l.write(fmt.Sprintf(format, v...), LOGLEVEL_ERR)
	os.Exit(1)
}

func (l *Logger) Fatalln(v ...interface{}) {
	l.write(fmt.Sprintln(v...), LOGLEVEL_ERR)
	os.Exit(1)
}

func (l *Logger) Panic(v ...interface{}) {
	s := fmt.Sprint(v...)
	l.write(s, LOGLEVEL_ERR)
	panic(s)
}
