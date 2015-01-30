package apps

type App interface {
	Start(string) // start or provide extra data
	Running() bool
	Quit()
}
