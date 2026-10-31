package commands

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/t0mer/cic/internal/devices"
	"github.com/t0mer/cic/internal/mqtt"
	"github.com/t0mer/cic/internal/store"
)

type pubCall struct {
	topic   string
	payload string
}

type fakePub struct {
	mu    sync.Mutex
	calls []pubCall
	err   error
}

func (p *fakePub) Publish(_ context.Context, topic string, payload []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return p.err
	}
	p.calls = append(p.calls, pubCall{topic, string(payload)})
	return nil
}

type fakeDevs map[string]bool

func (f fakeDevs) Get(_ context.Context, key string) (devices.Device, error) {
	if f[key] {
		return devices.Device{Key: key}, nil
	}
	return devices.Device{}, store.ErrNotFound
}

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "cic.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func newSvc(t *testing.T, cap int) (*Service, *fakePub, *clock) {
	t.Helper()
	pub := &fakePub{}
	clk := newClock()
	svc := NewService(pub, fakeDevs{"dradis/p1": true}, Config{
		Prefixes: []string{"dradis"}, PingSeconds: 30, SMSDailyCap: cap,
		Now: clk.Now, Usage: SQLUsage{DB: openDB(t)},
	})
	return svc, pub, clk
}

const dev = "dradis/p1"

func TestPayloads(t *testing.T) {
	svc, pub, _ := newSvc(t, 10)
	ctx := context.Background()
	steps := []struct {
		do      func() (Result, error)
		action  string
		topic   string
		payload string
	}{
		{func() (Result, error) { return svc.Ping(ctx, "u", dev, 0) }, ActionPing, "dradis/p1/ping", `{"seconds":30}`},
		{func() (Result, error) { return svc.Ping(ctx, "u", dev, 600) }, ActionPing, "dradis/p1/ping", `{"seconds":600}`},
		{func() (Result, error) { return svc.StopPing(ctx, "u", dev) }, ActionStopPing, "dradis/p1/ping", `{"seconds":0}`},
		{func() (Result, error) { return svc.Notify(ctx, "u", dev, "", "hi \"there\"\n") }, ActionNotify, "dradis/p1/notify", `{"text":"hi \"there\"\n","title":"CIC"}`},
		{func() (Result, error) { return svc.SendSMS(ctx, "u", dev, "+972501234567", "שלום") }, ActionSendSMS, "dradis/p1/sendsms", `{"phone":"+972501234567","text":"שלום"}`},
		{func() (Result, error) { return svc.GetStatus(ctx, "u", dev) }, ActionGetStatus, "dradis/p1/getstatus", `{}`},
		{func() (Result, error) { return svc.GetLocation(ctx, "u", dev) }, ActionGetLocation, "dradis/p1/getlocation", `{}`},
	}
	ids := map[string]bool{}
	for i, s := range steps {
		res, err := s.do()
		if err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		if res.Action != s.action || res.DeviceKey != dev || len(res.ID) != 32 || ids[res.ID] || res.SentAt.IsZero() {
			t.Errorf("step %d result = %+v", i, res)
		}
		ids[res.ID] = true
		c := pub.calls[i]
		if c.topic != s.topic || c.payload != s.payload {
			t.Errorf("step %d published %+v", i, c)
		}
		if !json.Valid([]byte(c.payload)) {
			t.Errorf("step %d invalid JSON", i)
		}
	}
}

