package devices

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/t0mer/cic/internal/mqtt"
	"github.com/t0mer/cic/internal/store"
)

var t0 = time.Unix(1_780_000_000, 0)

func newTestRegistry(t *testing.T) (*Registry, *time.Time) {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "cic.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	r := NewRegistry(db, 5*time.Minute, slog.New(slog.NewTextHandler(io.Discard, nil)))
	now := t0
	r.now = func() time.Time { return now }
	return r, &now
}

func ev(leaf string, at time.Time, payload any) mqtt.Event {
	return mqtt.Event{Prefix: "dradis", Device: "phone1", Leaf: leaf, ReceivedAt: at, Payload: payload}
}

func mustApply(t *testing.T, r *Registry, e mqtt.Event) {
	t.Helper()
	if err := r.Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
}

func TestAutoDiscoveryAndDefaults(t *testing.T) {
	r, _ := newTestRegistry(t)
	ctx := context.Background()
	mustApply(t, r, ev(mqtt.LeafBattery, t0, mqtt.Battery{Level: new(76)}))
	d, err := r.Get(ctx, "dradis/phone1")
	if err != nil {
		t.Fatal(err)
	}
	if d.FullName != "phone1" || d.Prefix != "dradis" || d.Device != "phone1" || d.Hidden {
		t.Errorf("unexpected defaults: %+v", d)
	}
	if *d.BatteryLevel != 76 || d.Status == nil || !*d.Status || d.Version != nil || d.Latitude != nil || !d.Online {
		t.Errorf("a live message implies online; unknown fields must stay nil: %+v", d)
	}
	if d.LastSeen != t0.Unix() {
		t.Errorf("last_seen = %d", d.LastSeen)
	}
	list, err := r.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("List = %v, %v", list, err)
	}
	if _, err := r.Get(ctx, "dradis/none"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Get missing = %v", err)
	}
}

func TestPartialUpdatesKeepOtherFields(t *testing.T) {
	r, _ := newTestRegistry(t)
	ctx := context.Background()
	tr, fl := true, false
	usb, home := "USB", "Home"
	locTime := int64(1_779_999_000)
	mustApply(t, r, ev(mqtt.LeafBattery, t0, mqtt.Battery{Level: new(76), Charging: &tr, ChargeType: &usb}))
	mustApply(t, r, ev(mqtt.LeafWifi, t0, mqtt.Wifi{Connected: &tr, SSID: &home}))
	mustApply(t, r, ev(mqtt.LeafLocation, t0, mqtt.Location{Latitude: 32.1, Longitude: 34.8, Time: &locTime}))
	mustApply(t, r, ev(mqtt.LeafDeviceInfo, t0, mqtt.DeviceInfo{Info: &usb, ScreenLocked: &tr}))
	mustApply(t, r, ev(mqtt.LeafVersion, t0, mqtt.Version{Version: "1.2.3"}))
	mustApply(t, r, ev(mqtt.LeafStatus, t0, mqtt.Status{Online: true}))

	mustApply(t, r, ev(mqtt.LeafBattery, t0, mqtt.Battery{Charging: &fl}))
	mustApply(t, r, ev(mqtt.LeafDeviceInfo, t0, mqtt.DeviceInfo{ScreenLocked: &fl}))
	d, _ := r.Get(ctx, "dradis/phone1")
	if *d.BatteryLevel != 76 || *d.ChargeType != "USB" || *d.Charging {
		t.Errorf("battery clobbered: %+v", d)
	}
	if *d.DeviceInfo != "USB" || *d.ScreenLocked {
		t.Errorf("device_info clobbered: %+v", d)
	}
	if *d.SSID != "Home" || !*d.WifiConnected || *d.Latitude != 32.1 || *d.LocationTime != locTime || d.GPSAccuracy != nil {
		t.Errorf("wifi/location clobbered: %+v", d)
	}
	if *d.Version != "1.2.3" || !*d.Status {
		t.Errorf("status/version clobbered: %+v", d)
	}

	mustApply(t, r, ev(mqtt.LeafWifi, t0, mqtt.Wifi{Connected: &fl}))
	d, _ = r.Get(ctx, "dradis/phone1")
	if *d.WifiConnected || d.SSID != nil {
		t.Errorf("disconnect should clear ssid: %+v", d)
	}
}

