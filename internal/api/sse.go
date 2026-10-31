package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
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

	tick := time.NewTicker(hb)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.Hub.Done():
			return
		case ev, open := <-ch:
			if !open {
				return
			}
			if err := writeEvent(w, ev.ID, ev.Type, ev.Data); err != nil {
				return
			}
		case <-tick.C:
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