func TestValidation(t *testing.T) {
	svc, pub, _ := newSvc(t, 10)
	ctx := context.Background()
	long := func(n int) string { return strings.Repeat("ש", n) }
	cases := map[string]func() error{
		"ping negative":  func() error { _, err := svc.Ping(ctx, "u", dev, -1); return err },
		"ping too long":  func() error { _, err := svc.Ping(ctx, "u", dev, 601); return err },
		"notify no text": func() error { _, err := svc.Notify(ctx, "u", dev, "t", ""); return err },
		"notify long":    func() error { _, err := svc.Notify(ctx, "u", dev, "t", long(1001)); return err },
		"title long":     func() error { _, err := svc.Notify(ctx, "u", dev, long(101), "x"); return err },
		"control char":   func() error { _, err := svc.Notify(ctx, "u", dev, "t", "a\x00b"); return err },
		"escape":         func() error { _, err := svc.Notify(ctx, "u", dev, "t", "a\x1bb"); return err },
		"bad utf8":       func() error { _, err := svc.Notify(ctx, "u", dev, "t", "a\xffb"); return err },
		"sms no plus":    func() error { _, err := svc.SendSMS(ctx, "u", dev, "972501234567", "x"); return err },
		"sms short":      func() error { _, err := svc.SendSMS(ctx, "u", dev, "+12345", "x"); return err },
		"sms spaces":     func() error { _, err := svc.SendSMS(ctx, "u", dev, "+972 50 123", "x"); return err },
		"sms leading 0":  func() error { _, err := svc.SendSMS(ctx, "u", dev, "+0501234567", "x"); return err },
		"sms no text":    func() error { _, err := svc.SendSMS(ctx, "u", dev, "+972501234567", ""); return err },
		"sms long":       func() error { _, err := svc.SendSMS(ctx, "u", dev, "+972501234567", long(1601)); return err },
		"key no prefix":  func() error { _, err := svc.GetStatus(ctx, "u", "p1"); return err },
		"key bad prefix": func() error { _, err := svc.GetStatus(ctx, "u", "evil/p1"); return err },
		"key wildcard":   func() error { _, err := svc.GetStatus(ctx, "u", "dradis/+"); return err },
		"key extra":      func() error { _, err := svc.GetStatus(ctx, "u", "dradis/p1/ping"); return err },
	}
	for name, f := range cases {
		if err := f(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: got %v, want ErrInvalid", name, err)
		}
	}
	if _, err := svc.Notify(ctx, "u", dev, "t", "tab\tand\nnewline"); err != nil {
		t.Errorf("tab/newline rejected: %v", err)
	}
	if _, err := svc.Notify(ctx, "u", dev, "t", long(1000)); err != nil {
		t.Errorf("1000 runes rejected: %v", err)
	}
	if len(pub.calls) != 2 {
		t.Errorf("invalid commands were published: %d calls", len(pub.calls))
	}
}

func TestUnknownDeviceAndNotConnected(t *testing.T) {
	svc, pub, _ := newSvc(t, 10)
	ctx := context.Background()
	if _, err := svc.GetStatus(ctx, "u", "dradis/ghost"); !errors.Is(err, ErrUnknownDevice) {
		t.Errorf("got %v", err)
	}
	if len(pub.calls) != 0 {
		t.Error("published to unknown device")
	}
	pub.err = mqtt.ErrNotConnected
	if _, err := svc.GetStatus(ctx, "u", dev); !errors.Is(err, ErrNotConnected) {
		t.Errorf("got %v", err)
	}
}

// phoneN returns a distinct valid E.164 number per n.
func phoneN(n int) string { return fmt.Sprintf("+97250%07d", n) }

func TestRateLimits(t *testing.T) {
	svc, _, clk := newSvc(t, 1000)
	ctx := context.Background()
	var n atomic.Int32
	for _, tc := range []struct {
		name string
		max  int
		do   func(user string) error
	}{
		{"ping", 10, func(u string) error { _, err := svc.Ping(ctx, u, dev, 5); return err }},
		{"notify", 10, func(u string) error { _, err := svc.Notify(ctx, u, dev, "", "x"); return err }},
		{"sms", 5, func(u string) error {
			_, err := svc.SendSMS(ctx, u, dev, phoneN(int(n.Add(1))), "x")
			return err
		}},
		{"getstatus", 20, func(u string) error { _, err := svc.GetStatus(ctx, u, dev); return err }},
		{"getlocation", 20, func(u string) error { _, err := svc.GetLocation(ctx, u, dev); return err }},
	} {
		for i := 0; i < tc.max; i++ {
			if err := tc.do("alice"); err != nil {
				t.Fatalf("%s %d: %v", tc.name, i, err)
			}
		}
		if err := tc.do("alice"); !errors.Is(err, ErrRateLimited) {
			t.Errorf("%s: got %v, want ErrRateLimited", tc.name, err)
		}
		if err := tc.do("bob"); err != nil {
			t.Errorf("%s: other user limited: %v", tc.name, err)
		}
	}
	clk.Add(61 * time.Second)
	if _, err := svc.Ping(ctx, "alice", dev, 5); err != nil {
		t.Errorf("window did not slide: %v", err)
	}
}

