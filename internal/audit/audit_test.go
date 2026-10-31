package audit

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/t0mer/cic/internal/store"
)

func newStore(t *testing.T, now func() time.Time) *Store {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return New(db, now)
}

func TestLogListNewestFirst(t *testing.T) {
	s := newStore(t, nil)
	ctx := context.Background()
	for _, a := range []string{"ping", "notify", "sms"} {
		if err := s.Log(ctx, Entry{User: "u", Device: "dradis/a", Action: a, Status: "sent", Detail: "d"}); err != nil {
			t.Fatal(err)
		}
	}
	p, err := s.List(ctx, 2, 0)
	if err != nil || len(p.Items) != 2 || p.Items[0].Action != "sms" || p.Items[1].Action != "notify" || p.NextBefore == nil {
		t.Fatalf("%+v %v", p, err)
	}
	p, _ = s.List(ctx, 2, *p.NextBefore)
	if len(p.Items) != 1 || p.Items[0].Action != "ping" || p.NextBefore != nil {
		t.Fatalf("%+v", p)
	}
}

func TestSetOutcomeUpdatesRow(t *testing.T) {
	s := newStore(t, nil)
	ctx := context.Background()
	_ = s.Log(ctx, Entry{CommandID: "c1", User: "u", Device: "d/a", Action: "sms", Status: "sent"})
	_ = s.Log(ctx, Entry{CommandID: "c2", User: "u", Device: "d/a", Action: "sms", Status: "sent"})
	if err := s.SetOutcome(ctx, "c1", "confirmed_ok"); err != nil {
		t.Fatal(err)
	}
	p, _ := s.List(ctx, 10, 0)
	if p.Items[1].Status != "confirmed_ok" || p.Items[0].Status != "sent" {
		t.Fatalf("%+v", p.Items)
	}
}

func TestOutcomeBeforeRowIsApplied(t *testing.T) {
	s := newStore(t, nil)
	ctx := context.Background()
	if err := s.SetOutcome(ctx, "early", "failed"); err != nil {
		t.Fatal(err)
	}
	_ = s.Log(ctx, Entry{CommandID: "early", User: "u", Device: "d/a", Action: "sms", Status: "sent"})
	_ = s.Log(ctx, Entry{CommandID: "", User: "u", Device: "d/a", Action: "ping", Status: "sent"})
	p, _ := s.List(ctx, 10, 0)
	if p.Items[1].Status != "failed" || p.Items[0].Status != "sent" {
		t.Fatalf("%+v", p.Items)
	}
	if len(s.stash) != 0 {
		t.Errorf("stash not drained: %v", s.stash)
	}
}

func TestPurgeAndJanitor(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	s := newStore(t, func() time.Time { return now })
	ctx := context.Background()
	for i, age := range []time.Duration{100 * 24 * time.Hour, 91 * 24 * time.Hour, 89 * 24 * time.Hour, time.Hour} {
		_ = s.Log(ctx, Entry{User: "u", Device: "d/a", Action: "ping", Status: "sent", Detail: string(rune('a' + i)), At: now.Add(-age)})
	}
	n, err := s.Purge(ctx, now.Add(-90*24*time.Hour))
	if err != nil || n != 2 {
		t.Fatalf("purged %d %v", n, err)
	}
	p, _ := s.List(ctx, 10, 0)
	if len(p.Items) != 2 {
		t.Fatalf("%+v", p.Items)
	}

	// The janitor purges on start and stops with its context.
	_ = s.Log(ctx, Entry{User: "u", Device: "d/a", Action: "ping", Status: "sent", At: now.Add(-200 * 24 * time.Hour)})
	jctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		s.RunJanitor(jctx, 2160*time.Hour, time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)))
		close(done)
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		p, _ = s.List(ctx, 10, 0)
		if len(p.Items) == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("janitor did not purge: %+v", p.Items)
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("janitor did not stop")
	}
}

func statusOf(t *testing.T, s *Store, id int64) (status, commandID string) {
	t.Helper()
	if err := s.db.QueryRow(`SELECT status, command_id FROM audit_log WHERE id = ?`, id).Scan(&status, &commandID); err != nil {
		t.Fatal(err)
	}
	return
}

