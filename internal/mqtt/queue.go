package mqtt

import (
	"context"
	"sync"
)

// eventQueue is a bounded FIFO. When full it drops the oldest telemetry event;
// status and version events are never dropped, so the queue may exceed its
// capacity while it holds nothing but those.
type eventQueue struct {
	mu    sync.Mutex
	items []Event
	limit int
	wake  chan struct{}
}

func newEventQueue(limit int) *eventQueue {
	return &eventQueue{limit: limit, wake: make(chan struct{}, 1)}
}

func isPriority(ev Event) bool {
	return ev.Leaf == LeafStatus || ev.Leaf == LeafVersion
}

// push appends ev and reports whether a telemetry event was dropped to make room.
func (q *eventQueue) push(ev Event) (dropped bool) {
	q.mu.Lock()
	if len(q.items) >= q.limit {
		victim := -1
		for i, it := range q.items {
			if !isPriority(it) {
				victim = i
				break
			}
		}
		switch {
		case victim >= 0:
			q.items = append(q.items[:victim], q.items[victim+1:]...)
			dropped = true
		case !isPriority(ev):
			q.mu.Unlock()
			return true
		}
	}
	q.items = append(q.items, ev)
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
	return dropped
}

func (q *eventQueue) pop() (Event, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) == 0 {
		return Event{}, false
	}
	ev := q.items[0]
	q.items[0] = Event{}
	q.items = q.items[1:]
	return ev, true
}

// next blocks until an event is available or ctx is done.
func (q *eventQueue) next(ctx context.Context) (Event, bool) {
	for {
		if ev, ok := q.pop(); ok {
			return ev, true
		}
		select {
		case <-ctx.Done():
			return Event{}, false
		case <-q.wake:
		}
	}
}
