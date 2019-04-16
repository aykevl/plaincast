package apps

type App interface {
	Start(string) // start or provide extra data
	Running() bool
	Quit()
	FriendlyName() string // return a human-readable name
	Data(string) string // return data from app
}
