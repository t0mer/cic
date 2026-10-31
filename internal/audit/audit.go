// Package audit records every remote command attempt.
//
// Rows never hold full phone numbers or message bodies: callers pass the masked
// and previewed forms from package commands (MaskPhone, Preview). Detail is cut to
// 120 bytes.
//
// A command is audited in two steps: Begin writes a pending intent row before the
// command is published, Finish sets the final status afterwards. Rows left pending
// by a crash are marked unknown by RecoverPending at startup.
package audit

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// Entry is one command attempt. CommandID may be empty (rejected attempts).
type Entry struct {
	CommandID string
	At        time.Time
	User      string
	Device    string
	Action    string
	Status    string // pending, sent, unknown, rejected:<code>, confirmed_ok, failed, no_confirmation
	Detail    string
}

// Item is a stored row as served by the activity API.
type Item struct {
	ID     int64     `json:"id"`
	At     time.Time `json:"at"`
	User   string    `json:"user"`
	Device string    `json:"device"`
	Action string    `json:"action"`
	Status string    `json:"status"`
	Detail string    `json:"detail"`
}

// Page is one page of items, newest first. NextBefore is nil on the last page.
type Page struct {
	Items      []Item `json:"items"`
	NextBefore *int64 `json:"next_before"`
}

// Limits. Values are fields on Store so tests can shrink them.
const (
	maxStash         = 1000
	stashTTL         = 10 * time.Minute
	maxDetailBytes   = 120
	DefaultRetention = 2160 * time.Hour // 90 days; used when a retention of zero or less is configured
	defaultMaxRows   = 200_000
	defaultRejCap    = 60 // stored rejected rows per user per rejWindow
	rejWindow        = time.Hour
)

// Status values besides rejected:<code> and the outcome statuses of package commands.
const (
	StatusPending = "pending" // intent row, written before the command is published
	StatusSent    = "sent"
	StatusUnknown = "unknown" // the broker did not confirm; the command may have been delivered
)

type stashed struct {
	status string
	at     time.Time
}

// Store is the SQLite audit log.
type Store struct {
	db  *sql.DB
	now func() time.Time

	// mu serializes writes so an outcome that arrives before its row is linked is
	// stashed and applied when the row gets its command id.
	mu      sync.Mutex
	stash   map[string]stashed
	rows    int64 // cached row count; -1 = unknown, recount on next insert
	maxRows int
	rejCap  int

	rejectedDropped atomic.Int64
	finishFailures  atomic.Int64
}

// New returns a Store. A nil now uses time.Now.
func New(db *sql.DB, now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{db: db, now: now, stash: map[string]stashed{}, rows: -1, maxRows: defaultMaxRows, rejCap: defaultRejCap}
}

// RejectedDropped counts rejected attempts that were not stored because their user
// exceeded the per-hour cap.
func (s *Store) RejectedDropped() int64 { return s.rejectedDropped.Load() }

// FinishFailures counts failed Finish updates (rows left pending).
func (s *Store) FinishFailures() int64 { return s.finishFailures.Load() }

// capDetail cuts detail to maxDetailBytes without splitting a rune.
func capDetail(d string) string {
	d = strings.ToValidUTF8(d, "")
	if len(d) <= maxDetailBytes {
		return d
	}
	n := maxDetailBytes
	for n > 0 && !utf8.RuneStart(d[n]) {
		n--
	}
	return d[:n]
}

func isRejected(status string) bool { return strings.HasPrefix(status, "rejected:") }