func TestPingAndStopShareOneBucket(t *testing.T) {
	svc, _, _ := newSvc(t, 10)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if _, err := svc.Ping(ctx, "u", dev, 5); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.StopPing(ctx, "u", dev); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.StopPing(ctx, "u", dev); !errors.Is(err, ErrRateLimited) {
		t.Errorf("stop: got %v, want ErrRateLimited", err)
	}
	if _, err := svc.Ping(ctx, "u", dev, 5); !errors.Is(err, ErrRateLimited) {
		t.Errorf("ping: got %v, want ErrRateLimited", err)
	}
}

func TestUnknownDeviceDoesNotConsumeRateSlot(t *testing.T) {
	svc, _, _ := newSvc(t, 10)
	ctx := context.Background()
	for i := 0; i < 50; i++ {
		if _, err := svc.GetStatus(ctx, "u", "dradis/ghost"); !errors.Is(err, ErrUnknownDevice) {
			t.Fatalf("got %v", err)
		}
	}
	if _, err := svc.GetStatus(ctx, "u", dev); err != nil {
		t.Errorf("unknown-device attempts consumed the slot: %v", err)
	}
}

func TestLimiterPrunesIdleKeys(t *testing.T) {
	l := newLimiter(time.Minute)
	now := time.Now()
	for i := 0; i < 100; i++ {
		l.allow(fmt.Sprintf("k%d", i), 5, now)
	}
	later := now.Add(2 * time.Minute)
	// Reaching a sweep boundary prunes every idle key, not only the one touched.
	for l.calls%sweepEvery != sweepEvery-1 {
		l.allow("busy", 5, later)
	}
	l.allow("busy", 5, later)
	if len(l.hits) != 1 {
		t.Errorf("idle keys not pruned: %d keys", len(l.hits))
	}
	l.undo("busy", later)
	if _, ok := l.hits["busy"]; ok && len(l.hits["busy"]) == 0 {
		t.Error("empty key kept after undo")
	}
}

func TestSMSDisabled(t *testing.T) {
	svc, pub, _ := newSvc(t, 0)
	if _, err := svc.SendSMS(context.Background(), "u", dev, "+972501234567", "x"); !errors.Is(err, ErrSMSDisabled) {
		t.Errorf("got %v", err)
	}
	if len(pub.calls) != 0 {
		t.Error("published while disabled")
	}
}

