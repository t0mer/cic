package mqtt

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
	"github.com/eclipse/paho.mqtt.golang/packets"

	"github.com/t0mer/cic/internal/config"
	"github.com/t0mer/cic/internal/ratelog"
	"github.com/t0mer/cic/internal/store"
)

type fakeMsg struct {
	topic    string
	payload  string
	retained bool
}

func (m fakeMsg) Duplicate() bool   { return false }
func (m fakeMsg) Qos() byte         { return 1 }
func (m fakeMsg) Retained() bool    { return m.retained }
func (m fakeMsg) Topic() string     { return m.topic }
func (m fakeMsg) MessageID() uint16 { return 0 }
func (m fakeMsg) Payload() []byte   { return []byte(m.payload) }
func (m fakeMsg) Ack()              {}

var _ paho.Message = fakeMsg{}

func testClient(t *testing.T, cfg config.MQTTConfig, buf io.Writer) *Client {
	t.Helper()
	if buf == nil {
		buf = io.Discard
	}
	if cfg.Prefixes == nil {
		cfg.Prefixes = []string{"dradis"}
	}
	return &Client{
		cfg:      cfg,
		log:      slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})),
		queue:    newEventQueue(eventQueueSize),
		track:    newConnTracker(),
		dropWarn: ratelog.Limiter{Interval: 10 * time.Second},
	}
}

func TestOnMessageRetainedFiltering(t *testing.T) {
	tests := []struct {
		leaf     string
		payload  string
		retained bool
		want     bool
	}{
		{"status", "1", true, true},
		{"version", "1.0", true, true},
		{"battery", `{"battery_level":5}`, true, false},
		{"wifi", `{"connected":true}`, true, false},
		{"location", `{"latitude":1,"longitude":2}`, true, false},
		{"device_info", `{"device_info":"x"}`, true, false},
		{"sendsms/result", `{"ok":true}`, true, false},
		{"battery", `{"battery_level":5}`, false, true},
		{"battery", `bad`, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.leaf+map[bool]string{true: "/retained", false: "/live"}[tt.retained], func(t *testing.T) {
			c := testClient(t, config.MQTTConfig{}, nil)
			c.onMessage(nil, fakeMsg{"dradis/phone/" + tt.leaf, tt.payload, tt.retained})
			ev, got := c.queue.pop()
			if got != tt.want {
				t.Fatalf("queued = %v, want %v", got, tt.want)
			}
			if got && (ev.Leaf != tt.leaf || ev.Retained != tt.retained || ev.Device != "phone") {
				t.Errorf("event = %+v", ev)
			}
		})
	}
}

func TestQueueOverflow(t *testing.T) {
	status := Event{Leaf: LeafStatus}
	bat := func(n int) Event { return Event{Leaf: LeafBattery, Device: string(rune('a' + n))} }

	t.Run("drops oldest telemetry, keeps status", func(t *testing.T) {
		q := newEventQueue(3)
		q.push(status)
		q.push(bat(0))
		q.push(bat(1))
		if !q.push(bat(2)) {
			t.Error("push into a full queue must report a drop")
		}
		var got []string
		for ev, ok := q.pop(); ok; ev, ok = q.pop() {
			got = append(got, ev.Leaf+ev.Device)
		}
		if strings.Join(got, ",") != "status,batteryb,batteryc" {
			t.Errorf("queue = %v", got)
		}
	})
	t.Run("status never dropped", func(t *testing.T) {
		q := newEventQueue(2)
		for range 5 {
			q.push(status)
		}
		q.push(Event{Leaf: LeafVersion})
		if !q.push(bat(0)) {
			t.Error("telemetry must be dropped when only priority events fill the queue")
		}
		n := 0
		for _, ok := q.pop(); ok; _, ok = q.pop() {
			n++
		}
		if n != 6 {
			t.Errorf("kept %d events, want 6", n)
		}
	})
	t.Run("status evicts telemetry when full", func(t *testing.T) {
		q := newEventQueue(2)
		q.push(bat(0))
		q.push(bat(1))
		q.push(status)
		first, _ := q.pop()
		second, _ := q.pop()
		if first.Device != "b" || second.Leaf != LeafStatus {
			t.Errorf("got %+v then %+v", first, second)
		}
	})
	t.Run("next wakes on push and stops on cancel", func(t *testing.T) {
		q := newEventQueue(2)
		ctx, cancel := context.WithCancel(context.Background())
		go func() { time.Sleep(20 * time.Millisecond); q.push(status) }()
		if ev, ok := q.next(ctx); !ok || ev.Leaf != LeafStatus {
			t.Fatalf("next = %+v, %v", ev, ok)
		}
		cancel()
		if _, ok := q.next(ctx); ok {
			t.Error("next must stop after cancel")
		}
	})
}

