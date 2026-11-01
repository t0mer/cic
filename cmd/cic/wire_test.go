package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"

	"github.com/t0mer/cic/internal/api"
	"github.com/t0mer/cic/internal/audit"
	"github.com/t0mer/cic/internal/auth"
	"github.com/t0mer/cic/internal/commands"
	"github.com/t0mer/cic/internal/config"
	"github.com/t0mer/cic/internal/devices"
	"github.com/t0mer/cic/internal/events"
	"github.com/t0mer/cic/internal/mqtt"
	"github.com/t0mer/cic/internal/store"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "w.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// testCorrelator returns a correlator wired like production; the relay is closed on cleanup.
func testCorrelator(t *testing.T, hub *events.Hub, aud *audit.Store, now func() time.Time) *commands.Correlator {
	t.Helper()
	relay := newOutcomeRelay(hub, aud, quiet)
	t.Cleanup(relay.Close)
	return newCorrelator(relay, now)
}

func waitStatus(t *testing.T, a *audit.Store, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if st := auditStatus(t, a); st == want {
			return
		} else if time.Now().After(deadline) {
			t.Fatalf("audit status %q, want %q", st, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func auditStatus(t *testing.T, a *audit.Store) string {
	t.Helper()
	p, err := a.List(context.Background(), 50, 0)
	if err != nil || len(p.Items) == 0 {
		t.Fatalf("audit list: %v %v", p, err)
	}
	return p.Items[0].Status
}

func recvEvent(t *testing.T, ch <-chan events.Event) events.Event {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("hub channel closed")
		}
		return ev
	case <-time.After(3 * time.Second):
		t.Fatal("no event")
	}
	return events.Event{}
}

func TestSMSResultPublishesEventAndUpdatesAudit(t *testing.T) {
	hub := events.NewHub()
	t.Cleanup(hub.Close)
	aud := audit.New(openDB(t), nil)
	corr := testCorrelator(t, hub, aud, nil)
	ctx := context.Background()
	ch, cancel := hub.Subscribe()
	defer cancel()

	_ = aud.Log(ctx, audit.Entry{CommandID: "c1", User: "u", Device: "dradis/a", Action: "sms", Status: "sent"})
	corr.Record("c1", "dradis/a", "+972501234567")
	corr.Observe(mqtt.Event{Prefix: "dradis", Device: "a", Leaf: mqtt.LeafSMSResult,
		Payload: mqtt.SMSResult{Phone: "+972501234567", OK: false, Error: "radio off, called 0501234567"}})

	ev := recvEvent(t, ch)
	if ev.Type != events.TypeCommandResult {
		t.Fatalf("type %q", ev.Type)
	}
	b, _ := json.Marshal(ev.Data)
	var got map[string]any
	_ = json.Unmarshal(b, &got)
	if got["id"] != "c1" || got["action"] != "sms" || got["device"] != "dradis/a" || got["status"] != "failed" || got["phone_masked"] != "+*********567" {
		t.Fatalf("event %s", b)
	}
	if strings.Contains(string(b), "501234567") && !strings.Contains(string(b), "***") {
		t.Fatalf("raw phone leaked: %s", b)
	}
	if strings.Contains(string(b), "972501234567") || strings.Contains(string(b), "0501234567") {
		t.Fatalf("raw phone leaked: %s", b)
	}
	waitStatus(t, aud, "failed")
}

type fakeClock struct{ ns atomic.Int64 }

func (c *fakeClock) now() time.Time      { return time.Unix(0, c.ns.Load()).UTC() }
func (c *fakeClock) add(d time.Duration) { c.ns.Add(int64(d)) }

func TestSweepTickerReportsNoConfirmation(t *testing.T) {
	hub := events.NewHub()
	t.Cleanup(hub.Close)
	aud := audit.New(openDB(t), nil)
	clk := &fakeClock{}
	clk.ns.Store(time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC).UnixNano())
	corr := testCorrelator(t, hub, aud, clk.now)
	ch, cancel := hub.Subscribe()
	defer cancel()
	_ = aud.Log(context.Background(), audit.Entry{CommandID: "c9", User: "u", Device: "dradis/a", Action: "sms", Status: "sent"})
	corr.Record("c9", "dradis/a", "+972501234567")

	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { runSweeper(ctx, corr, 5*time.Millisecond, clk.now); close(done) }()

	time.Sleep(30 * time.Millisecond) // sweeps run, but 60 s have not passed
	select {
	case ev := <-ch:
		t.Fatalf("early outcome: %+v", ev)
	default:
	}
	clk.add(61 * time.Second)
	ev := recvEvent(t, ch)
	b, _ := json.Marshal(ev.Data)
	if !strings.Contains(string(b), `"status":"no_confirmation"`) || !strings.Contains(string(b), `"phone_masked":"+*********567"`) {
		t.Fatalf("event %s", b)
	}
	waitStatus(t, aud, "no_confirmation")
	stop()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("sweeper did not stop")
	}
}