// overRejectedCap reports whether user already has the maximum of stored rejected rows
// in the current window. exclude is a row id not to count (the row being finished).
func (s *Store) overRejectedCap(ctx context.Context, user string, exclude int64) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log
		WHERE user = ? AND at >= ? AND status LIKE 'rejected:%' AND id <> ?`,
		user, s.now().Add(-rejWindow).Unix(), exclude).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("counting rejected audit rows: %w", err)
	}
	return n >= s.rejCap, nil
}

// insert writes one row and enforces the table cap. mu must be held.
func (s *Store) insert(ctx context.Context, e Entry) (int64, error) {
	if e.At.IsZero() {
		e.At = s.now()
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO audit_log(at, user, device, action, status, detail, command_id)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, e.At.Unix(), e.User, e.Device, e.Action, e.Status, capDetail(e.Detail), e.CommandID)
	if err != nil {
		return 0, fmt.Errorf("writing audit log: %w", err)
	}
	id, _ := res.LastInsertId()
	if s.rows < 0 {
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log`).Scan(&s.rows); err != nil {
			s.rows = -1
			return id, nil // the row is written; the cap check runs on the next insert
		}
	} else {
		s.rows++
	}
	if s.rows > int64(s.maxRows) {
		drop := max(1, s.maxRows/20)
		if _, err := s.db.ExecContext(ctx, `DELETE FROM audit_log WHERE id IN
			(SELECT id FROM audit_log ORDER BY id LIMIT ?)`, drop); err == nil {
			s.rows = -1
		}
	}
	return id, nil
}

// Log writes one finished row. A zero At means now. A rejected row beyond the per-user
// hourly cap is counted but not stored.
func (s *Store) Log(ctx context.Context, e Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if isRejected(e.Status) {
		over, err := s.overRejectedCap(ctx, e.User, 0)
		if err != nil {
			return err
		}
		if over {
			s.rejectedDropped.Add(1)
			return nil
		}
	}
	if e.CommandID != "" {
		if st, ok := s.takeStash(e.CommandID); ok {
			e.Status = st
		}
	}
	_, err := s.insert(ctx, e)
	return err
}

