package main

import "testing"

func TestProbeAddr(t *testing.T) {
	tests := []struct {
		listen, want string
		wantErr      bool
	}{
		{":8080", "127.0.0.1:8080", false},
		{"0.0.0.0:9000", "127.0.0.1:9000", false},
		{"[::]:9000", "127.0.0.1:9000", false},
		{"127.0.0.1:1", "127.0.0.1:1", false},
		{"192.168.1.5:8080", "192.168.1.5:8080", false},
		{"[::1]:8080", "[::1]:8080", false},
		{"cic.local:80", "cic.local:80", false},
		{"8080", "", true},
		{"", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.listen, func(t *testing.T) {
			got, err := probeAddr(tt.listen)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Fatalf("got %q, %v; want %q (err=%v)", got, err, tt.want, tt.wantErr)
			}
		})
	}
}
