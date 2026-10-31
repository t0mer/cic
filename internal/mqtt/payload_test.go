package mqtt

import (
	"strings"
	"testing"
)

func TestParseStatus(t *testing.T) {
	tests := []struct {
		in      string
		want    bool
		wantErr bool
	}{
		{"1", true, false},
		{"0", false, false},
		{" 1\n", true, false},
		{"", false, true},
		{"true", false, true},
		{"2", false, true},
		{strings.Repeat("1", MaxPayloadSize+1), false, true},
	}
	for _, tt := range tests {
		got, err := ParseStatus([]byte(tt.in))
		if (err != nil) != tt.wantErr || got.Online != tt.want {
			t.Errorf("ParseStatus(%.10q) = %v, %v", tt.in, got, err)
		}
	}
}

func TestParseVersion(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"2026.6.12", "2026.6.12", false},
		{" 2026.6.12\n", "2026.6.12", false},
		{"", "", true},
		{"a\x00b", "", true},
		{strings.Repeat("v", 65), "", true},
		{strings.Repeat("v", MaxPayloadSize+1), "", true},
	}
	for _, tt := range tests {
		got, err := ParseVersion([]byte(tt.in))
		if (err != nil) != tt.wantErr || got.Version != tt.want {
			t.Errorf("ParseVersion(%.10q) = %v, %v", tt.in, got, err)
		}
	}
}

func TestParseBattery(t *testing.T) {
	got, err := ParseBattery([]byte(`{"battery_level":76,"charging":true,"charge_type":"USB"}`))
	if err != nil || *got.Level != 76 || !*got.Charging || *got.ChargeType != "USB" {
		t.Fatalf("README example: %+v, %v", got, err)
	}
	got, err = ParseBattery([]byte(`{"charging":false,"extra":1}`))
	if err != nil || got.Level != nil || got.ChargeType != nil || *got.Charging {
		t.Errorf("missing fields: %+v, %v", got, err)
	}
	got, err = ParseBattery([]byte(`{"battery_level":0}`))
	if err != nil || got.Level == nil || *got.Level != 0 {
		t.Errorf("explicit zero: %+v, %v", got, err)
	}
	for _, in := range []string{
		`{"battery_level":"76"}`, `{"battery_level":101}`, `{"battery_level":-1}`,
		`{"charging":"yes"}`, `garbage`, ``, `[1]`,
		`{"battery_level":1,"pad":"` + strings.Repeat("x", MaxPayloadSize) + `"}`,
	} {
		if _, err := ParseBattery([]byte(in)); err == nil {
			t.Errorf("ParseBattery(%.40q) should fail", in)
		}
	}
}

func TestParseWifi(t *testing.T) {
	got, err := ParseWifi([]byte(`{"connected":true,"ssid":"Home-WiFi"}`))
	if err != nil || !*got.Connected || *got.SSID != "Home-WiFi" {
		t.Fatalf("README example: %+v, %v", got, err)
	}
	got, err = ParseWifi([]byte(`{"connected":true,"ssid":null}`))
	if err != nil || !*got.Connected || got.SSID != nil {
		t.Errorf("null ssid: %+v, %v", got, err)
	}
	got, err = ParseWifi([]byte(`{}`))
	if err != nil || got.Connected != nil || got.SSID != nil {
		t.Errorf("missing fields: %+v, %v", got, err)
	}
	for _, in := range []string{`{"connected":"true"}`, `{"ssid":5}`, `garbage`, `{"ssid":"` + strings.Repeat("x", MaxPayloadSize) + `"}`} {
		if _, err := ParseWifi([]byte(in)); err == nil {
			t.Errorf("ParseWifi(%.40q) should fail", in)
		}
	}
}

func TestParseLocation(t *testing.T) {
	got, err := ParseLocation([]byte(`{"latitude":32.0853,"longitude":34.7818,"gps_accuracy":5.0,"time":1780473149}`))
	if err != nil || got.Latitude != 32.0853 || got.Longitude != 34.7818 || *got.GPSAccuracy != 5 || *got.Time != 1780473149 {
		t.Fatalf("README example: %+v, %v", got, err)
	}
	got, err = ParseLocation([]byte(`{"latitude":0,"longitude":0}`))
	if err != nil || got.GPSAccuracy != nil || got.Time != nil {
		t.Errorf("optional fields: %+v, %v", got, err)
	}
	for _, in := range []string{
		`{"latitude":32.0}`, `{"longitude":34.0}`, `{}`,
		`{"latitude":"32","longitude":34}`,
		`{"latitude":91,"longitude":0}`, `{"latitude":-90.1,"longitude":0}`,
		`{"latitude":0,"longitude":180.5}`, `{"latitude":0,"longitude":-181}`,
		`{"latitude":0,"longitude":0,"time":-5}`,
		`garbage`, ``,
		`{"latitude":1,"longitude":1,"pad":"` + strings.Repeat("x", MaxPayloadSize) + `"}`,
	} {
		if _, err := ParseLocation([]byte(in)); err == nil {
			t.Errorf("ParseLocation(%.40q) should fail", in)
		}
	}
}

func TestParseDeviceInfo(t *testing.T) {
	got, err := ParseDeviceInfo([]byte(`{"time":1780473149,"device_info":"samsung SM-S926B (15)","screen_locked":true}`))
	if err != nil || *got.Info != "samsung SM-S926B (15)" || !*got.ScreenLocked || *got.Time != 1780473149 {
		t.Fatalf("README example: %+v, %v", got, err)
	}
	got, err = ParseDeviceInfo([]byte(`{"screen_locked":false}`))
	if err != nil || got.Info != nil || got.Time != nil || *got.ScreenLocked {
		t.Errorf("missing fields: %+v, %v", got, err)
	}
	for _, in := range []string{`{"device_info":5}`, `{"screen_locked":"no"}`, `garbage`, `{"device_info":"` + strings.Repeat("x", 300) + `"}`} {
		if _, err := ParseDeviceInfo([]byte(in)); err == nil {
			t.Errorf("ParseDeviceInfo(%.40q) should fail", in)
		}
	}
}

func TestParseSMSResult(t *testing.T) {
	got, err := ParseSMSResult([]byte(`{"phone":"+972501234567","ok":false,"error":"SEND_SMS permission not granted"}`))
	if err != nil || got.OK || got.Error != "SEND_SMS permission not granted" || got.Phone == "" {
		t.Fatalf("README example: %+v, %v", got, err)
	}
	got, err = ParseSMSResult([]byte(`{"phone":"+972501234567","ok":true}`))
	if err != nil || !got.OK || got.Error != "" {
		t.Errorf("no error field: %+v, %v", got, err)
	}
	for _, in := range []string{`{"phone":"x"}`, `{"ok":"true"}`, `garbage`, ``} {
		if _, err := ParseSMSResult([]byte(in)); err == nil {
			t.Errorf("ParseSMSResult(%.40q) should fail", in)
		}
	}
}
