package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/t0mer/cic/internal/auth"
	"github.com/t0mer/cic/internal/devices"
	"github.com/t0mer/cic/internal/events"
)

func (s *server) sse(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	hb := s.Heartbeat
	if hb <= 0 {
		hb = defaultHeartbeat
	}
	recheck := s.SessionRecheck
	if recheck <= 0 {
		recheck = defaultRecheck
	}
	sess, _ := auth.SessionFrom(r.Context())
	user, _ := auth.UserFrom(r.Context())
	// command.result carries the masked recipient of an SMS: administrators only. The role
	// is the one the stream was opened with.
	admin := user.Role == auth.RoleAdmin
	// ended closes the moment this session is logged out, evicted, disabled or has its
	// password changed in this process, so the stream does not outlive it.
	ended, stop, err := s.Auth.WatchSession(sess)
	if err != nil {
		if errors.Is(err, auth.ErrTooManyStreams) {
			w.Header().Set("Retry-After", "30")
			writeError(w, http.StatusTooManyRequests, "too many open event streams")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer stop()
	ch, cancel := s.Hub.Subscribe()
	defer cancel()

	// The server-wide WriteTimeout must not kill a long-lived stream.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	// Last-Event-ID is intentionally ignored: clients refetch /api/devices.
	if err := writeEvent(w, 0, "hello", s.brokerView()); err != nil {
		return
	}
	fl.Flush()

	// active re-validates the session before every write. A positive answer is cached
	// for activeCacheTTL so a burst of events costs one query, not one per event.
	var okUntil time.Time
	active := func() bool {
		if now := time.Now(); now.Before(okUntil) {
			return true
		}
		if !s.Auth.SessionActive(r.Context(), sess) {
			return false
		}
		okUntil = time.Now().Add(activeCacheTTL)
		return true
	}
	tick := time.NewTicker(hb)
	defer tick.Stop()
	poll := time.NewTicker(recheck)
	defer poll.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.Hub.Done():
			return
		case <-ended:
			return
		case <-poll.C:
			okUntil = time.Time{}
			if !active() {
				return
			}
			continue
		case ev, open := <-ch:
			if !open || !active() {
				return
			}
			if ev.Type == events.TypeCommandResult && !admin {
				continue
			}
			if !admin {
				// Hidden devices are never shown to viewers; a device that has just become
				// hidden is announced to them as removed.
				var drop bool
				if ev, drop = s.viewerView(ev); drop {
					continue
				}
			}
			if err := writeEvent(w, ev.ID, ev.Type, ev.Data); err != nil {
				return
			}
		case <-tick.C:
			if !active() {
				return
			}
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
		}
		fl.Flush()
	}
}

func writeEvent(w http.ResponseWriter, id uint64, typ string, data any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if id != 0 {
		if _, err := fmt.Fprintf(w, "id: %d\n", id); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", typ, b)
	return err
}

// viewerView adapts a device event for a non-admin stream; drop means send nothing. A device
// event whose payload type is not recognised is dropped (fail closed) and reported once per
// type, by type name only.
func (s *server) viewerView(ev events.Event) (events.Event, bool) {
	switch d := ev.Data.(type) {
	case devices.Device:
		return ev, d.Hidden
	case deviceUpdate:
		if d.Hidden {
			if d.becameHidden {
				return events.Event{ID: ev.ID, Type: events.TypeDeviceRemoved, Data: removedEvent{Key: d.Key}}, false
			}
			return ev, true
		}
		return ev, false
	case removedEvent:
		return ev, d.hidden
	}
	if ev.Type == events.TypeDeviceUpdated || ev.Type == events.TypeDeviceRemoved {
		name := fmt.Sprintf("%T", ev.Data)
		if _, seen := s.warnedTypes.LoadOrStore(name, true); !seen {
			s.log().Warn("dropping device event with unrecognised payload type for viewers", "type", name)
		}
		return ev, true
	}
	return ev, false
}