func TestLocationTimeFallsBackToReceipt(t *testing.T) {
	r, _ := newTestRegistry(t)
	mustApply(t, r, ev(mqtt.LeafLocation, t0, mqtt.Location{Latitude: 1, Longitude: 2}))
	d, _ := r.Get(context.Background(), "dradis/phone1")
	if *d.LocationTime != t0.Unix() {
		t.Errorf("location_time = %d", *d.LocationTime)
	}
}

func TestOnlineBoundary(t *testing.T) {
	tests := []struct {
		name   string
		status bool
		age    time.Duration
		want   bool
	}{
		{"fresh online", true, time.Second, true},
		{"exactly at limit", true, 5 * time.Minute, true},
		{"just outside", true, 5*time.Minute + time.Second, false},
		{"status 0 fresh", false, time.Second, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, now := newTestRegistry(t)
			mustApply(t, r, ev(mqtt.LeafStatus, t0, mqtt.Status{Online: tt.status}))
			*now = t0.Add(tt.age)
			d, _ := r.Get(context.Background(), "dradis/phone1")
			if d.Online != tt.want {
				t.Errorf("Online = %v, want %v", d.Online, tt.want)
			}
		})
	}
}

func TestRetainedDoesNotRefreshLastSeen(t *testing.T) {
	r, now := newTestRegistry(t)
	e := ev(mqtt.LeafStatus, t0, mqtt.Status{Online: true})
	e.Retained = true
	mustApply(t, r, e)
	d, _ := r.Get(context.Background(), "dradis/phone1")
	if d.Online || *d.Status != true {
		t.Errorf("retained-only device must not be online: %+v", d)
	}
	mustApply(t, r, ev(mqtt.LeafBattery, t0, mqtt.Battery{Level: new(5)}))
	*now = t0.Add(time.Second)
	d, _ = r.Get(context.Background(), "dradis/phone1")
	if !d.Online {
		t.Errorf("live message should make it online: %+v", d)
	}
}

func TestProfileDeleteReappear(t *testing.T) {
	r, _ := newTestRegistry(t)
	ctx := context.Background()
	mustApply(t, r, ev(mqtt.LeafStatus, t0, mqtt.Status{Online: true}))
	if err := r.SetProfile(ctx, "dradis/phone1", "Dad's phone", "#ff0000", true); err != nil {
		t.Fatal(err)
	}
	d, _ := r.Get(ctx, "dradis/phone1")
	if d.FullName != "Dad's phone" || d.Color != "#ff0000" || !d.Hidden {
		t.Errorf("profile not saved: %+v", d)
	}
	mustApply(t, r, ev(mqtt.LeafBattery, t0, mqtt.Battery{Level: new(50)}))
	d, _ = r.Get(ctx, "dradis/phone1")
	if d.FullName != "Dad's phone" {
		t.Errorf("profile clobbered by telemetry: %+v", d)
	}
	if err := r.Delete(ctx, "dradis/phone1"); err != nil {
		t.Fatal(err)
	}
	if err := r.Delete(ctx, "dradis/phone1"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("second delete = %v", err)
	}
	if err := r.SetProfile(ctx, "dradis/phone1", "x", "", false); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("SetProfile missing = %v", err)
	}
	mustApply(t, r, ev(mqtt.LeafBattery, t0, mqtt.Battery{Level: new(40)}))
	d, err := r.Get(ctx, "dradis/phone1")
	if err != nil || d.FullName != "phone1" || d.Hidden || *d.BatteryLevel != 40 {
		t.Errorf("reappeared device = %+v, %v", d, err)
	}
}

