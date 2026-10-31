package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/t0mer/cic/internal/auth"
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