func count(t *testing.T, s *Store) (n int) {
	t.Helper()
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM audit_log`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return
}

func TestBeginFinish(t *testing.T) {
	s := newStore(t, nil)
	ctx := context.Background()
	id, err := s.Begin(ctx, Entry{User: "u", Device: "d/a", Action: "sms"})
	if err != nil {
		t.Fatal(err)
	}
	if st, _ := statusOf(t, s, id); st != StatusPending {
		t.Fatalf("intent status %q", st)
	}
	if err := s.Finish(ctx, id, StatusSent, "c1"); err != nil {
		t.Fatal(err)
	}
	if st, cid := statusOf(t, s, id); st != StatusSent || cid != "c1" {
		t.Fatalf("%q %q", st, cid)
	}
}

func TestOutcomeBeforeFinishIsApplied(t *testing.T) {
	s := newStore(t, nil)
	ctx := context.Background()
	id, _ := s.Begin(ctx, Entry{User: "u", Device: "d/a", Action: "sms"})
	_ = s.SetOutcome(ctx, "fast", "confirmed_ok") // result beat Finish
	_ = s.Finish(ctx, id, StatusSent, "fast")
	if st, _ := statusOf(t, s, id); st != "confirmed_ok" {
		t.Fatalf("status %q", st)
	}
}

func TestUnknownThenLaterOutcomeUpdatesRow(t *testing.T) {
	s := newStore(t, nil)
	ctx := context.Background()
	id, _ := s.Begin(ctx, Entry{User: "u", Device: "d/a", Action: "sms"})
	if err := s.Finish(ctx, id, StatusUnknown, "u1"); err != nil {
		t.Fatal(err)
	}
	if st, cid := statusOf(t, s, id); st != StatusUnknown || cid != "u1" {
		t.Fatalf("%q %q", st, cid)
	}
	if err := s.SetOutcome(ctx, "u1", "no_confirmation"); err != nil {
		t.Fatal(err)
	}
	if st, _ := statusOf(t, s, id); st != "no_confirmation" {
		t.Fatalf("status %q", st)
	}
}

func TestRecoverPending(t *testing.T) {
	s := newStore(t, nil)
	ctx := context.Background()
	a, _ := s.Begin(ctx, Entry{User: "u", Device: "d/a", Action: "sms"})
	b, _ := s.Begin(ctx, Entry{User: "u", Device: "d/a", Action: "sms"})
	_ = s.Finish(ctx, b, StatusSent, "x")
	n, err := s.RecoverPending(ctx)
	if err != nil || n != 1 {
		t.Fatalf("recovered %d %v", n, err)
	}
	if st, _ := statusOf(t, s, a); st != StatusUnknown {
		t.Fatalf("pending -> %q", st)
	}
	if st, _ := statusOf(t, s, b); st != StatusSent {
		t.Fatalf("sent -> %q", st)
	}
}

func TestFinishFailureIsCounted(t *testing.T) {
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "f.db"))
	if err != nil {
		t.Fatal(err)
	}
	s := New(db, nil)
	id, _ := s.Begin(context.Background(), Entry{User: "u", Device: "d/a", Action: "ping"})
	db.Close()
	if err := s.Finish(context.Background(), id, StatusSent, ""); err == nil {
		t.Fatal("want error")
	}
	if s.FinishFailures() != 1 {
		t.Fatalf("failures %d", s.FinishFailures())
	}
}

func TestDetailCappedAtRuneBoundary(t *testing.T) {
	s := newStore(t, nil)
	ctx := context.Background()
	for _, prefix := range []string{"", "a", "ab", "abc"} {
		id, err := s.Begin(ctx, Entry{User: "u", Device: "d/a", Action: "notify", Detail: prefix + strings.Repeat("é", 100)})
		if err != nil {
			t.Fatal(err)
		}
		var d string
		_ = s.db.QueryRow(`SELECT detail FROM audit_log WHERE id = ?`, id).Scan(&d)
		if len(d) > 120 || !utf8.ValidString(d) || len(d) < 118 {
			t.Errorf("prefix %q: len %d valid %v", prefix, len(d), utf8.ValidString(d))
		}
	}
}

func TestRejectedRowsCappedPerUserPerHour(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	s := newStore(t, func() time.Time { return now })
	s.rejCap = 3
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		_ = s.Log(ctx, Entry{User: "spam", Device: "d/a", Action: "ping", Status: "rejected:rate_limited"})
	}
	_ = s.Log(ctx, Entry{User: "other", Device: "d/a", Action: "ping", Status: "rejected:rate_limited"})
	_ = s.Log(ctx, Entry{User: "spam", Device: "d/a", Action: "ping", Status: "sent"}) // not rejected: always stored
	if n := count(t, s); n != 5 {
		t.Fatalf("rows %d, want 3+1+1", n)
	}
	if s.RejectedDropped() != 2 {
		t.Fatalf("dropped %d", s.RejectedDropped())
	}
	// Begin/Finish path: the over-cap row is deleted.
	id, _ := s.Begin(ctx, Entry{User: "spam", Device: "d/a", Action: "ping"})
	_ = s.Finish(ctx, id, "rejected:rate_limited", "")
	if n := count(t, s); n != 5 || s.RejectedDropped() != 3 {
		t.Fatalf("rows %d dropped %d", n, s.RejectedDropped())
	}
	// A new window stores again.
	now = now.Add(61 * time.Minute)
	_ = s.Log(ctx, Entry{User: "spam", Device: "d/a", Action: "ping", Status: "rejected:rate_limited"})
	if n := count(t, s); n != 6 {
		t.Fatalf("rows %d", n)
	}
}

func TestTableCapDeletesOldest(t *testing.T) {
	s := newStore(t, nil)
	s.maxRows = 100
	ctx := context.Background()
	for i := 0; i < 101; i++ {
		_ = s.Log(ctx, Entry{User: "u", Device: "d/a", Action: "ping", Status: "sent", Detail: strconv.Itoa(i)})
	}
	if n := count(t, s); n != 96 {
		t.Fatalf("rows %d, want 96 (101 minus 5%% of 100)", n)
	}
	var oldest string
	_ = s.db.QueryRow(`SELECT detail FROM audit_log ORDER BY id LIMIT 1`).Scan(&oldest)
	if oldest != "5" {
		t.Fatalf("oldest %q", oldest)
	}
}

func TestZeroRetentionMeansDefaultNotForever(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	s := newStore(t, func() time.Time { return now })
	ctx := context.Background()
	_ = s.Log(ctx, Entry{User: "u", Device: "d/a", Action: "ping", Status: "sent", At: now.Add(-100 * 24 * time.Hour)})
	_ = s.Log(ctx, Entry{User: "u", Device: "d/a", Action: "ping", Status: "sent", At: now.Add(-24 * time.Hour)})
	jctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		s.RunJanitor(jctx, 0, time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)))
		close(done)
	}()
	deadline := time.Now().Add(3 * time.Second)
	for count(t, s) != 1 {
		if time.Now().After(deadline) {
			t.Fatal("zero retention kept old rows")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
}

func TestStashExpiresAndEvictsOldest(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	s := newStore(t, func() time.Time { return now })
	ctx := context.Background()
	_ = s.SetOutcome(ctx, "old", "failed")
	now = now.Add(stashTTL + time.Second)
	_ = s.Log(ctx, Entry{CommandID: "old", User: "u", Device: "d/a", Action: "sms", Status: "sent"})
	var st string
	_ = s.db.QueryRow(`SELECT status FROM audit_log WHERE command_id = 'old'`).Scan(&st)
	if st != "sent" {
		t.Fatalf("expired stash applied: %q", st)
	}

	// Full stash evicts the oldest instead of refusing the newest.
	for i := 0; i < maxStash+5; i++ {
		now = now.Add(time.Millisecond)
		_ = s.SetOutcome(ctx, "k"+strconv.Itoa(i), "failed")
	}
	if len(s.stash) > maxStash {
		t.Fatalf("stash %d", len(s.stash))
	}
	if _, ok := s.stash["k0"]; ok {
		t.Error("oldest not evicted")
	}
	if _, ok := s.stash["k"+strconv.Itoa(maxStash+4)]; !ok {
		t.Error("newest refused")
	}
}

func TestSetOutcomeUsesCommandIDIndex(t *testing.T) {
	s := newStore(t, nil)
	rows, err := s.db.Query(`EXPLAIN QUERY PLAN UPDATE audit_log SET status = ? WHERE command_id = ?`, "x", "y")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	if !strings.Contains(strings.Join(plan, "|"), "USING INDEX audit_log_command_id") {
		t.Fatalf("plan does not use the command_id index: %v", plan)
	}
}