func TestOnChangeAndSMSResult(t *testing.T) {
	r, _ := newTestRegistry(t)
	var got []Device
	r.OnChange(func(d Device) { got = append(got, d) })
	mustApply(t, r, ev(mqtt.LeafBattery, t0, mqtt.Battery{Level: new(9)}))
	mustApply(t, r, ev(mqtt.LeafSMSResult, t0, mqtt.SMSResult{OK: true}))
	if len(got) != 1 || *got[0].BatteryLevel != 9 {
		t.Errorf("hook calls = %+v", got)
	}
	devs, _ := r.List(context.Background())
	if len(devs) != 1 {
		t.Errorf("sms result must not create devices")
	}
	if err := r.Apply(context.Background(), ev(mqtt.LeafBattery, t0, "bogus")); err == nil {
		t.Error("unsupported payload should error")
	}
}

func TestConcurrentApply(t *testing.T) {
	r, _ := newTestRegistry(t)
	var wg sync.WaitGroup
	for i := range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e := mqtt.Event{Prefix: "dradis", Device: "dev" + string(rune('a'+i%4)), ReceivedAt: t0}
			if i%2 == 0 {
				e.Leaf, e.Payload = mqtt.LeafBattery, mqtt.Battery{Level: new(i)}
			} else {
				e.Leaf, e.Payload = mqtt.LeafStatus, mqtt.Status{Online: true}
			}
			if err := r.Apply(context.Background(), e); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	list, err := r.List(context.Background())
	if err != nil || len(list) != 4 {
		t.Fatalf("List = %d devices, %v", len(list), err)
	}
}

func live(leaf string, off time.Duration, p any) mqtt.Event { return ev(leaf, t0.Add(off), p) }

func retained(leaf string, off time.Duration, p any) mqtt.Event {
	e := live(leaf, off, p)
	e.Retained = true
	return e
}

func TestLivenessRules(t *testing.T) {
	bat := mqtt.Battery{Level: new(50)}
	on, off := mqtt.Status{Online: true}, mqtt.Status{}
	tests := []struct {
		name       string
		events     []mqtt.Event
		at         time.Duration
		wantOnline bool
		wantStatus bool
	}{
		{"stale retained 0 then live battery", []mqtt.Event{retained(mqtt.LeafStatus, 0, off), live(mqtt.LeafBattery, time.Second, bat)}, 2 * time.Second, true, true},
		{"live status 0 after live battery", []mqtt.Event{live(mqtt.LeafBattery, 0, bat), live(mqtt.LeafStatus, time.Second, off)}, 2 * time.Second, false, false},
		{"live battery after live status 0", []mqtt.Event{live(mqtt.LeafStatus, 0, off), live(mqtt.LeafBattery, time.Second, bat)}, 2 * time.Second, true, true},
		{"retained 1 only", []mqtt.Event{retained(mqtt.LeafStatus, 0, on)}, time.Second, false, true},
		{"retained 1 does not override live 0", []mqtt.Event{live(mqtt.LeafStatus, 0, off), retained(mqtt.LeafStatus, time.Second, on)}, 2 * time.Second, false, false},
		{"retained 0 does not override live battery", []mqtt.Event{live(mqtt.LeafBattery, 0, bat), retained(mqtt.LeafStatus, time.Second, off)}, 2 * time.Second, true, true},
		{"retained version keeps last_seen", []mqtt.Event{retained(mqtt.LeafVersion, 0, mqtt.Version{Version: "1"})}, time.Second, false, false},
		{"live status 1", []mqtt.Event{live(mqtt.LeafStatus, 0, on)}, time.Second, true, true},
		{"just inside offline_after", []mqtt.Event{live(mqtt.LeafBattery, 0, bat)}, 5 * time.Minute, true, true},
		{"just outside offline_after", []mqtt.Event{live(mqtt.LeafBattery, 0, bat)}, 5*time.Minute + time.Second, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, now := newTestRegistry(t)
			for _, e := range tt.events {
				mustApply(t, r, e)
			}
			*now = t0.Add(tt.at)
			d, err := r.Get(context.Background(), "dradis/phone1")
			if err != nil {
				t.Fatal(err)
			}
			if d.Online != tt.wantOnline {
				t.Errorf("Online = %v, want %v (%+v)", d.Online, tt.wantOnline, d)
			}
			if got := d.Status != nil && *d.Status; got != tt.wantStatus {
				t.Errorf("status = %v, want %v", got, tt.wantStatus)
			}
		})
	}
}

