package mqtt

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"

	"github.com/t0mer/cic/internal/config"
	"github.com/t0mer/cic/internal/store"
)

// startMosquitto runs a throwaway local broker on a free loopback port.
// The test fails instead of skipping when CIC_REQUIRE_MOSQUITTO=1.
func startMosquitto(t *testing.T) (int, *exec.Cmd) {
	t.Helper()
	bin, err := exec.LookPath("mosquitto")
	if err != nil {
		if os.Getenv("CIC_REQUIRE_MOSQUITTO") == "1" {
			t.Fatal("mosquitto not installed but CIC_REQUIRE_MOSQUITTO=1")
		}
		t.Skip("mosquitto not installed")
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	conf := filepath.Join(t.TempDir(), "m.conf")
	body := fmt.Sprintf("listener %d 127.0.0.1\nallow_anonymous true\npersistence false\n", port)
	if err := os.WriteFile(conf, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "-c", conf)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	for i := 0; i < 100; i++ {
		if c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port)); err == nil {
			c.Close()
			return port, cmd
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("mosquitto did not start")
	return 0, nil
}

// connectedClient returns a Client connected to a fresh local broker.
func connectedClient(t *testing.T) (*Client, int, *exec.Cmd, context.Context) {
	t.Helper()
	port, cmd := startMosquitto(t)
	ctx, cancel := context.WithCancel(context.Background())
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "cic.db"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.MQTTConfig{Prefixes: []string{"dradis"}, Broker: config.BrokerConfig{Host: "127.0.0.1", Port: port}}
	c, err := New(ctx, cfg, db, nopSink{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done; db.Close() })
	deadline := time.Now().Add(10 * time.Second)
	for !c.Connected() {
		if time.Now().After(deadline) {
			t.Fatal("client did not connect")
		}
		time.Sleep(20 * time.Millisecond)
	}
	return c, port, cmd, ctx
}

func TestPublishCancelledContextSendsNothing(t *testing.T) {
	c, port, _, _ := connectedClient(t)
	msgs := make(chan string, 4)
	sub := paho.NewClient(paho.NewClientOptions().AddBroker(fmt.Sprintf("tcp://127.0.0.1:%d", port)).SetClientID("test-sub-c"))
	if tok := sub.Connect(); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatal("subscriber connect failed")
	}
	defer sub.Disconnect(100)
	sub.Subscribe("dradis/p1/+", 1, func(_ paho.Client, m paho.Message) { msgs <- m.Topic() }).WaitTimeout(5 * time.Second)

	cctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := c.Publish(cctx, "dradis/p1/ping", []byte("{}"))
	if !errors.Is(err, ErrNotSent) || !errors.Is(err, context.Canceled) || !NotSent(err) {
		t.Fatalf("got %v", err)
	}
	select {
	case m := <-msgs:
		t.Fatalf("cancelled publish reached the broker: %s", m)
	case <-time.After(500 * time.Millisecond):
	}
}

func TestMapTokenError(t *testing.T) {
	if mapTokenError(nil) != nil {
		t.Error("nil should map to nil")
	}
	err := mapTokenError(paho.ErrNotConnected)
	if !errors.Is(err, ErrNotConnected) || !NotSent(err) {
		t.Errorf("paho not connected: %v", err)
	}
	err = mapTokenError(errors.New("connection lost before Publish completed"))
	if !errors.Is(err, ErrNotConnected) || !errors.Is(err, ErrConnectionLost) || NotSent(err) {
		t.Errorf("connection lost: %v (NotSent=%v)", err, NotSent(err))
	}
	err = mapTokenError(errors.New("boom"))
	if errors.Is(err, ErrNotConnected) || NotSent(err) {
		t.Errorf("unknown error: %v", err)
	}
}

func TestPublishInvalidTopicIsNotSent(t *testing.T) {
	c := &Client{cfg: config.MQTTConfig{Prefixes: []string{"dradis"}}}
	for _, bad := range []string{"ping", "dradis/p1/status", "other/p1/ping"} {
		if err := c.Publish(context.Background(), bad, nil); !errors.Is(err, ErrInvalidTopic) || !NotSent(err) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestPublishLocalBroker(t *testing.T) {
	port, _ := startMosquitto(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "cic.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := config.MQTTConfig{Prefixes: []string{"dradis"}, Broker: config.BrokerConfig{Host: "127.0.0.1", Port: port}}
	c, err := New(ctx, cfg, db, nopSink{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Publish(ctx, "dradis/p1/ping", []byte(`{"seconds":1}`)); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("before connect: %v", err)
	}

	type got struct {
		topic    string
		payload  string
		retained bool
	}
	msgs := make(chan got, 16)
	sub := paho.NewClient(paho.NewClientOptions().AddBroker(fmt.Sprintf("tcp://127.0.0.1:%d", port)).SetClientID("test-sub"))
	if tok := sub.Connect(); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatal("subscriber connect failed")
	}
	defer sub.Disconnect(100)
	if tok := sub.Subscribe("dradis/p1/+", 1, func(_ paho.Client, m paho.Message) {
		msgs <- got{m.Topic(), string(m.Payload()), m.Retained()}
	}); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatal("subscribe failed")
	}

	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(10 * time.Second)
	for !c.Connected() {
		if time.Now().After(deadline) {
			t.Fatal("client did not connect")
		}
		time.Sleep(20 * time.Millisecond)
	}

	b := NewBuilder(cfg.Prefixes)
	cases := []struct{ leaf, payload string }{
		{CmdPing, `{"seconds":30}`},
		{CmdNotify, `{"title":"CIC","text":"hi"}`},
		{CmdSendSMS, `{"phone":"+972501234567","text":"x"}`},
		{CmdGetStatus, `{}`},
		{CmdGetLocation, `{}`},
	}
	for _, tc := range cases {
		topic, err := b.Command("dradis", "p1", tc.leaf)
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Publish(ctx, topic, []byte(tc.payload)); err != nil {
			t.Fatalf("%s: %v", tc.leaf, err)
		}
		select {
		case m := <-msgs:
			if m.topic != topic || m.payload != tc.payload || m.retained {
				t.Errorf("%s: got %+v", tc.leaf, m)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: no message", tc.leaf)
		}
	}

	// Nothing may have been stored as retained: a late subscriber sees nothing.
	late := make(chan string, 4)
	sub2 := paho.NewClient(paho.NewClientOptions().AddBroker(fmt.Sprintf("tcp://127.0.0.1:%d", port)).SetClientID("test-late"))
	if tok := sub2.Connect(); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatal("late subscriber connect failed")
	}
	defer sub2.Disconnect(100)
	sub2.Subscribe("dradis/p1/+", 1, func(_ paho.Client, m paho.Message) { late <- m.Topic() }).WaitTimeout(5 * time.Second)
	select {
	case topic := <-late:
		t.Errorf("retained message found on %s", topic)
	case <-time.After(500 * time.Millisecond):
	}

	for _, bad := range []string{"dradis/p1/status", "other/p1/ping", "dradis/+/ping", "dradis/p1/ping/x", "ping"} {
		if err := c.Publish(ctx, bad, []byte("{}")); err == nil {
			t.Errorf("Publish(%q) should fail", bad)
		}
	}
}

type nopSink struct{}

func (nopSink) Handle(Event) {}