func TestEnqueueLogsDropsRateLimited(t *testing.T) {
	var buf bytes.Buffer
	c := testClient(t, config.MQTTConfig{}, &buf)
	c.queue = newEventQueue(1)
	now := time.Unix(0, 0)
	c.dropWarn = ratelog.Limiter{Interval: 10 * time.Second, Now: func() time.Time { return now }}
	for range 5 {
		c.enqueue(Event{Leaf: LeafBattery})
	}
	if n := strings.Count(buf.String(), "dropped telemetry"); n != 1 {
		t.Errorf("warnings = %d, want 1 within the interval\n%s", n, buf.String())
	}
	now = now.Add(11 * time.Second)
	c.enqueue(Event{Leaf: LeafBattery})
	if n := strings.Count(buf.String(), "dropped telemetry"); n != 2 || !strings.Contains(buf.String(), "dropped_total=5") {
		t.Errorf("warnings = %d\n%s", n, buf.String())
	}
}

func TestSubscribeAllRetries(t *testing.T) {
	filters := []string{"a/+/status", "b/+/status", "c/+/status"}
	tests := []struct {
		name      string
		failFirst map[string]int
		cancelAt  int
		wantErr   bool
		wantSubs  map[string]int
		wantFails int
		wantSleep []time.Duration
	}{
		{"all succeed", nil, -1, false, map[string]int{"a/+/status": 1, "b/+/status": 1, "c/+/status": 1}, 0, nil},
		{"one filter retried only", map[string]int{"b/+/status": 2}, -1, false, map[string]int{"a/+/status": 1, "b/+/status": 3, "c/+/status": 1}, 2,
			[]time.Duration{5 * time.Second, 10 * time.Second}},
		{"backoff capped", map[string]int{"a/+/status": 6}, -1, false, map[string]int{"a/+/status": 7, "b/+/status": 1, "c/+/status": 1}, 6,
			[]time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second, 40 * time.Second, 60 * time.Second, 60 * time.Second}},
		{"cancelled while failing", map[string]int{"a/+/status": 99}, 1, true, map[string]int{"a/+/status": 2, "b/+/status": 1, "c/+/status": 1}, 2,
			[]time.Duration{5 * time.Second, 10 * time.Second}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			subs := map[string]int{}
			fails := 0
			var slept []time.Duration
			err := subscribeAll(ctx, filters,
				func(f string) error {
					subs[f]++
					if subs[f] <= tt.failFirst[f] {
						return errors.New("boom")
					}
					return nil
				},
				func(string, error) { fails++ },
				func(_ context.Context, d time.Duration) bool {
					slept = append(slept, d)
					if tt.cancelAt >= 0 && len(slept) > tt.cancelAt {
						cancel()
						return false
					}
					return true
				})
			if (err != nil) != tt.wantErr {
				t.Errorf("err = %v", err)
			}
			if fails != tt.wantFails {
				t.Errorf("fails = %d, want %d", fails, tt.wantFails)
			}
			for f, n := range tt.wantSubs {
				if subs[f] != n {
					t.Errorf("subscribe %s = %d, want %d", f, subs[f], n)
				}
			}
			if len(slept) != len(tt.wantSleep) {
				t.Fatalf("sleeps = %v, want %v", slept, tt.wantSleep)
			}
			for i := range slept {
				if slept[i] != tt.wantSleep[i] {
					t.Errorf("sleeps = %v, want %v", slept, tt.wantSleep)
					break
				}
			}
		})
	}
}