func TestRetainedNeverBumpsLastSeen(t *testing.T) {
	r, _ := newTestRegistry(t)
	mustApply(t, r, retained(mqtt.LeafVersion, 0, mqtt.Version{Version: "1"}))
	mustApply(t, r, live(mqtt.LeafBattery, time.Second, mqtt.Battery{Level: new(5)}))
	mustApply(t, r, retained(mqtt.LeafVersion, time.Hour, mqtt.Version{Version: "2"}))
	d, _ := r.Get(context.Background(), "dradis/phone1")
	if d.LastSeen != t0.Add(time.Second).Unix() || *d.Version != "2" {
		t.Errorf("last_seen = %d, version = %v", d.LastSeen, d.Version)
	}
	mustApply(t, r, live(mqtt.LeafBattery, -time.Hour, mqtt.Battery{Level: new(6)}))
	d, _ = r.Get(context.Background(), "dradis/phone1")
	if d.LastSeen != t0.Add(time.Second).Unix() {
		t.Errorf("last_seen must be monotonic, got %d", d.LastSeen)
	}
}

func TestDeviceCap(t *testing.T) {
	r, _ := newTestRegistry(t)
	r.SetMaxDevices(2)
	ctx := context.Background()
	apply := func(dev string) error {
		return r.Apply(ctx, mqtt.Event{Prefix: "dradis", Device: dev, Leaf: mqtt.LeafBattery, ReceivedAt: t0, Payload: mqtt.Battery{Level: new(1)}})
	}
	for _, d := range []string{"a", "b"} {
		if err := apply(d); err != nil {
			t.Fatal(err)
		}
	}
	if err := apply("c"); !errors.Is(err, ErrDeviceLimit) {
		t.Fatalf("third device = %v, want ErrDeviceLimit", err)
	}
	if err := apply("a"); err != nil {
		t.Errorf("existing device must keep updating: %v", err)
	}
	if list, _ := r.List(ctx); len(list) != 2 {
		t.Errorf("devices = %d, want 2", len(list))
	}
	r.SetMaxDevices(0)
	if err := apply("c"); err != nil {
		t.Errorf("unlimited cap: %v", err)
	}
}

func TestWifiSSIDHandling(t *testing.T) {
	tr, fl := true, false
	home := "Home"
	tests := []struct {
		name     string
		second   mqtt.Wifi
		wantConn bool
		wantSSID *string
	}{
		{"connected without ssid keeps ssid", mqtt.Wifi{Connected: &tr}, true, &home},
		{"connected with new ssid", mqtt.Wifi{Connected: &tr, SSID: new("Work")}, true, new("Work")},
		{"disconnected clears ssid", mqtt.Wifi{Connected: &fl, SSID: &home}, false, nil},
		{"ssid only keeps connection", mqtt.Wifi{SSID: new("Work")}, true, new("Work")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, _ := newTestRegistry(t)
			mustApply(t, r, ev(mqtt.LeafWifi, t0, mqtt.Wifi{Connected: &tr, SSID: &home}))
			mustApply(t, r, ev(mqtt.LeafWifi, t0, tt.second))
			d, _ := r.Get(context.Background(), "dradis/phone1")
			if *d.WifiConnected != tt.wantConn || (d.SSID == nil) != (tt.wantSSID == nil) || (d.SSID != nil && *d.SSID != *tt.wantSSID) {
				t.Errorf("wifi = %v %v", *d.WifiConnected, d.SSID)
			}
		})
	}
}

