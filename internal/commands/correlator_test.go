package commands

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/t0mer/cic/internal/mqtt"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newClock() *clock { return &clock{t: time.Date(2026, 11, 1, 23, 59, 0, 0, time.UTC)} }

func smsEvent(device, phone string, ok bool, errMsg string) mqtt.Event {
	return mqtt.Event{Prefix: "dradis", Device: device, Leaf: mqtt.LeafSMSResult,
		Payload: mqtt.SMSResult{Phone: phone, OK: ok, Error: errMsg}}
}

func TestCorrelator(t *testing.T) {
	clk := newClock()
	var got []Outcome
	c := NewCorrelator(func(o Outcome) { got = append(got, o) }, clk.Now)

	c.Record("id1", "dradis/p1", "+972501111111")
	c.Record("id2", "dradis/p1", "+972501111111")
	c.Record("id3", "dradis/p2", "+972502222222")

	// Oldest pending for the same device+phone resolves first.
	c.Observe(smsEvent("p1", "+972 50-111-1111", true, ""))
	// A different device never matches p1's pending entry.
	c.Observe(smsEvent("p2", "+972501111111", true, ""))
	// Failure carries the trimmed error.
	c.Observe(smsEvent("p2", "+972502222222", false, strings.Repeat("x", 300)))
	// Unrelated events are ignored.
	c.Observe(mqtt.Event{Prefix: "dradis", Device: "p1", Leaf: mqtt.LeafStatus, Payload: mqtt.SMSResult{OK: true}})

	if len(got) != 2 {
		t.Fatalf("outcomes = %+v", got)
	}
	if got[0].CommandID != "id1" || got[0].Status != OutcomeConfirmed {
		t.Errorf("first = %+v", got[0])
	}
	if got[1].CommandID != "id3" || got[1].Status != OutcomeFailed || len(got[1].Error) != 200 {
		t.Errorf("second = %+v", got[1])
	}

	// id2 expires after 60 s.
	clk.Add(30 * time.Second)
	c.Sweep(clk.Now())
	if len(got) != 2 {
		t.Fatalf("swept too early: %+v", got)
	}
	clk.Add(31 * time.Second)
	c.Sweep(clk.Now())
	if len(got) != 3 || got[2].CommandID != "id2" || got[2].Status != OutcomeNoConfirmation {
		t.Fatalf("after sweep: %+v", got)
	}
	c.Sweep(clk.Now())
	if len(got) != 3 {
		t.Errorf("duplicate sweep outcome: %+v", got)
	}
}

func TestCorrelatorLateResultIgnored(t *testing.T) {
	clk := newClock()
	var got []Outcome
	c := NewCorrelator(func(o Outcome) { got = append(got, o) }, clk.Now)
	c.Record("id1", "dradis/p1", "+972501111111")
	clk.Add(61 * time.Second)
	c.Observe(smsEvent("p1", "+972501111111", true, ""))
	if len(got) != 0 {
		t.Fatalf("late result matched: %+v", got)
	}
	c.Sweep(clk.Now())
	if len(got) != 1 || got[0].Status != OutcomeNoConfirmation {
		t.Fatalf("got %+v", got)
	}
}

func TestCorrelatorBoundedPending(t *testing.T) {
	clk := newClock()
	var got []Outcome
	c := NewCorrelator(func(o Outcome) { got = append(got, o) }, clk.Now)
	for i := 0; i < maxPending+3; i++ {
		c.Record(string(rune('a'+i%26))+time.Duration(i).String(), "dradis/p1", "+972501111111")
	}
	if len(c.pending) != maxPending {
		t.Fatalf("pending = %d", len(c.pending))
	}
	if len(got) != 0 { // evicted entries were still inside the window: never confirmable, not reported
		t.Fatalf("evicted = %+v", got)
	}
}

func TestCorrelatorPhoneCanonicalization(t *testing.T) {
	for _, echoed := range []string{"972501111111", "+972501111111", "+972 50-111-1111", "(972) 50.111.1111", "+972 50‑111‑1111"} {
		clk := newClock()
		var got []Outcome
		c := NewCorrelator(func(o Outcome) { got = append(got, o) }, clk.Now)
		c.Record("id1", "dradis/p1", "+972501111111")
		c.Observe(smsEvent("p1", echoed, true, ""))
		if len(got) != 1 || got[0].Status != OutcomeConfirmed {
			t.Errorf("echoed %q: got %+v", echoed, got)
		}
	}
	// A local-format number cannot be matched to an international one.
	clk := newClock()
	var got []Outcome
	c := NewCorrelator(func(o Outcome) { got = append(got, o) }, clk.Now)
	c.Record("id1", "dradis/p1", "+972501111111")
	c.Observe(smsEvent("p1", "0501111111", true, ""))
	if len(got) != 0 || len(c.pending) != 1 {
		t.Fatalf("local format matched: %+v", got)
	}
	clk.Add(61 * time.Second)
	c.Sweep(clk.Now())
	if len(got) != 1 || got[0].Status != OutcomeNoConfirmation {
		t.Errorf("got %+v", got)
	}
}

