package mqtt

import (
	"slices"
	"testing"
)

func TestValidSegment(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"pixel-8_a", true},
		{"A1", true},
		{"", false},
		{"bad name", false},
		{"a/b", false},
		{"+", false},
		{"#", false},
		{"דירה", false},
		{string(make([]byte, 65)), false},
	}
	for _, tt := range tests {
		if got := ValidSegment(tt.in); got != tt.want {
			t.Errorf("ValidSegment(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
	long := make([]byte, 64)
	for i := range long {
		long[i] = 'a'
	}
	if !ValidSegment(string(long)) {
		t.Error("64 character segment should be valid")
	}
}

func TestParseTopic(t *testing.T) {
	prefixes := []string{"dradis", "other"}
	tests := []struct {
		topic             string
		prefix, dev, leaf string
		ok                bool
	}{
		{"dradis/phone1/battery", "dradis", "phone1", "battery", true},
		{"other/p-2/sendsms/result", "other", "p-2", "sendsms/result", true},
		{"dradis/phone1/status", "dradis", "phone1", "status", true},
		{"dradis/phone1", "", "", "", false},
		{"dradis/phone1/battery/extra", "", "", "", false},
		{"dradis/phone1/sendsms", "", "", "", false},
		{"dradis/phone1/unknown", "", "", "", false},
		{"nope/phone1/battery", "", "", "", false},
		{"dradis/bad name/battery", "", "", "", false},
		{"dradis/+/battery", "", "", "", false},
		{"dradis/#/battery", "", "", "", false},
		{"dradis//battery", "", "", "", false},
		{"/dradis/phone1/battery", "", "", "", false},
		{"", "", "", "", false},
	}
	for _, tt := range tests {
		p, d, l, ok := ParseTopic(tt.topic, prefixes)
		if p != tt.prefix || d != tt.dev || l != tt.leaf || ok != tt.ok {
			t.Errorf("ParseTopic(%q) = %q,%q,%q,%v", tt.topic, p, d, l, ok)
		}
	}
}

func TestSubscribeFilters(t *testing.T) {
	got := SubscribeFilters([]string{"dradis", "x"})
	if len(got) != 14 {
		t.Fatalf("got %d filters, want 14", len(got))
	}
	for _, want := range []string{"dradis/+/status", "dradis/+/sendsms/result", "x/+/device_info"} {
		if !slices.Contains(got, want) {
			t.Errorf("missing filter %q", want)
		}
	}
}

func TestBuilderCommand(t *testing.T) {
	b := NewBuilder([]string{"dradis"})
	tests := []struct {
		prefix, device, leaf string
		want                 string
		wantErr              bool
	}{
		{"dradis", "phone1", "ping", "dradis/phone1/ping", false},
		{"dradis", "phone1", "sendsms", "dradis/phone1/sendsms", false},
		{"dradis", "phone1", "sendsms/cmd", "", true},
		{"dradis", "phone1", "status", "", true},
		{"other", "phone1", "ping", "", true},
		{"dradis", "bad name", "ping", "", true},
		{"dradis", "a/b", "ping", "", true},
		{"dradis", "+", "ping", "", true},
		{"dradis", "phone1", "", "", true},
		{"dradis", "phone1", "a//b", "", true},
		{"dradis", "phone1", "#", "", true},
	}
	for _, tt := range tests {
		got, err := b.Command(tt.prefix, tt.device, tt.leaf)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("Command(%q,%q,%q) = %q, %v", tt.prefix, tt.device, tt.leaf, got, err)
		}
	}
}