func TestStaleLocationIgnored(t *testing.T) {
	tests := []struct {
		name    string
		second  int64
		wantLat float64
	}{
		{"older ignored", 900, 1},
		{"equal wins", 1000, 2},
		{"newer wins", 1100, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, now := newTestRegistry(t)
			first, second := int64(1000), tt.second
			mustApply(t, r, ev(mqtt.LeafLocation, t0, mqtt.Location{Latitude: 1, Longitude: 1, Time: &first}))
			mustApply(t, r, ev(mqtt.LeafLocation, t0.Add(time.Second), mqtt.Location{Latitude: 2, Longitude: 2, Time: &second}))
			*now = t0.Add(2 * time.Second)
			d, _ := r.Get(context.Background(), "dradis/phone1")
			if *d.Latitude != tt.wantLat {
				t.Errorf("latitude = %v, want %v", *d.Latitude, tt.wantLat)
			}
			if !d.Online || d.LastSeen != t0.Add(time.Second).Unix() {
				t.Errorf("stale location is still a live message: %+v", d)
			}
		})
	}
}

func TestApplyAndDeleteRace(t *testing.T) {
	r, _ := newTestRegistry(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for range 50 {
				if err := r.Apply(ctx, ev(mqtt.LeafBattery, t0, mqtt.Battery{Level: new(1)})); err != nil {
					t.Error(err)
				}
			}
		}()
		go func() {
			defer wg.Done()
			for range 50 {
				_ = r.Delete(ctx, "dradis/phone1")
			}
		}()
	}
	wg.Wait()
}

func TestUpdateProfileAndDeleteHook(t *testing.T) {
	r, _ := newTestRegistry(t)
	ctx := context.Background()
	mustApply(t, r, ev(mqtt.LeafBattery, t0, mqtt.Battery{Level: new(5)}))
	var removed []Device
	r.OnRemove(func(d Device) { removed = append(removed, d) })

	name, color, hidden := "Tablet", "#aabbcc", true
	d, was, err := r.UpdateProfile(ctx, "dradis/phone1", ProfilePatch{FullName: &name, Color: &color, Hidden: &hidden})
	if err != nil || was || d.FullName != "Tablet" || d.Color != "#aabbcc" || !d.Hidden {
		t.Fatalf("%+v %v", d, err)
	}
	empty := ""
	d, was, err = r.UpdateProfile(ctx, "dradis/phone1", ProfilePatch{FullName: &empty}) // others untouched
	if err != nil || !was || d.FullName != "phone1" || d.Color != "#aabbcc" || !d.Hidden {
		t.Fatalf("partial: %+v %v", d, err)
	}
	if _, _, err := r.UpdateProfile(ctx, "dradis/ghost", ProfilePatch{Hidden: &hidden}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown: %v", err)
	}
	if err := r.Delete(ctx, "dradis/ghost"); !errors.Is(err, store.ErrNotFound) || len(removed) != 0 {
		t.Errorf("delete unknown: %v %v", err, removed)
	}
	if err := r.Delete(ctx, "dradis/phone1"); err != nil || len(removed) != 1 || removed[0].Key != "dradis/phone1" || !removed[0].Hidden {
		t.Errorf("delete: %v %+v", err, removed)
	}
	if _, err := r.Get(ctx, "dradis/phone1"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("still there: %v", err)
	}
}

func TestDeleteHookSeesHiddenAtDeletion(t *testing.T) {
	r, _ := newTestRegistry(t)
	ctx := context.Background()
	mustApply(t, r, ev(mqtt.LeafBattery, t0, mqtt.Battery{Level: new(5)}))
	var removed []Device
	r.OnRemove(func(d Device) { removed = append(removed, d) })
	hidden := true
	if _, _, err := r.UpdateProfile(ctx, "dradis/phone1", ProfilePatch{Hidden: &hidden}); err != nil {
		t.Fatal(err)
	}
	if err := r.Delete(ctx, "dradis/phone1"); err != nil || len(removed) != 1 || !removed[0].Hidden {
		t.Fatalf("hook must see the hidden row: %v %+v", err, removed)
	}
}