// Begin writes the intent row (status pending unless e.Status is set) BEFORE a command
// is published and returns its row id. If it fails the caller must not publish.
func (s *Store) Begin(ctx context.Context, e Entry) (int64, error) {
	if e.Status == "" {
		e.Status = StatusPending
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.insert(ctx, e)
}

// Finish sets the final status (and the command id, when known) of a row from Begin.
// An outcome that arrived earlier for commandID wins over status. A rejected row beyond
// the per-user hourly cap is deleted and counted instead. Failures are counted.
func (s *Store) Finish(ctx context.Context, rowID int64, status, commandID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.finish(ctx, rowID, status, commandID)
	if err != nil {
		s.finishFailures.Add(1)
	}
	return err
}

func (s *Store) finish(ctx context.Context, rowID int64, status, commandID string) error {
	if isRejected(status) {
		var user string
		if err := s.db.QueryRowContext(ctx, `SELECT user FROM audit_log WHERE id = ?`, rowID).Scan(&user); err != nil {
			return fmt.Errorf("finishing audit row: %w", err)
		}
		over, err := s.overRejectedCap(ctx, user, rowID)
		if err != nil {
			return err
		}
		if over {
			if _, err := s.db.ExecContext(ctx, `DELETE FROM audit_log WHERE id = ?`, rowID); err != nil {
				return fmt.Errorf("finishing audit row: %w", err)
			}
			s.rows = -1
			s.rejectedDropped.Add(1)
			return nil
		}
	}
	if commandID != "" {
		if st, ok := s.takeStash(commandID); ok {
			status = st
		}
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE audit_log SET status = ?, command_id = ? WHERE id = ?`, status, commandID, rowID); err != nil {
		return fmt.Errorf("finishing audit row: %w", err)
	}
	return nil
}

// RecoverPending marks rows left pending by a crash as unknown and returns how many.
func (s *Store) RecoverPending(ctx context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.ExecContext(ctx, `UPDATE audit_log SET status = ? WHERE status = ?`, StatusUnknown, StatusPending)
	if err != nil {
		return 0, fmt.Errorf("recovering pending audit rows: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// takeStash returns and removes a stashed, unexpired outcome. mu must be held.
func (s *Store) takeStash(id string) (string, bool) {
	st, ok := s.stash[id]
	if !ok {
		return "", false
	}
	delete(s.stash, id)
	if s.now().Sub(st.at) > stashTTL {
		return "", false
	}
	return st.status, true
}

// addStash remembers an outcome whose row is not linked yet, dropping expired entries
// and, when full, the oldest. mu must be held.
func (s *Store) addStash(id, status string) {
	now := s.now()
	s.sweepStashLocked(now)
	for len(s.stash) >= maxStash {
		oldest, first := "", true
		var at time.Time
		for k, v := range s.stash {
			if first || v.at.Before(at) {
				oldest, at, first = k, v.at, false
			}
		}
		delete(s.stash, oldest)
	}
	s.stash[id] = stashed{status, now}
}

func (s *Store) sweepStashLocked(now time.Time) {
	for k, v := range s.stash {
		if now.Sub(v.at) > stashTTL {
			delete(s.stash, k)
		}
	}
}

// SetOutcome sets the status of the row for commandID. If no row carries that id yet
// (the result beat the row, or the row is still pending) the status is kept for
// stashTTL and applied when the row gets its id.
func (s *Store) SetOutcome(ctx context.Context, commandID, status string) error {
	if commandID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.ExecContext(ctx, `UPDATE audit_log SET status = ? WHERE command_id = ?`, status, commandID)
	if err != nil {
		return fmt.Errorf("updating audit log: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		s.addStash(commandID, status)
	}
	return nil
}

// List returns up to limit rows with id < before (before <= 0 means from the newest).
func (s *Store) List(ctx context.Context, limit int, before int64) (Page, error) {
	if before <= 0 {
		before = 1<<63 - 1
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, at, user, device, action, status, detail
		FROM audit_log WHERE id < ? ORDER BY id DESC LIMIT ?`, before, limit+1)
	if err != nil {
		return Page{}, fmt.Errorf("listing audit log: %w", err)
	}
	defer rows.Close()
	p := Page{Items: []Item{}}
	for rows.Next() {
		var it Item
		var at int64
		if err := rows.Scan(&it.ID, &at, &it.User, &it.Device, &it.Action, &it.Status, &it.Detail); err != nil {
			return Page{}, fmt.Errorf("scanning audit log: %w", err)
		}
		it.At = time.Unix(at, 0).UTC()
		p.Items = append(p.Items, it)
	}
	if err := rows.Err(); err != nil {
		return Page{}, fmt.Errorf("listing audit log: %w", err)
	}
	if len(p.Items) > limit {
		p.Items = p.Items[:limit]
		id := p.Items[limit-1].ID
		p.NextBefore = &id
	}
	return p, nil
}

// Purge deletes rows older than olderThan and returns how many.
func (s *Store) Purge(ctx context.Context, olderThan time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM audit_log WHERE at < ?`, olderThan.Unix())
	if err != nil {
		return 0, fmt.Errorf("purging audit log: %w", err)
	}
	n, _ := res.RowsAffected()
	s.mu.Lock()
	s.rows = -1
	s.mu.Unlock()
	return n, nil
}

// RunJanitor purges rows older than retention now and then every `every` until
// ctx is done. A retention of zero or less means DefaultRetention, never "forever".
func (s *Store) RunJanitor(ctx context.Context, retention, every time.Duration, log *slog.Logger) {
	if retention <= 0 {
		retention = DefaultRetention
	}
	run := func() {
		s.mu.Lock()
		s.sweepStashLocked(s.now())
		s.mu.Unlock()
		n, err := s.Purge(ctx, s.now().Add(-retention))
		switch {
		case err != nil && ctx.Err() == nil:
			log.Warn("audit purge failed", "err", err)
		case n > 0:
			log.Info("audit rows purged", "rows", n)
		}
	}
	run()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			run()
		}
	}
}