func usageCount(t *testing.T, svc *Service) int {
	t.Helper()
	var n int
	err := svc.cfg.Usage.(SQLUsage).DB.QueryRow(`SELECT COALESCE(SUM(count),0) FROM sms_usage`).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSMSDailyCapAndRollover(t *testing.T) {
	svc, pub, clk := newSvc(t, 3)
	ctx := context.Background()
	var n int
	send := func(user string) error {
		n++
		_, err := svc.SendSMS(ctx, user, dev, phoneN(n), "x")
		return err
	}
	for i := 0; i < 3; i++ {
		if err := send("u" + string(rune('a'+i))); err != nil {
			t.Fatal(err)
		}
	}
	if err := send("ud"); !errors.Is(err, ErrSMSCap) {
		t.Fatalf("got %v, want ErrSMSCap", err)
	}
	if len(pub.calls) != 3 {
		t.Errorf("cap-exhausted send was published: %d calls", len(pub.calls))
	}
	// Day rollover (UTC): the clock starts at 23:59.
	clk.Add(2 * time.Minute)
	if err := send("ue"); err != nil {
		t.Fatalf("after rollover: %v", err)
	}
	if len(pub.calls) != 4 {
		t.Errorf("calls = %d", len(pub.calls))
	}
}

func TestSMSCapCountsSegments(t *testing.T) {
	svc, pub, _ := newSvc(t, 3)
	ctx := context.Background()
	four := strings.Repeat("a", 4*153) // 4 segments > cap 3
	if _, err := svc.SendSMS(ctx, "u", dev, phoneN(1), four); !errors.Is(err, ErrSMSCap) {
		t.Fatalf("got %v, want ErrSMSCap", err)
	}
	if usageCount(t, svc) != 0 || len(pub.calls) != 0 {
		t.Errorf("oversized message reserved %d / published %d", usageCount(t, svc), len(pub.calls))
	}
	two := strings.Repeat("a", 161) // 2 segments
	if _, err := svc.SendSMS(ctx, "u", dev, phoneN(2), two); err != nil {
		t.Fatal(err)
	}
	if got := usageCount(t, svc); got != 2 {
		t.Errorf("usage = %d, want 2", got)
	}
	if _, err := svc.SendSMS(ctx, "u", dev, phoneN(3), two); !errors.Is(err, ErrSMSCap) {
		t.Fatalf("got %v, want ErrSMSCap (1 left, 2 needed)", err)
	}
	if got := usageCount(t, svc); got != 2 {
		t.Errorf("failed reserve changed usage to %d", got)
	}
	if _, err := svc.SendSMS(ctx, "u", dev, phoneN(4), "x"); err != nil {
		t.Errorf("the remaining segment should still be usable: %v", err)
	}
}

func TestRateLimitedRequestDoesNotConsumeCap(t *testing.T) {
	svc, _, _ := newSvc(t, 1000)
	ctx := context.Background()
	for i := 0; i < smsPerMinute; i++ {
		if _, err := svc.SendSMS(ctx, "u", dev, phoneN(i), "x"); err != nil {
			t.Fatal(err)
		}
	}
	before := usageCount(t, svc)
	if _, err := svc.SendSMS(ctx, "u", dev, phoneN(99), "x"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("got %v", err)
	}
	if got := usageCount(t, svc); got != before {
		t.Errorf("rate-limited request consumed cap: %d -> %d", before, got)
	}
}

func TestRecipientLimit(t *testing.T) {
	svc, _, clk := newSvc(t, 1000)
	ctx := context.Background()
	to := "+972501234567"
	for i := 0; i < 3; i++ {
		if _, err := svc.SendSMS(ctx, "user"+fmt.Sprint(i), dev, to, "x"); err != nil {
			t.Fatal(err)
		}
	}
	before := usageCount(t, svc)
	if _, err := svc.SendSMS(ctx, "other", dev, to, "x"); !errors.Is(err, ErrRecipientLimited) {
		t.Fatalf("got %v, want ErrRecipientLimited", err)
	}
	if usageCount(t, svc) != before {
		t.Error("recipient-limited send consumed the cap")
	}
	if _, err := svc.SendSMS(ctx, "other", dev, "+972501234568", "x"); err != nil {
		t.Errorf("different recipient limited: %v", err)
	}
	clk.Add(61 * time.Minute)
	if _, err := svc.SendSMS(ctx, "other", dev, to, "x"); err != nil {
		t.Errorf("window did not slide: %v", err)
	}
}

func TestFailedSendDoesNotBurnRecipientSlot(t *testing.T) {
	svc, pub, _ := newSvc(t, 1000)
	ctx := context.Background()
	to := "+972501234567"
	pub.err = mqtt.ErrNotConnected
	for i := 0; i < 5; i++ {
		if _, err := svc.SendSMS(ctx, "u"+fmt.Sprint(i), dev, to, "x"); !errors.Is(err, ErrNotConnected) {
			t.Fatalf("got %v", err)
		}
	}
	pub.err = nil
	if _, err := svc.SendSMS(ctx, "v", dev, to, "x"); err != nil {
		t.Errorf("proven-unsent sends burned recipient slots: %v", err)
	}
}

func TestSMSCapConcurrent(t *testing.T) {
	svc, pub, _ := newSvc(t, 10)
	ctx := context.Background()
	two := strings.Repeat("a", 161) // 2 segments each: 5 of 40 fit
	var ok, capped atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// A distinct user and recipient per call keeps the other limits out of the way.
			_, err := svc.SendSMS(ctx, "user"+string(rune('A'+i)), dev, phoneN(i), two)
			switch {
			case err == nil:
				ok.Add(1)
			case errors.Is(err, ErrSMSCap):
				capped.Add(1)
			default:
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if ok.Load() != 5 || capped.Load() != 35 || len(pub.calls) != 5 || usageCount(t, svc) != 10 {
		t.Errorf("ok=%d capped=%d published=%d usage=%d", ok.Load(), capped.Load(), len(pub.calls), usageCount(t, svc))
	}
}

type failingUsage struct {
	SQLUsage
	reserveErr, releaseErr error
}

func (f failingUsage) Reserve(ctx context.Context, day string, n, cap int) (bool, error) {
	if f.reserveErr != nil {
		return false, f.reserveErr
	}
	return f.SQLUsage.Reserve(ctx, day, n, cap)
}

func (f failingUsage) Release(ctx context.Context, day string, n int) error {
	if f.releaseErr != nil {
		return f.releaseErr
	}
	return f.SQLUsage.Release(ctx, day, n)
}

func newSMSSvc(t *testing.T, usage UsageStore, cor *Correlator, buf *bytes.Buffer) (*Service, *fakePub) {
	t.Helper()
	pub := &fakePub{}
	cfg := Config{Prefixes: []string{"dradis"}, SMSDailyCap: 100, Now: newClock().Now, Usage: usage, Correlator: cor}
	if buf != nil {
		cfg.Log = slog.New(slog.NewTextHandler(buf, nil))
	}
	return NewService(pub, fakeDevs{dev: true}, cfg), pub
}

func TestPublishErrorContract(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		want        error
		wantRelease bool
	}{
		{"not connected", mqtt.ErrNotConnected, ErrNotConnected, true},
		{"wrapped not connected", fmt.Errorf("x: %w", mqtt.ErrNotConnected), ErrNotConnected, true},
		{"invalid topic", fmt.Errorf("%w: bad", mqtt.ErrInvalidTopic), ErrInvalid, true},
		{"not sent", fmt.Errorf("%w: %w", mqtt.ErrNotSent, context.Canceled), context.Canceled, true},
		{"timeout", fmt.Errorf("waiting: %w", context.DeadlineExceeded), ErrOutcomeUnknown, false},
		{"cancel after write", fmt.Errorf("waiting: %w", context.Canceled), ErrOutcomeUnknown, false},
		{"connection lost", fmt.Errorf("%w: %w", mqtt.ErrNotConnected, mqtt.ErrConnectionLost), ErrOutcomeUnknown, false},
		{"unknown token error", errors.New("boom"), ErrOutcomeUnknown, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []Outcome
			cor := NewCorrelator(func(o Outcome) { got = append(got, o) }, nil)
			svc, pub := newSMSSvc(t, SQLUsage{DB: openDB(t)}, cor, nil)
			pub.err = tt.err
			_, err := svc.SendSMS(context.Background(), "u", dev, "+972501234567", "x")
			if !errors.Is(err, tt.want) {
				t.Fatalf("got %v, want %v", err, tt.want)
			}
			if tt.want == ErrOutcomeUnknown && (errors.Is(err, ErrNotConnected) || errors.Is(err, ErrInvalid)) {
				t.Errorf("unknown outcome also matches a proven-unsent sentinel: %v", err)
			}
			wantUsage, wantPending := 1, 1
			if tt.wantRelease {
				wantUsage, wantPending = 0, 0
			}
			if u := usageCount(t, svc); u != wantUsage {
				t.Errorf("usage = %d, want %d", u, wantUsage)
			}
			if len(cor.pending) != wantPending {
				t.Errorf("pending = %d, want %d", len(cor.pending), wantPending)
			}
		})
	}
}