func TestUpdateProfileConcurrentChainsPreviousHidden(t *testing.T) {
	r, _ := newTestRegistry(t)
	ctx := context.Background()
	mustApply(t, r, ev(mqtt.LeafBattery, t0, mqtt.Battery{Level: new(5)}))
	// Each update reports the flag the previous committed update left behind (initially false).
	type step struct{ was, now bool }
	var mu sync.Mutex
	var steps []step
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h := i%2 == 0
			d, was, err := r.UpdateProfile(ctx, "dradis/phone1", ProfilePatch{Hidden: &h})
			if err != nil {
				t.Errorf("update: %v", err)
				return
			}
			mu.Lock()
			steps = append(steps, step{was, d.Hidden})
			mu.Unlock()
		}()
	}
	wg.Wait()
	// Net number of false->true flips minus true->false flips must equal the final state.
	net := 0
	for _, st := range steps {
		switch {
		case !st.was && st.now:
			net++
		case st.was && !st.now:
			net--
		}
	}
	final, err := r.Get(ctx, "dradis/phone1")
	if err != nil {
		t.Fatal(err)
	}
	if want := map[bool]int{false: 0, true: 1}[final.Hidden]; net != want {
		t.Fatalf("net flips %d, final hidden=%v: %+v", net, final.Hidden, steps)
	}
}

func TestSetLocationName(t *testing.T) {
	r, _ := newTestRegistry(t)
	ctx := context.Background()
	at := int64(1700000000)
	ev := mqtt.Event{Prefix: "dradis", Device: "p", Leaf: mqtt.LeafLocation,
		Payload: mqtt.Location{Latitude: 32.1, Longitude: 34.8, Time: &at}, ReceivedAt: time.Unix(at, 0)}
	if err := r.Apply(ctx, ev); err != nil {
		t.Fatal(err)
	}
	var seen *Device
	r.OnChange(func(d Device) { seen = &d })
	if err := r.SetLocationName(ctx, "dradis/p", "Home", 32.1, 34.8, time.Unix(at+5, 0)); err != nil {
		t.Fatal(err)
	}
	d, _ := r.Get(ctx, "dradis/p")
	if d.LocationName == nil || *d.LocationName != "Home" || *d.LocationNameAt != at+5 || *d.GeocodedLat != 32.1 {
		t.Fatalf("device %+v", d)
	}
	if seen == nil || seen.LocationName == nil {
		t.Fatal("OnChange not fired")
	}
	// A device that moved on meanwhile keeps its old name.
	if err := r.SetLocationName(ctx, "dradis/p", "Old", 1, 1, time.Unix(at, 0)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("moved: %v", err)
	}
}

