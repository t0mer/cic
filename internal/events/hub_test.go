package events

import (
	"sync"
	"testing"
)

func TestFanOut(t *testing.T) {
	h := NewHub()
	a, ca := h.Subscribe()
	b, cb := h.Subscribe()
	defer ca()
	defer cb()
	h.Publish(TypeBrokerState, "x")
	for _, ch := range []<-chan Event{a, b} {
		ev := <-ch
		if ev.Type != TypeBrokerState || ev.ID != 1 || ev.Data != "x" {
			t.Fatalf("got %+v", ev)
		}
	}
}

func TestSlowSubscriberDropped(t *testing.T) {
	h := NewHub()
	slow, _ := h.Subscribe()
	fast, cancel := h.Subscribe()
	defer cancel()
	for i := 0; i < subscriberBuffer+1; i++ {
		h.Publish(TypeDeviceUpdated, i)
		select {
		case <-fast:
		default:
		}
	}
	if h.Dropped() != 1 {
		t.Fatalf("dropped = %d, want 1", h.Dropped())
	}
	n := 0
	for range slow {
		n++
	}
	if n != subscriberBuffer {
		t.Fatalf("slow got %d buffered events, want %d", n, subscriberBuffer)
	}
	h.Publish(TypeDeviceUpdated, "after")
	if ev := <-fast; ev.Data != "after" {
		t.Fatalf("fast subscriber lost event: %+v", ev)
	}
}

func TestCancelIdempotent(t *testing.T) {
	h := NewHub()
	ch, cancel := h.Subscribe()
	cancel()
	cancel()
	if _, ok := <-ch; ok {
		t.Fatal("channel not closed")
	}
	h.Publish("x", nil)
}

func TestCloseAndSubscribeAfterClose(t *testing.T) {
	h := NewHub()
	ch, cancel := h.Subscribe()
	h.Close()
	h.Close()
	if _, ok := <-ch; ok {
		t.Fatal("channel not closed")
	}
	cancel()
	select {
	case <-h.Done():
	default:
		t.Fatal("Done not closed")
	}
	late, c := h.Subscribe()
	c()
	if _, ok := <-late; ok {
		t.Fatal("late channel open")
	}
	h.Publish("x", nil)
}

func TestConcurrent(t *testing.T) {
	h := NewHub()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			ch, cancel := h.Subscribe()
			defer cancel()
			for j := 0; j < 20; j++ {
				select {
				case <-ch:
				default:
				}
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				h.Publish("x", j)
			}
		}()
	}
	wg.Wait()
	h.Close()
}

func TestSubscribersAndFirstSubscriber(t *testing.T) {
	h := NewHub()
	fired := make(chan struct{}, 4)
	h.OnFirstSubscriber(func() { fired <- struct{}{} })
	if h.Subscribers() != 0 {
		t.Fatal("want 0")
	}
	_, c1 := h.Subscribe()
	<-fired
	_, c2 := h.Subscribe() // 1 -> 2: no callback
	if h.Subscribers() != 2 {
		t.Fatalf("subscribers %d", h.Subscribers())
	}
	c1()
	c1() // idempotent
	c2()
	if h.Subscribers() != 0 {
		t.Fatalf("subscribers %d", h.Subscribers())
	}
	_, c3 := h.Subscribe() // 0 -> 1 again
	defer c3()
	<-fired
	select {
	case <-fired:
		t.Fatal("callback fired more than once per 0->1")
	default:
	}
}

func TestOnFirstSubscriberDoesNotBlock(t *testing.T) {
	h := NewHub()
	block := make(chan struct{})
	h.OnFirstSubscriber(func() { <-block })
	defer close(block)
	_, c := h.Subscribe() // must return although the callback blocks
	defer c()
	h.Publish(TypeBrokerState, 1)
}

func TestFirstSubscriberRace(t *testing.T) {
	h := NewHub()
	h.OnFirstSubscriber(func() {})
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, c := h.Subscribe()
			h.Publish(TypeBrokerState, 1)
			_ = h.Subscribers()
			c()
		}()
	}
	wg.Wait()
}