func TestFanoutRunsAllSinks(t *testing.T) {
	var n int
	f := fanoutSink{sinks: []mqtt.Sink{sinkFn(func(mqtt.Event) { n++ }), sinkFn(func(mqtt.Event) { n += 10 })}, log: quiet}
	f.Handle(mqtt.Event{})
	if n != 11 {
		t.Fatalf("n=%d", n)
	}
}

type sinkFn func(mqtt.Event)

func (f sinkFn) Handle(ev mqtt.Event) { f(ev) }

func TestNoBrokerCommandsAreNotConnected(t *testing.T) {
	svc := commands.NewService(noBroker{}, stubLookup{}, commands.Config{Prefixes: []string{"dradis"}, PingSeconds: 20})
	if _, err := svc.StopPing(context.Background(), "1", "dradis/a"); err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("err %v", err)
	}
}

type stubLookup struct{}

func (stubLookup) Get(context.Context, string) (devices.Device, error) {
	return devices.Device{Key: "dradis/a"}, nil
}

// ---- end to end against a LOCAL mosquitto ----

func startMosquitto(t *testing.T) int {
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
	if err := os.WriteFile(conf, []byte(fmt.Sprintf("listener %d 127.0.0.1\nallow_anonymous true\npersistence false\n", port)), 0o600); err != nil {
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
			return port
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("mosquitto did not start")
	return 0
}

type received struct {
	topic   string
	payload string
	retain  bool
}

func TestEndToEndCommandsOverLocalBroker(t *testing.T) {
	port := startMosquitto(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db := openDB(t)

	// A separate subscriber plays the phone.
	got := make(chan received, 16)
	opts := paho.NewClientOptions().AddBroker(fmt.Sprintf("tcp://127.0.0.1:%d", port)).SetClientID("test-phone")
	phone := paho.NewClient(opts)
	if tok := phone.Connect(); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatalf("phone connect: %v", tok.Error())
	}
	defer phone.Disconnect(100)
	if tok := phone.Subscribe("dradis/a/+", 1, func(_ paho.Client, m paho.Message) {
		got <- received{m.Topic(), string(m.Payload()), m.Retained()}
	}); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatalf("subscribe: %v", tok.Error())
	}

	registry := devices.NewRegistry(db, time.Minute, quiet)
	hub := events.NewHub()
	defer hub.Close()
	aud := audit.New(db, nil)
	corr := testCorrelator(t, hub, aud, nil)
	cfg := config.MQTTConfig{Prefixes: []string{"dradis"}, Broker: config.BrokerConfig{Host: "127.0.0.1", Port: port}}
	client, err := mqtt.New(ctx, cfg, db, fanoutSink{sinks: []mqtt.Sink{registry, observerSink{corr}}, log: quiet}, quiet)
	if err != nil {
		t.Fatal(err)
	}
	go client.Run(ctx)
	for deadline := time.Now().Add(10 * time.Second); !client.Connected(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("cic client did not connect")
		}
	}
	// The device must exist in the registry for commands to be accepted.
	if err := registry.Apply(ctx, mqtt.Event{Prefix: "dradis", Device: "a", Leaf: mqtt.LeafStatus, Payload: mqtt.Status{Online: true}, ReceivedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	authSvc := auth.NewService(auth.Config{DB: db, Log: quiet, InsecureCookies: true})
	if _, err := authSvc.Store().CreateUser(ctx, "boss", "correct horse battery", auth.RoleAdmin, false); err != nil {
		t.Fatal(err)
	}
	svc := commands.NewService(client, registry, commands.Config{Prefixes: []string{"dradis"}, PingSeconds: 20, SMSDailyCap: 10,
		Log: quiet, Usage: commands.SQLUsage{DB: db}, Correlator: corr})
	d := api.Deps{Devices: registry, Broker: client, DB: db, Hub: hub, Log: quiet, Auth: authSvc, Commands: svc, Audit: aud}
	srv := httptest.NewServer(newRouter(d, "", nil))
	defer srv.Close()

	c := &http.Client{Jar: newJar(t)}
	resp, body := req(t, c, "POST", srv.URL+"/api/auth/login", `{"username":"boss","password":"correct horse battery"}`, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("login %d %s", resp.StatusCode, body)
	}
	var login struct {
		CSRF string `json:"csrf_token"`
	}
	_ = json.Unmarshal([]byte(body), &login)
	hdr := map[string]string{auth.CSRFHeader: login.CSRF, "Content-Type": "application/json"}

	// SSE stream, opened before the SMS result is produced.
	sseReq, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/events", nil)
	sseResp, err := c.Do(sseReq)
	if err != nil {
		t.Fatal(err)
	}
	defer sseResp.Body.Close()
	lines := make(chan string, 64)
	go func() {
		sc := bufio.NewScanner(sseResp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()

	wait := func() received {
		select {
		case r := <-got:
			return r
		case <-time.After(5 * time.Second):
			t.Fatal("phone received nothing")
		}
		return received{}
	}

	// ping
	resp, body = req(t, c, "POST", srv.URL+"/api/devices/dradis/a/ping", `{"seconds":25}`, hdr)
	if resp.StatusCode != 202 || !strings.Contains(body, `"action":"ping"`) || !strings.Contains(body, `"device":"dradis/a"`) {
		t.Fatalf("ping %d %s", resp.StatusCode, body)
	}
	if r := wait(); r.topic != "dradis/a/ping" || r.payload != `{"seconds":25}` || r.retain {
		t.Fatalf("ping received %+v", r)
	}
	// ping with no body uses the configured default
	if resp, body = req(t, c, "POST", srv.URL+"/api/devices/dradis/a/ping", "", hdr); resp.StatusCode != 202 {
		t.Fatalf("ping default %d %s", resp.StatusCode, body)
	}
	if r := wait(); r.payload != `{"seconds":20}` {
		t.Fatalf("ping default received %+v", r)
	}

	// sms
	phoneNo, text := "+972501234567", "Please call me back as soon as you can, thanks"
	resp, body = req(t, c, "POST", srv.URL+"/api/devices/dradis/a/sms", fmt.Sprintf(`{"phone":%q,"text":%q}`, phoneNo, text), hdr)
	if resp.StatusCode != 202 || !strings.Contains(body, `"action":"sms"`) {
		t.Fatalf("sms %d %s", resp.StatusCode, body)
	}
	var acc struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal([]byte(body), &acc)
	if r := wait(); r.topic != "dradis/a/sendsms" || r.retain || !strings.Contains(r.payload, phoneNo) {
		t.Fatalf("sms received %+v", r)
	}

	resp, body = req(t, c, "GET", srv.URL+"/api/activity", "", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("activity %d %s", resp.StatusCode, body)
	}
	if strings.Contains(body, phoneNo) || strings.Contains(body, "call me back as soon") || !strings.Contains(body, `phone=+*********567 len=46 \"Please call me back \"`) {
		t.Fatalf("activity not masked: %s", body)
	}
	var rows int
	_ = db.QueryRow(`SELECT count(*) FROM audit_log WHERE detail LIKE '%501234567%' OR detail LIKE '%as soon%'`).Scan(&rows)
	if rows != 0 {
		t.Fatal("full phone/text in audit_log")
	}

	// The phone reports the result; SSE and audit follow.
	tok := phone.Publish("dradis/a/sendsms/result", 1, false, fmt.Sprintf(`{"phone":%q,"ok":true}`, phoneNo))
	if !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatal(tok.Error())
	}
	deadline := time.After(5 * time.Second)
	var sawResult bool
	for !sawResult {
		select {
		case l := <-lines:
			if strings.HasPrefix(l, "data: ") && strings.Contains(l, `"status":"confirmed_ok"`) {
				if !strings.Contains(l, acc.ID) || !strings.Contains(l, `"phone_masked":"+*********567"`) || strings.Contains(l, "501234567") {
					t.Fatalf("sse data %s", l)
				}
				sawResult = true
			}
		case <-deadline:
			t.Fatal("no command.result over SSE")
		}
	}
	var st string
	_ = db.QueryRow(`SELECT status FROM audit_log WHERE command_id = ?`, acc.ID).Scan(&st)
	if st != "confirmed_ok" {
		t.Fatalf("audit status %q", st)
	}
}

func TestFanoutRecoversPerSink(t *testing.T) {
	var buf strings.Builder
	log := slog.New(slog.NewTextHandler(&buf, nil))
	var after int
	f := fanoutSink{sinks: []mqtt.Sink{
		sinkFn(func(mqtt.Event) { panic("secret-payload-+972501234567") }),
		sinkFn(func(mqtt.Event) { after++ }),
	}, log: log}
	f.Handle(mqtt.Event{})
	if after != 1 {
		t.Fatal("a panicking sink skipped the next sink")
	}
	out := buf.String()
	if !strings.Contains(out, "level=ERROR") || strings.Contains(out, "secret-payload") || strings.Contains(out, "972501234567") {
		t.Fatalf("log: %s", out)
	}
}

func TestOutcomeRelayNeverBlocksAndCountsOverflow(t *testing.T) {
	var buf strings.Builder
	// No consumer goroutine: the queue fills and Deliver must drop, not block.
	o := &outcomeRelay{log: slog.New(slog.NewTextHandler(&buf, nil)), ch: make(chan commands.Outcome, 2), done: make(chan struct{})}
	finished := make(chan struct{})
	go func() {
		for i := 0; i < 10; i++ {
			o.Deliver(commands.Outcome{CommandID: fmt.Sprint("c", i), Status: "failed", Error: "x"})
		}
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("Deliver blocked")
	}
	if o.Dropped() != 8 || !strings.Contains(buf.String(), "level=ERROR") {
		t.Fatalf("dropped %d log %s", o.Dropped(), buf.String())
	}
}

func TestOutcomeRelayCloseDrainsAndIsIdempotent(t *testing.T) {
	hub := events.NewHub()
	t.Cleanup(hub.Close)
	aud := audit.New(openDB(t), nil)
	relay := newOutcomeRelay(hub, aud, quiet)
	ctx := context.Background()
	var ids []int64
	for i := 0; i < 20; i++ {
		id, _ := aud.Begin(ctx, audit.Entry{User: "u", Device: "d/a", Action: "sms"})
		_ = aud.Finish(ctx, id, "sent", fmt.Sprint("c", i))
		ids = append(ids, id)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			relay.Deliver(commands.Outcome{CommandID: fmt.Sprint("c", i), Status: "confirmed_ok"})
		}()
	}
	wg.Wait()
	relay.Close()
	relay.Close()
	p, _ := aud.List(ctx, 50, 0)
	for _, it := range p.Items {
		if it.Status != "confirmed_ok" {
			t.Fatalf("outcome not drained before Close returned: %+v", it)
		}
	}
	done := make(chan struct{})
	go func() { relay.Deliver(commands.Outcome{CommandID: "late"}); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Deliver after Close blocked")
	}
}

func TestUnknownOutcomeSMSThenSweepUpdatesAuditAndEvent(t *testing.T) {
	hub := events.NewHub()
	t.Cleanup(hub.Close)
	aud := audit.New(openDB(t), nil)
	clk := &fakeClock{}
	clk.ns.Store(time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC).UnixNano())
	corr := testCorrelator(t, hub, aud, clk.now)
	ch, cancel := hub.Subscribe()
	defer cancel()
	ctx := context.Background()

	// The service published, the broker did not confirm: the row is "unknown" but linked.
	rowID, _ := aud.Begin(ctx, audit.Entry{User: "u", Device: "dradis/a", Action: "sms"})
	corr.Record("u1", "dradis/a", "+972501234567")
	_ = aud.Finish(ctx, rowID, audit.StatusUnknown, "u1")
	if st := auditStatus(t, aud); st != "unknown" {
		t.Fatalf("status %q", st)
	}
	clk.add(61 * time.Second)
	corr.Sweep(clk.now())
	ev := recvEvent(t, ch)
	b, _ := json.Marshal(ev.Data)
	if !strings.Contains(string(b), `"status":"no_confirmation"`) {
		t.Fatalf("event %s", b)
	}
	waitStatus(t, aud, "no_confirmation")
}

func TestStartHistoryPurgesWhileRecordingDisabled(t *testing.T) {
	db := openDB(t)
	old := time.Now().Add(-48 * time.Hour).Unix()
	if _, err := db.Exec(`INSERT INTO location_points(device_key, ts, lat, lon) VALUES ('dradis/p', ?, 1, 1)`, old); err != nil {
		t.Fatal(err)
	}
	reg := devices.NewRegistry(db, time.Minute, quiet)
	ctx, cancel := context.WithCancel(context.Background())
	wait := startHistory(ctx, db, reg, config.LocationsConfig{Enabled: false, Retention: 24 * time.Hour}, quiet)
	deadline := time.Now().Add(5 * time.Second)
	n := 1
	for n != 0 && time.Now().Before(deadline) {
		db.QueryRow(`SELECT COUNT(*) FROM location_points`).Scan(&n)
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	wait()
	if n != 0 {
		t.Fatal("retention janitor did not run with recording disabled")
	}
}
