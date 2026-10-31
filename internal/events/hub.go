// Package events provides a non-blocking in-process publish/subscribe hub.
package events

import (
	"sync"
	"sync/atomic"
)

// Event types published on the hub.
const (
	TypeDeviceUpdated = "device.updated"
	TypeDeviceRemoved = "device.removed"
	TypeBrokerState   = "broker.state"
	TypeCommandResult = "command.result"
)

// subscriberBuffer is the per-subscriber channel capacity.
const subscriberBuffer = 64

// Event is a message delivered to subscribers. Data must be JSON-serializable.
type Event struct {
	ID   uint64 `json:"id"`
	Type string `json:"type"`
	Data any    `json:"data"`
}

// Hub fans events out to subscribers. A subscriber that cannot keep up is
// dropped (its channel is closed) so Publish never blocks.
type Hub struct {
	mu      sync.Mutex
	subs    map[uint64]chan Event
	nextSub uint64
	nextID  uint64
	closed  bool
	done    chan struct{}
	dropped atomic.Uint64
}

// NewHub returns an empty Hub.
func NewHub() *Hub {
	return &Hub{subs: map[uint64]chan Event{}, done: make(chan struct{})}
}

// Subscribe registers a subscriber. The channel is closed when the subscriber
// is dropped for being slow, when the hub is closed, or when cancel is called.
// cancel is idempotent.
func (h *Hub) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, subscriberBuffer)
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		close(ch)
		return ch, func() {}
	}
	h.nextSub++
	id := h.nextSub
	h.subs[id] = ch
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if c, ok := h.subs[id]; ok {
			delete(h.subs, id)
			close(c)
		}
	}
}

// Publish assigns the next ID and delivers the event to every subscriber
// without blocking; subscribers with a full buffer are dropped.
func (h *Hub) Publish(typ string, data any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.nextID++
	ev := Event{ID: h.nextID, Type: typ, Data: data}
	for id, ch := range h.subs {
		select {
		case ch <- ev:
		default:
			delete(h.subs, id)
			close(ch)
			h.dropped.Add(1)
		}
	}
}

// Dropped returns how many slow subscribers have been dropped.
func (h *Hub) Dropped() uint64 { return h.dropped.Load() }

// Done is closed when the hub is closed.
func (h *Hub) Done() <-chan struct{} { return h.done }

// Close closes all subscriber channels and rejects further subscriptions.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	close(h.done)
	for id, ch := range h.subs {
		delete(h.subs, id)
		close(ch)
	}
}
