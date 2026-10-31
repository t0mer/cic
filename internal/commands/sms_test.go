package commands

import (
	"strings"
	"testing"
)

func TestSMSSegments(t *testing.T) {
	tests := []struct {
		name        string
		text        string
		enc         string
		chars, segs int
	}{
		{"empty", "", "GSM-7", 0, 0},
		{"hello", "Hello", "GSM-7", 5, 1},
		{"gsm 160", strings.Repeat("a", 160), "GSM-7", 160, 1},
		{"gsm 161", strings.Repeat("a", 161), "GSM-7", 161, 2},
		{"gsm 306", strings.Repeat("a", 306), "GSM-7", 306, 2},
		{"gsm 307", strings.Repeat("a", 307), "GSM-7", 307, 3},
		{"extension counts 2", "^{}\\[~]|€", "GSM-7", 18, 1},
		{"extension pushes over", strings.Repeat("a", 159) + "{", "GSM-7", 161, 2},
		{"gsm special", "£$¥èé@\n", "GSM-7", 7, 1},
		{"hebrew", "שלום", "UCS-2", 4, 1},
		{"ucs 70", strings.Repeat("ש", 70), "UCS-2", 70, 1},
		{"ucs 71", strings.Repeat("ש", 71), "UCS-2", 71, 2},
		{"ucs 134", strings.Repeat("ש", 134), "UCS-2", 134, 2},
		{"ucs 135", strings.Repeat("ש", 135), "UCS-2", 135, 3},
		{"emoji surrogate", "😀", "UCS-2", 2, 1},
		{"emoji 35 = 70 units", strings.Repeat("😀", 35), "UCS-2", 70, 1},
		{"emoji 36 = 72 units", strings.Repeat("😀", 36), "UCS-2", 72, 2},
		{"mixed forces ucs", "hello ש", "UCS-2", 7, 1},
		{"ext straddles 152/153", strings.Repeat("a", 152) + "{" + strings.Repeat("b", 152), "GSM-7", 306, 3},
		{"ext fits exactly at 153", strings.Repeat("a", 151) + "{" + strings.Repeat("b", 153), "GSM-7", 306, 2},
		{"emoji straddles 66/67", strings.Repeat("ש", 66) + "😀" + strings.Repeat("ש", 66), "UCS-2", 134, 3},
		{"emoji fits exactly at 67", strings.Repeat("ש", 65) + "😀" + strings.Repeat("ש", 67), "UCS-2", 134, 2},
		{"non-gsm latin", "ç", "UCS-2", 1, 1},
	}
	for _, tt := range tests {
		enc, chars, segs := SMSSegments(tt.text)
		if enc != tt.enc || chars != tt.chars || segs != tt.segs {
			t.Errorf("%s: got %s %d %d, want %s %d %d", tt.name, enc, chars, segs, tt.enc, tt.chars, tt.segs)
		}
	}
}

func TestMaskPhone(t *testing.T) {
	tests := map[string]string{
		"+972501234567": "+*********567",
		"0501234567":    "*******567",
		"+12":           "+**",
		"":              "",
	}
	for in, want := range tests {
		if got := MaskPhone(in); got != want {
			t.Errorf("MaskPhone(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPreview(t *testing.T) {
	n, s := Preview("short")
	if n != 5 || s != "short" {
		t.Errorf("got %d %q", n, s)
	}
	n, s = Preview(strings.Repeat("ש", 30) + "😀")
	if n != 31 || s != strings.Repeat("ש", 20) {
		t.Errorf("got %d %q", n, s)
	}
	n, s = Preview(strings.Repeat("😀", 25))
	if n != 25 || s != strings.Repeat("😀", 20) {
		t.Errorf("got %d %q", n, s)
	}
}

func TestPreviewStripsControlAndFormat(t *testing.T) {
	n, s := Preview("a\nb\u202eevil\u200b\x00\ufeffz")
	if n != 12 || s != "abevilz" {
		t.Errorf("got %d %q", n, s)
	}
	_, s = Preview(strings.Repeat("\u202e", 30) + strings.Repeat("x", 30))
	if s != strings.Repeat("x", 20) {
		t.Errorf("got %q", s)
	}
}
