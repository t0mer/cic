package commands

import (
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/t0mer/cic/internal/mqtt"
)

// Outcome statuses.
const (
	OutcomeConfirmed      = "confirmed_ok"
	OutcomeFailed         = "failed"
	OutcomeNoConfirmation = "no_confirmation"
)

const (
	correlationWindow = 60 * time.Second
	maxPending        = 1000
	maxErrorLen       = 200
)

// Outcome is the final state of one SMS command. Phone is normalized and
// unmasked; callers must mask it before logging.
type Outcome struct {
	CommandID string
	DeviceKey string
	Phone     string
	Status    string
	Error     string // phone-side error, only for OutcomeFailed
	At        time.Time
}

type pendingSMS struct {
	id, key, phone string // phone is the display form (punctuation stripped)
	digits         string // canonical digits-only form used for matching
	at             time.Time
}

// Correlator matches sendsms/result events to the SMS commands that caused them.
//
// The wiring MUST call Sweep(time.Now()) about every 5 seconds: Sweep is the
// only thing that reports no_confirmation for SMS whose result never arrived
// and that frees their pending entries.
type Correlator struct {
	deliver func(Outcome)
	now     func() time.Time

	mu      sync.Mutex
	pending []pendingSMS // oldest first
}

// NewCorrelator returns a Correlator that calls deliver for every Outcome.
// A nil deliver discards outcomes; a nil now uses time.Now.
func NewCorrelator(deliver func(Outcome), now func() time.Time) *Correlator {
	if deliver == nil {
		deliver = func(Outcome) {}
	}
	if now == nil {
		now = time.Now
	}
	return &Correlator{deliver: deliver, now: now}
}

func normalizePhone(p string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case ' ', '-', '(', ')', '.':
			return -1
		}
		return r
	}, p)
}

// phoneDigits canonicalizes a phone number to its ASCII digits, so "+972 50-123",
// "972501234567" and "(+972) 50.123" compare equal. Local-format numbers keep
// their leading 0 and therefore never match an international one.
func phoneDigits(p string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, p)
}

// Record registers an SMS awaiting its result. Call it BEFORE publishing so a
// fast result cannot arrive first; undo with Forget if the publish provably
// did not send.
func (c *Correlator) Record(id, deviceKey, phone string) {
	now := c.now()
	var evicted []Outcome
	c.mu.Lock()
	c.pending = append(c.pending, pendingSMS{id, deviceKey, normalizePhone(phone), phoneDigits(phone), now})
	young := 0
	for len(c.pending) > maxPending {
		p := c.pending[0]
		c.pending = c.pending[1:]
		if now.Sub(p.at) > correlationWindow {
			evicted = append(evicted, noConfirmation(p, now))
		} else {
			young++ // never confirmable once evicted; nothing to report
		}
	}
	c.mu.Unlock()
	if young > 0 {
		slog.Debug("sms correlator full, evicted unexpired entries", "evicted", young, "max", maxPending)
	}
	c.emit(evicted...)
}

// Forget removes the pending entry for id without emitting an outcome. It
// reports whether the entry existed.
func (c *Correlator) Forget(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, p := range c.pending {
		if p.id == id {
			c.pending = append(c.pending[:i], c.pending[i+1:]...)
			return true
		}
	}
	return false
}

// Observe consumes a sendsms/result event; other events are ignored. The
// oldest pending SMS for the same device and phone (compared as digits only)
// within 60 s is resolved. A result without a phone resolves a pending SMS
// only when exactly one is pending for the device; otherwise it is ambiguous
// and the entries are left for Sweep.
func (c *Correlator) Observe(ev mqtt.Event) {
	res, ok := ev.Payload.(mqtt.SMSResult)
	if !ok || ev.Leaf != mqtt.LeafSMSResult {
		return
	}
	key := ev.Prefix + "/" + ev.Device
	digits := phoneDigits(res.Phone)
	now := c.now()
	c.mu.Lock()
	idx, forDevice := -1, 0
	for i, p := range c.pending {
		if p.key != key || now.Sub(p.at) > correlationWindow {
			continue
		}
		forDevice++
		if idx < 0 && digits != "" && p.digits == digits {
			idx = i
		}
		if idx < 0 && digits == "" {
			idx = i
		}
	}
	if digits == "" && forDevice != 1 {
		idx = -1 // ambiguous: leave for Sweep
	}
	if idx < 0 {
		c.mu.Unlock()
		return
	}
	p := c.pending[idx]
	c.pending = append(c.pending[:idx], c.pending[idx+1:]...)
	c.mu.Unlock()

	out := Outcome{CommandID: p.id, DeviceKey: p.key, Phone: p.phone, Status: OutcomeConfirmed, At: now}
	if !res.OK {
		out.Status = OutcomeFailed
		out.Error = sanitizeError(res.Error, maxErrorLen)
	}
	c.emit(out)
}

// Sweep reports no_confirmation for every pending SMS older than 60 s. It must
// be called about every 5 s with time.Now().
func (c *Correlator) Sweep(now time.Time) {
	var expired []Outcome
	c.mu.Lock()
	keep := c.pending[:0]
	for _, p := range c.pending {
		if now.Sub(p.at) > correlationWindow {
			expired = append(expired, noConfirmation(p, now))
		} else {
			keep = append(keep, p)
		}
	}
	c.pending = keep
	c.mu.Unlock()
	c.emit(expired...)
}

func noConfirmation(p pendingSMS, now time.Time) Outcome {
	return Outcome{CommandID: p.id, DeviceKey: p.key, Phone: p.phone, Status: OutcomeNoConfirmation, At: now}
}

func (c *Correlator) emit(outs ...Outcome) {
	for _, o := range outs {
		c.deliver(o)
	}
}

// sanitizeError makes a phone-supplied error string safe to store and show:
// whitespace collapses to single spaces, control (Cc) and format (Cf)
// characters are dropped, runs of 6 or more digits (possible phone numbers)
// become "***", and the result is cut to n runes.
func sanitizeError(s string, n int) string {
	s = strings.ToValidUTF8(s, "")
	s = strings.Map(func(r rune) rune {
		switch {
		case unicode.IsSpace(r):
			return ' '
		case unicode.In(r, unicode.Cc, unicode.Cf):
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	var b strings.Builder
	rs := []rune(s)
	for i := 0; i < len(rs); {
		if !unicode.IsDigit(rs[i]) {
			b.WriteRune(rs[i])
			i++
			continue
		}
		j := i
		for j < len(rs) && unicode.IsDigit(rs[j]) {
			j++
		}
		if j-i >= 6 {
			b.WriteString("***")
		} else {
			b.WriteString(string(rs[i:j]))
		}
		i = j
	}
	return strings.TrimSpace(trimRunes(b.String(), n))
}

func trimRunes(s string, n int) string {
	s = strings.ToValidUTF8(strings.TrimSpace(s), "")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