func TestCorrelatorEmptyPhoneNeedsExactlyOnePending(t *testing.T) {
	clk := newClock()
	var got []Outcome
	c := NewCorrelator(func(o Outcome) { got = append(got, o) }, clk.Now)
	c.Record("id1", "dradis/p1", "+972501111111")
	c.Record("id2", "dradis/p1", "+972502222222")
	c.Observe(smsEvent("p1", "", true, ""))
	if len(got) != 0 || len(c.pending) != 2 {
		t.Fatalf("ambiguous result resolved: %+v", got)
	}
	clk.Add(61 * time.Second)
	c.Sweep(clk.Now())
	if len(got) != 2 || got[0].Status != OutcomeNoConfirmation || got[1].Status != OutcomeNoConfirmation {
		t.Fatalf("after sweep: %+v", got)
	}

	// Exactly one pending for the device resolves it, even with another device pending.
	got = nil
	c.Record("id3", "dradis/p1", "+972503333333")
	c.Record("id4", "dradis/p2", "+972504444444")
	c.Observe(smsEvent("p1", "", false, "no service"))
	if len(got) != 1 || got[0].CommandID != "id3" || got[0].Status != OutcomeFailed {
		t.Fatalf("got %+v", got)
	}
}

func TestCorrelatorNilDeliver(t *testing.T) {
	clk := newClock()
	c := NewCorrelator(nil, clk.Now)
	c.Record("id1", "dradis/p1", "+972501111111")
	c.Observe(smsEvent("p1", "+972501111111", true, ""))
	c.Record("id2", "dradis/p1", "+972501111111")
	clk.Add(61 * time.Second)
	c.Sweep(clk.Now()) // must not panic
}

func TestCorrelatorForget(t *testing.T) {
	clk := newClock()
	var got []Outcome
	c := NewCorrelator(func(o Outcome) { got = append(got, o) }, clk.Now)
	c.Record("id1", "dradis/p1", "+972501111111")
	if !c.Forget("id1") || c.Forget("id1") {
		t.Error("Forget should succeed once")
	}
	clk.Add(61 * time.Second)
	c.Sweep(clk.Now())
	if len(got) != 0 {
		t.Errorf("forgotten entry reported: %+v", got)
	}
}

func TestCorrelatorEvictionReportsOnlyExpired(t *testing.T) {
	clk := newClock()
	var got []Outcome
	c := NewCorrelator(func(o Outcome) { got = append(got, o) }, clk.Now)
	c.Record("old", "dradis/p1", "+972501111111")
	clk.Add(61 * time.Second)
	for i := 0; i < maxPending; i++ { // young entries; "old" is evicted first
		c.Record(fmt.Sprint("y", i), "dradis/p1", "+972501111111")
	}
	if len(got) != 1 || got[0].CommandID != "old" {
		t.Fatalf("expired eviction: %+v", got)
	}
	got = nil
	c.Record("extra", "dradis/p1", "+972501111111") // evicts a young entry
	if len(got) != 0 {
		t.Errorf("young eviction emitted outcomes: %+v", got)
	}
	if len(c.pending) != maxPending {
		t.Errorf("pending = %d", len(c.pending))
	}
}

func TestSanitizeError(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"normal", "Radio off", "Radio off"},
		{"newline", "line1\nline2\r\nline3", "line1 line2 line3"},
		{"bidi override", "ok‮evil⁦x​", "okevilx"},
		{"nul and esc", "a\x00b\x1bc", "abc"},
		{"digits masked", "failed to +972501234567 now", "failed to +*** now"},
		{"short digits kept", "error 12345 code 99", "error 12345 code 99"},
		{"six digits", "id 123456", "id ***"},
		{"whitespace collapse", "  a \t\t b   ", "a b"},
	}
	for _, tt := range tests {
		if got := sanitizeError(tt.in, maxErrorLen); got != tt.want {
			t.Errorf("%s: got %q, want %q", tt.name, got, tt.want)
		}
	}
	if got := sanitizeError(strings.Repeat("x", 300), maxErrorLen); len([]rune(got)) != maxErrorLen {
		t.Errorf("length = %d", len([]rune(got)))
	}
	// Observe applies it.
	clk := newClock()
	var got []Outcome
	c := NewCorrelator(func(o Outcome) { got = append(got, o) }, clk.Now)
	c.Record("id1", "dradis/p1", "+972501111111")
	c.Observe(smsEvent("p1", "+972501111111", false, "bad\n‮ 0501111111"))
	if len(got) != 1 || got[0].Error != "bad ***" {
		t.Errorf("got %+v", got)
	}
}