func TestNonSMSUnknownOutcome(t *testing.T) {
	svc, pub, _ := newSvc(t, 10)
	pub.err = context.DeadlineExceeded
	if _, err := svc.GetStatus(context.Background(), "u", dev); !errors.Is(err, ErrOutcomeUnknown) {
		t.Errorf("got %v", err)
	}
}

func TestCancelledContextSendsNothing(t *testing.T) {
	svc, pub, _ := newSvc(t, 10)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := svc.SendSMS(ctx, "u", dev, "+972501234567", "x"); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if len(pub.calls) != 0 || usageCount(t, svc) != 0 {
		t.Errorf("published %d, usage %d", len(pub.calls), usageCount(t, svc))
	}
}

func TestStoreFailureIsUnavailable(t *testing.T) {
	svc, pub := newSMSSvc(t, failingUsage{SQLUsage: SQLUsage{DB: openDB(t)}, reserveErr: errors.New("disk full")}, nil, nil)
	if _, err := svc.SendSMS(context.Background(), "u", dev, "+972501234567", "x"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("got %v", err)
	}
	if len(pub.calls) != 0 {
		t.Error("published after reserve failure")
	}
	svc, _ = newSMSSvc(t, nil, nil, nil)
	if _, err := svc.SendSMS(context.Background(), "u", dev, "+972501234567", "x"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("missing usage store: got %v", err)
	}
}

type errDevs struct{}

