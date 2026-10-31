package mqtt

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode"
)

// MaxPayloadSize is the largest payload, in bytes, that is parsed.
const MaxPayloadSize = 8 << 10

// Status is a device liveness message.
type Status struct {
	Online bool
}

// Version is the device app version.
type Version struct {
	Version string
}

// Battery is a battery telemetry snapshot; nil fields were not reported.
type Battery struct {
	Level      *int
	Charging   *bool
	ChargeType *string
}

// Wifi is a Wi-Fi telemetry snapshot; nil fields were not reported.
type Wifi struct {
	Connected *bool
	SSID      *string
}

// Location is a GPS fix. Time is Unix epoch seconds and nil when not reported.
type Location struct {
	Latitude    float64
	Longitude   float64
	GPSAccuracy *float64
	Time        *int64
}

// DeviceInfo describes the device hardware and lock state; nil fields were not reported.
type DeviceInfo struct {
	Info         *string
	ScreenLocked *bool
	Time         *int64
}

// SMSResult is the outcome of a send-SMS command.
type SMSResult struct {
	Phone string
	OK    bool
	Error string
}

func checkSize(b []byte) error {
	if len(b) > MaxPayloadSize {
		return fmt.Errorf("payload of %d bytes exceeds limit", len(b))
	}
	return nil
}

func decode(b []byte, v any) error {
	if err := checkSize(b); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("decoding json: %w", err)
	}
	return nil
}

// ParseStatus parses a "1" or "0" status payload.
func ParseStatus(b []byte) (Status, error) {
	if err := checkSize(b); err != nil {
		return Status{}, err
	}
	switch strings.TrimSpace(string(b)) {
	case "1":
		return Status{Online: true}, nil
	case "0":
		return Status{}, nil
	}
	return Status{}, errors.New("status must be 1 or 0")
}

// ParseVersion parses a plain-text version payload.
func ParseVersion(b []byte) (Version, error) {
	if err := checkSize(b); err != nil {
		return Version{}, err
	}
	s := strings.TrimSpace(string(b))
	if s == "" || len(s) > 64 {
		return Version{}, errors.New("version must be 1 to 64 characters")
	}
	for _, r := range s {
		if !unicode.IsPrint(r) {
			return Version{}, errors.New("version contains non-printable characters")
		}
	}
	return Version{Version: s}, nil
}

// ParseBattery parses a battery payload.
func ParseBattery(b []byte) (Battery, error) {
	var raw struct {
		Level      *float64 `json:"battery_level"`
		Charging   *bool    `json:"charging"`
		ChargeType *string  `json:"charge_type"`
	}
	if err := decode(b, &raw); err != nil {
		return Battery{}, err
	}
	out := Battery{Charging: raw.Charging, ChargeType: raw.ChargeType}
	if raw.Level != nil {
		l := *raw.Level
		if math.IsNaN(l) || l < 0 || l > 100 {
			return Battery{}, errors.New("battery_level out of range")
		}
		n := int(math.Round(l))
		out.Level = &n
	}
	if out.ChargeType != nil && len(*out.ChargeType) > 32 {
		return Battery{}, errors.New("charge_type too long")
	}
	return out, nil
}

// ParseWifi parses a Wi-Fi payload; a null or empty ssid stays unknown.
func ParseWifi(b []byte) (Wifi, error) {
	var raw struct {
		Connected *bool   `json:"connected"`
		SSID      *string `json:"ssid"`
	}
	if err := decode(b, &raw); err != nil {
		return Wifi{}, err
	}
	if raw.SSID != nil && (*raw.SSID == "" || len(*raw.SSID) > 64) {
		raw.SSID = nil
	}
	return Wifi{Connected: raw.Connected, SSID: raw.SSID}, nil
}

// ParseLocation parses a location payload; latitude and longitude are required.
func ParseLocation(b []byte) (Location, error) {
	var raw struct {
		Latitude    *float64 `json:"latitude"`
		Longitude   *float64 `json:"longitude"`
		GPSAccuracy *float64 `json:"gps_accuracy"`
		Time        *float64 `json:"time"`
	}
	if err := decode(b, &raw); err != nil {
		return Location{}, err
	}
	if raw.Latitude == nil || raw.Longitude == nil {
		return Location{}, errors.New("latitude and longitude are required")
	}
	lat, lon := *raw.Latitude, *raw.Longitude
	if math.IsNaN(lat) || math.IsNaN(lon) || lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		return Location{}, errors.New("coordinates out of range")
	}
	out := Location{Latitude: lat, Longitude: lon}
	if a := raw.GPSAccuracy; a != nil && !math.IsNaN(*a) && *a >= 0 {
		out.GPSAccuracy = a
	}
	t, err := epoch(raw.Time)
	if err != nil {
		return Location{}, err
	}
	out.Time = t
	return out, nil
}

// ParseDeviceInfo parses a device_info payload.
func ParseDeviceInfo(b []byte) (DeviceInfo, error) {
	var raw struct {
		Time         *float64 `json:"time"`
		DeviceInfo   *string  `json:"device_info"`
		ScreenLocked *bool    `json:"screen_locked"`
	}
	if err := decode(b, &raw); err != nil {
		return DeviceInfo{}, err
	}
	if raw.DeviceInfo != nil && len(*raw.DeviceInfo) > 256 {
		return DeviceInfo{}, errors.New("device_info too long")
	}
	t, err := epoch(raw.Time)
	if err != nil {
		return DeviceInfo{}, err
	}
	return DeviceInfo{Info: raw.DeviceInfo, ScreenLocked: raw.ScreenLocked, Time: t}, nil
}

// ParseSMSResult parses a sendsms/result payload; ok is required.
func ParseSMSResult(b []byte) (SMSResult, error) {
	var raw struct {
		Phone string `json:"phone"`
		OK    *bool  `json:"ok"`
		Error string `json:"error"`
	}
	if err := decode(b, &raw); err != nil {
		return SMSResult{}, err
	}
	if raw.OK == nil {
		return SMSResult{}, errors.New("ok is required")
	}
	return SMSResult{Phone: raw.Phone, OK: *raw.OK, Error: raw.Error}, nil
}

func epoch(f *float64) (*int64, error) {
	if f == nil {
		return nil, nil
	}
	if math.IsNaN(*f) || *f < 0 || *f > 1e11 {
		return nil, errors.New("time out of range (expected Unix epoch seconds)")
	}
	t := int64(*f)
	return &t, nil
}

func parseLeaf(leaf string, b []byte) (any, error) {
	switch leaf {
	case LeafStatus:
		return ParseStatus(b)
	case LeafVersion:
		return ParseVersion(b)
	case LeafBattery:
		return ParseBattery(b)
	case LeafWifi:
		return ParseWifi(b)
	case LeafLocation:
		return ParseLocation(b)
	case LeafDeviceInfo:
		return ParseDeviceInfo(b)
	case LeafSMSResult:
		return ParseSMSResult(b)
	}
	return nil, fmt.Errorf("unknown leaf %q", leaf)
}
