package apps

type App interface {
	Start(string)
	Running() bool
	Quit()
}
