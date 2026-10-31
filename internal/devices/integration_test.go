package devices

import (
	"context"
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
	"github.com/t0mer/cic/internal/mqtt"
	"github.com/t0mer/cic/internal/store"
)

func startMosquitto(t *testing.T) int {
	t.Helper()
	bin, err := exec.LookPath("mosquitto")
	if err != nil {
		t.Skip("mosquitto not installed")
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	dir := t.TempDir()
	conf := filepath.Join(dir, "mosquitto.conf")
	body := fmt.Sprintf("listener %d 127.0.0.1\nallow_anonymous true\npersistence false\n", port)
	if err := os.WriteFile(conf, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "-c", conf)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	for range 50 {
		if c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(port)), 100*time.Millisecond); err == nil {
			c.Close()
			return port
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("mosquitto did not start")
	return 0
}

func publisher(t *testing.T, port int) paho.Client {
	t.Helper()
	opts := paho.NewClientOptions().AddBroker(fmt.Sprintf("tcp://127.0.0.1:%d", port)).SetClientID("test-pub")
	c := paho.NewClient(opts)
	if tok := c.Connect(); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatalf("publisher connect: %v", tok.Error())
	}
	t.Cleanup(func() { c.Disconnect(100) })
	return c
}

func pub(t *testing.T, c paho.Client, topic, payload string, retain bool) {
	t.Helper()
	tok := c.Publish(topic, 1, retain, payload)
	if !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatalf("publish %s: %v", topic, tok.Error())
	}
}

func TestMQTTIngest(t *testing.T) {
	port := startMosquitto(t)
	pubc := publisher(t, port)

	// Retained messages present before the subscriber connects.
	pub(t, pubc, "dradis/alpha/status", "1", true)
	pub(t, pubc, "dradis/alpha/version", "2026.6.12", true)
	pub(t, pubc, "dradis/alpha/battery", `{"battery_level":11,"charging":false}`, true)
	pub(t, pubc, "dradis/stale/status", "0", true)
	pub(t, pubc, "dradis/ghost/location", `{"latitude":32.0853,"longitude":34.7818}`, true)

	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "cic.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg := NewRegistry(db, 5*time.Minute, log)
	cfg := config.MQTTConfig{Prefixes: []string{"dradis"}, Broker: config.BrokerConfig{Host: "127.0.0.1", Port: port}}
	client, err := mqtt.New(context.Background(), cfg, db, reg, log)
	if err != nil {
		t.Fatal(err)
	}
	again, err := mqtt.New(context.Background(), cfg, db, reg, log)
	if err != nil || again.ClientID() != client.ClientID() {
		t.Fatalf("client id not persisted: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); client.Run(ctx) }()
	defer func() { cancel(); <-done }()

	waitFor(t, "connect", client.Connected)

	pub(t, pubc, "dradis/alpha/battery", `{"battery_level":76,"charging":true,"charge_type":"USB"}`, false)
	pub(t, pubc, "dradis/alpha/wifi", `{"connected":true,"ssid":"Home-WiFi"}`, false)
	pub(t, pubc, "dradis/alpha/location", `{"latitude":32.0853,"longitude":34.7818,"gps_accuracy":5.0,"time":1780473149}`, false)
	pub(t, pubc, "dradis/alpha/device_info", `{"time":1780473149,"device_info":"samsung SM-S926B (15)","screen_locked":true}`, false)
	pub(t, pubc, "dradis/alpha/sendsms/result", `{"phone":"+972501234567","ok":false,"error":"SEND_SMS permission not granted"}`, false)
	pub(t, pubc, "dradis/stale/battery", `{"battery_level":42}`, false)
	pub(t, pubc, "dradis/beta/status", "1", false)
	pub(t, pubc, "dradis/beta/battery", `{"battery_level":50}`, false)
	pub(t, pubc, "dradis/beta/battery", `not json`, false)
	pub(t, pubc, "dradis/gamma/battery", `{"battery_level":300}`, false)
	pub(t, pubc, "dradis/gamma/status", "0", false)
	pub(t, pubc, "dradis/beta/unknown", `{}`, false)
	pub(t, pubc, "dradis/beta/extra/battery", `{"battery_level":1}`, false)
	pub(t, pubc, "other/delta/status", "1", false)
	waitFor(t, "gamma", func() bool { _, err := reg.Get(ctx, "dradis/gamma"); return err == nil })
	waitFor(t, "stale", func() bool { d, err := reg.Get(ctx, "dradis/stale"); return err == nil && d.BatteryLevel != nil })
	waitFor(t, "alpha complete", func() bool {
		d, err := reg.Get(ctx, "dradis/alpha")
		return err == nil && d.DeviceInfo != nil && d.LocationTime != nil && d.BatteryLevel != nil && *d.BatteryLevel == 76
	})

	list, err := reg.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]Device{}
	for _, d := range list {
		keys[d.Key] = d
	}
	if len(keys) != 4 {
		t.Fatalf("devices = %v, want alpha, beta, gamma, stale", keys)
	}
	if st := keys["dradis/stale"]; !st.Online {
		t.Errorf("stale retained 0 followed by live data must be online: %+v", st)
	}
	a := keys["dradis/alpha"]
	if !a.Online || *a.Version != "2026.6.12" || *a.ChargeType != "USB" || !*a.Charging || *a.SSID != "Home-WiFi" ||
		*a.Latitude != 32.0853 || *a.LocationTime != 1780473149 || *a.GPSAccuracy != 5 || !*a.ScreenLocked {
		t.Errorf("alpha = %+v", a)
	}
	b := keys["dradis/beta"]
	if !b.Online || *b.BatteryLevel != 50 {
		t.Errorf("beta = %+v", b)
	}
	g := keys["dradis/gamma"]
	if g.Online || g.BatteryLevel != nil || *g.Status {
		t.Errorf("gamma = %+v", g)
	}
	if _, ok := keys["dradis/ghost"]; ok {
		t.Error("retained location must not create a device")
	}
	if _, ok := keys["other/delta"]; ok {
		t.Error("unconfigured prefix must be ignored")
	}
	if s := client.State(); !s.Connected || s.LastError != "" {
		t.Errorf("state = %+v", s)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
