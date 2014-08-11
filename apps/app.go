package apps

type App interface {
	Start(string)
	Running() bool
	Stop()
}