func TestLocationNameClearedWhenMovedFar(t *testing.T) {
	r, _ := newTestRegistry(t)
	ctx := context.Background()
	t0 := int64(1700000000)
	loc := func(lat, lon float64, at int64) {
		t.Helper()
		ev := mqtt.Event{Prefix: "dradis", Device: "p", Leaf: mqtt.LeafLocation,
			Payload: mqtt.Location{Latitude: lat, Longitude: lon, Time: &at}, ReceivedAt: time.Unix(at, 0)}
		if err := r.Apply(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	loc(32.1, 34.8, t0)
	if err := r.SetLocationName(ctx, "dradis/p", "Home", 32.1, 34.8, time.Unix(t0, 0)); err != nil {
		t.Fatal(err)
	}
	loc(32.105, 34.8, t0+10) // ~555 m: keeps the name
	d, _ := r.Get(ctx, "dradis/p")
	if d.LocationName == nil || *d.LocationName != "Home" || d.GeocodedLat == nil {
		t.Fatalf("near move dropped the name: %+v", d)
	}
	loc(32.12, 34.8, t0+20) // ~2.2 km: stale
	d, _ = r.Get(ctx, "dradis/p")
	if d.LocationName != nil || d.LocationNameAt != nil || d.GeocodedLat != nil || d.GeocodedLon != nil {
		t.Fatalf("far move kept the name: %+v", d)
	}
	if *d.Latitude != 32.12 {
		t.Fatalf("location not updated: %v", *d.Latitude)
	}
}

func TestSetLocationNameEmptyClearsName(t *testing.T) {
	r, _ := newTestRegistry(t)
	ctx := context.Background()
	at := int64(1700000000)
	apply := func(lat float64, ts int64) {
		ev := mqtt.Event{Prefix: "dradis", Device: "p", Leaf: mqtt.LeafLocation,
			Payload: mqtt.Location{Latitude: lat, Longitude: 34.8, Time: &ts}, ReceivedAt: time.Unix(ts, 0)}
		if err := r.Apply(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	apply(32.1, at)
	if err := r.SetLocationName(ctx, "dradis/p", "Home", 32.1, 34.8, time.Unix(at, 0)); err != nil {
		t.Fatal(err)
	}
	apply(32.104, at+1) // within 1000 m: name kept, but for other coordinates
	if err := r.SetLocationName(ctx, "dradis/p", "", 32.104, 34.8, time.Unix(at+2, 0)); err != nil {
		t.Fatal(err)
	}
	d, _ := r.Get(ctx, "dradis/p")
	if d.LocationName != nil || d.LocationNameAt == nil || *d.LocationNameAt != at+2 || *d.GeocodedLat != 32.104 {
		t.Fatalf("device %+v", d)
	}
}

func pointCount(t *testing.T, r *Registry) int {
	t.Helper()
	var n int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM location_points`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestOnLocationHook(t *testing.T) {
	r, _ := newTestRegistry(t)
	type call struct {
		key      string
		ts       int64
		lat, lon float64
		acc      *float64
	}
	var got []call
	r.OnLocation(func(key string, ts int64, lat, lon float64, acc *float64) {
		got = append(got, call{key, ts, lat, lon, acc})
	})
	fix := t0.Unix() - 100
	mustApply(t, r, ev(mqtt.LeafBattery, t0, mqtt.Battery{Level: new(9)}))
	mustApply(t, r, ev(mqtt.LeafLocation, t0, mqtt.Location{Latitude: 32, Longitude: 34, GPSAccuracy: new(5.0), Time: &fix}))
	older := fix - 50
	mustApply(t, r, ev(mqtt.LeafLocation, t0, mqtt.Location{Latitude: 33, Longitude: 35, Time: &older})) // stale
	mustApply(t, r, ev(mqtt.LeafLocation, t0, mqtt.Location{Latitude: 34, Longitude: 36}))               // no fix time: receipt time
	if len(got) != 2 {
		t.Fatalf("hook calls = %+v", got)
	}
	if got[0].key != "dradis/phone1" || got[0].ts != fix || got[0].lat != 32 || got[0].acc == nil || *got[0].acc != 5 {
		t.Errorf("first call = %+v", got[0])
	}
	if got[1].ts != t0.Unix() {
		t.Errorf("second call ts = %d", got[1].ts)
	}
}

func TestDeleteRemovesLocationPoints(t *testing.T) {
	r, _ := newTestRegistry(t)
	ctx := context.Background()
	mustApply(t, r, ev(mqtt.LeafStatus, t0, mqtt.Status{Online: true}))
	mustApply(t, r, mqtt.Event{Prefix: "dradis", Device: "other", Leaf: mqtt.LeafStatus, ReceivedAt: t0, Payload: mqtt.Status{Online: true}})
	for _, k := range []string{"dradis/phone1", "dradis/other"} {
		if _, err := r.db.Exec(`INSERT INTO location_points(device_key, ts, lat, lon) VALUES (?, 1, 1, 1)`, k); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Delete(ctx, "dradis/phone1"); err != nil {
		t.Fatal(err)
	}
	if n := pointCount(t, r); n != 1 {
		t.Fatalf("points after delete = %d, want 1 (only the other device's)", n)
	}
}