func TestRetryDelay(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		cur      time.Duration
		wantWait time.Duration
		wantNext time.Duration
		wantAuth bool
	}{
		{"first failure", errors.New("dial"), time.Second, time.Second, 2 * time.Second, false},
		{"capped", errors.New("dial"), 16 * time.Second, 16 * time.Second, 20 * time.Second, false},
		{"stays capped", errors.New("dial"), 20 * time.Second, 20 * time.Second, 20 * time.Second, false},
		{"bad credentials", packets.ErrorRefusedBadUsernameOrPassword, 8 * time.Second, 60 * time.Second, time.Second, true},
		{"not authorised", packets.ErrorRefusedNotAuthorised, time.Second, 60 * time.Second, time.Second, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wait, next, auth := retryDelay(tt.err, tt.cur)
			if wait != tt.wantWait || next != tt.wantNext || auth != tt.wantAuth {
				t.Errorf("got %v %v %v", wait, next, auth)
			}
		})
	}
}

func TestConnTrackerIgnoresStaleCallbacks(t *testing.T) {
	t.Run("late connect cannot overwrite lost", func(t *testing.T) {
		tr := newConnTracker()
		gen := tr.begin()
		tr.lost("connection reset")
		if tr.connected(gen) {
			t.Error("stale connected() must be rejected")
		}
		if s := tr.snapshot(); s.Connected || s.LastError != "connection reset" {
			t.Errorf("state = %+v", s)
		}
	})
	t.Run("late failure cannot overwrite newer connect", func(t *testing.T) {
		tr := newConnTracker()
		old := tr.begin()
		cur := tr.begin()
		if !tr.connected(cur) {
			t.Fatal("current generation must connect")
		}
		if tr.fail(old, "subscribe failed") {
			t.Error("stale fail() must be rejected")
		}
		if s := tr.snapshot(); !s.Connected || s.LastError != "" {
			t.Errorf("state = %+v", s)
		}
	})
	t.Run("connect failure applies unconditionally", func(t *testing.T) {
		tr := newConnTracker()
		tr.connected(tr.begin())
		tr.fail(0, "connect failed")
		if s := tr.snapshot(); s.Connected || s.LastError != "connect failed" {
			t.Errorf("state = %+v", s)
		}
	})
	t.Run("concurrent callbacks end consistent", func(t *testing.T) {
		tr := newConnTracker()
		var wg sync.WaitGroup
		for range 50 {
			wg.Add(2)
			go func() { defer wg.Done(); tr.connected(tr.begin()) }()
			go func() { defer wg.Done(); tr.lost("x") }()
		}
		wg.Wait()
		_ = tr.snapshot()
	})
}

func writeCert(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTLSConfig(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "bad.pem")
	if err := os.WriteFile(bad, []byte("not a pem"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		broker   config.BrokerConfig
		wantErr  string
		wantRoot bool
		wantSkip bool
	}{
		{"system roots", config.BrokerConfig{Host: "h"}, "", false, false},
		{"valid ca file", config.BrokerConfig{Host: "h", CAFile: writeCert(t)}, "", true, false},
		{"bad pem", config.BrokerConfig{Host: "h", CAFile: bad}, "no valid certificates", false, false},
		{"missing file", config.BrokerConfig{Host: "h", CAFile: filepath.Join(t.TempDir(), "none.pem")}, "reading mqtt ca_file", false, false},
		{"insecure skip verify warns", config.BrokerConfig{Host: "h", InsecureSkipVerify: true}, "", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			c := testClient(t, config.MQTTConfig{Broker: tt.broker}, &buf)
			tc, err := c.tlsConfig()
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if (tc.RootCAs != nil) != tt.wantRoot || tc.InsecureSkipVerify != tt.wantSkip || tc.ServerName != "h" {
				t.Errorf("tls config = %+v", tc)
			}
			if warned := strings.Contains(buf.String(), "verification is disabled"); warned != tt.wantSkip {
				t.Errorf("warned = %v, want %v", warned, tt.wantSkip)
			}
		})
	}
}

func TestClientIDPersists(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "cic.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := config.MQTTConfig{Prefixes: []string{"dradis"}, Broker: config.BrokerConfig{Host: "127.0.0.1", Port: 1883}}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	first, err := New(ctx, cfg, db, nil, log)
	if err != nil {
		t.Fatal(err)
	}
	second, err := New(ctx, cfg, db, nil, log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(first.ClientID(), "cic-") || len(first.ClientID()) != len("cic-")+16 || first.ClientID() != second.ClientID() {
		t.Errorf("client ids = %q, %q", first.ClientID(), second.ClientID())
	}
	stored, err := store.GetMeta(ctx, db, clientIDMetaKey)
	if err != nil || stored != first.ClientID() {
		t.Errorf("stored id = %q, %v", stored, err)
	}
}
