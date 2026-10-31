package mqtt

import (
	"sync"
	"time"
)

// connTracker serialises state writes from asynchronous connect and
// connection-lost callbacks. Each connect attempt gets a generation; a write
// from a superseded generation is ignored.
type connTracker struct {
	mu    sync.Mutex
	gen   uint64
	state State
}

func newConnTracker() *connTracker {
	return &connTracker{state: State{Since: time.Now()}}
}

// begin starts a new generation, invalidating all earlier ones.
func (t *connTracker) begin() uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.gen++
	return t.gen
}

// connected marks the connection up if gen is still current.
func (t *connTracker) connected(gen uint64) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if gen != t.gen {
		return false
	}
	t.state = State{Connected: true, Since: time.Now()}
	return true
}

// fail records an error and marks the connection down. With gen == 0 it applies
// to the current generation unconditionally; otherwise only if gen is current.
func (t *connTracker) fail(gen uint64, msg string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if gen != 0 && gen != t.gen {
		return false
	}
	t.state = State{LastError: msg, Since: time.Now()}
	return true
}

// lost invalidates the current generation and marks the connection down.
func (t *connTracker) lost(msg string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.gen++
	t.state = State{LastError: msg, Since: time.Now()}
}

func (t *connTracker) snapshot() State {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.state
}
