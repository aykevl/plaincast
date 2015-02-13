package youtube

import (
	"math/rand"
	"sync"
)

// RandomID implements the 'rid' field for the Channel API.
type RandomID struct {
	mutex  sync.Mutex
	number int
}

// NewRandomID returns a new initialized RID counter.
func NewRandomID() *RandomID {
	rid := &RandomID{}
	rid.Restart()
	return rid
}

// Restart resets the counter to a new random value (as if a new RID object was
// created).
func (rid *RandomID) Restart() {
	rid.mutex.Lock()
	defer rid.mutex.Unlock()

	// this appears to be a random number between 10000-99999
	rid.number = rand.Intn(80000) + 10000
}

// Next returns the next RID, incrementing the humber by one.
func (rid *RandomID) Next() int {
	rid.mutex.Lock()
	defer rid.mutex.Unlock()

	rid.number++
	return rid.number
}