func (errDevs) Get(context.Context, string) (devices.Device, error) {
	return devices.Device{}, errors.New("db down")
}

func TestDeviceLookupFailureIsUnavailable(t *testing.T) {
	svc := NewService(&fakePub{}, errDevs{}, Config{Prefixes: []string{"dradis"}})
	if _, err := svc.GetStatus(context.Background(), "u", dev); !errors.Is(err, ErrUnavailable) {
		t.Errorf("got %v", err)
	}
}

func TestReleaseFailureIsLoggedAndCounted(t *testing.T) {
	var buf bytes.Buffer
	usage := failingUsage{SQLUsage: SQLUsage{DB: openDB(t)}, releaseErr: errors.New("db locked +972501234567")}
	svc, pub := newSMSSvc(t, usage, nil, &buf)
	pub.err = mqtt.ErrNotConnected
	if _, err := svc.SendSMS(context.Background(), "u", dev, "+972501234567", "secret text"); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("got %v", err)
	}
	if svc.ReleaseFailures() != 1 {
		t.Errorf("ReleaseFailures = %d", svc.ReleaseFailures())
	}
	out := buf.String()
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "release_failed") || !strings.Contains(out, dev) {
		t.Errorf("log = %q", out)
	}
	if strings.Contains(out, "972501234567") || strings.Contains(out, "secret text") {
		t.Errorf("log leaks phone or text: %q", out)
	}
}

func TestNotifyUnicodeAndBlank(t *testing.T) {
	svc, pub, _ := newSvc(t, 10)
	ctx := context.Background()
	bad := map[string]func() error{
		"title bidi override": func() error { _, err := svc.Notify(ctx, "u", dev, "a\u202eb", "x"); return err },
		"title zero width":    func() error { _, err := svc.Notify(ctx, "u", dev, "a\u200bb", "x"); return err },
		"title newline":       func() error { _, err := svc.Notify(ctx, "u", dev, "a\nb", "x"); return err },
		"title tab":           func() error { _, err := svc.Notify(ctx, "u", dev, "a\tb", "x"); return err },
		"title blank":         func() error { _, err := svc.Notify(ctx, "u", dev, "   ", "x"); return err },
		"text isolate":        func() error { _, err := svc.Notify(ctx, "u", dev, "t", "a\u2066b"); return err },
		"text BOM":            func() error { _, err := svc.Notify(ctx, "u", dev, "t", "\ufeffhello"); return err },
		"text zero width":     func() error { _, err := svc.Notify(ctx, "u", dev, "t", "a\u200db"); return err },
		"text blank":          func() error { _, err := svc.Notify(ctx, "u", dev, "t", " \n\t "); return err },
		"sms bidi":            func() error { _, err := svc.SendSMS(ctx, "u", dev, "+972501234567", "a\u202eb"); return err },
		"sms blank":           func() error { _, err := svc.SendSMS(ctx, "u", dev, "+972501234567", "   "); return err },
	}
	for name, f := range bad {
		if err := f(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: got %v, want ErrInvalid", name, err)
		}
	}
	if len(pub.calls) != 0 {
		t.Errorf("rejected input was published: %d", len(pub.calls))
	}
	if _, err := svc.Notify(ctx, "u", dev, "", "שלום\nworld"); err != nil {
		t.Errorf("empty title should default: %v", err)
	}
	if pub.calls[0].payload != `{"text":"שלום\nworld","title":"CIC"}` {
		t.Errorf("payload = %s", pub.calls[0].payload)
	}
}

func TestServiceRecordsSMSInCorrelator(t *testing.T) {
	pub := &fakePub{}
	clk := newClock()
	var got []Outcome
	cor := NewCorrelator(func(o Outcome) { got = append(got, o) }, clk.Now)
	svc := NewService(pub, fakeDevs{dev: true}, Config{
		Prefixes: []string{"dradis"}, SMSDailyCap: 5, Now: clk.Now, Usage: SQLUsage{DB: openDB(t)}, Correlator: cor,
	})
	res, err := svc.SendSMS(context.Background(), "u", dev, "+972501234567", "x")
	if err != nil {
		t.Fatal(err)
	}
	cor.Observe(smsEvent("p1", "+972501234567", true, ""))
	if len(got) != 1 || got[0].CommandID != res.ID || got[0].Status != OutcomeConfirmed {
		t.Errorf("got %+v", got)
	}
}
