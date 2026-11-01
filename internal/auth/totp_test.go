package auth

import (
	"strings"
	"testing"
	"time"
)

// RFC 6238 appendix B SHA-1 vectors (8 digits there; we use the last 6).
func TestTOTPRFCVectors(t *testing.T) {
	secret := b32.EncodeToString([]byte("12345678901234567890"))
	for _, v := range []struct {
		unix int64
		code string
	}{{59, "287082"}, {1111111109, "081804"}, {1111111111, "050471"}, {1234567890, "005924"}, {2000000000, "279037"}, {20000000000, "353130"}} {
		got, err := Code(secret, v.unix/30)
		if err != nil || got != v.code {
			t.Fatalf("t=%d got %q want %q (%v)", v.unix, got, v.code, err)
		}
	}
}

func TestTOTPVerifySkewReplay(t *testing.T) {
	secret, err := GenerateSecret()
	if err != nil || len(secret) != 32 || strings.Contains(secret, "=") {
		t.Fatalf("secret %q %v", secret, err)
	}
	now := time.Unix(1700000000, 0)
	cur := TOTPStep(now)
	for d, want := range map[int64]bool{-2: false, -1: true, 0: true, 1: true, 2: false} {
		c, _ := Code(secret, cur+d)
		step, ok := Verify(secret, c, now, 0)
		if ok != want || (ok && step != cur+d) {
			t.Fatalf("skew %d: ok=%v step=%d", d, ok, step)
		}
	}
	c, _ := Code(secret, cur)
	if _, ok := Verify(secret, c[:3]+" "+c[3:], now, 0); !ok {
		t.Fatal("spaces not tolerated")
	}
	if _, ok := Verify(secret, c, now, cur); ok {
		t.Fatal("replay accepted")
	}
	if _, ok := Verify(secret, c, now, cur-1); !ok {
		t.Fatal("fresh step rejected")
	}
	for _, bad := range []string{"", "12345", "1234567", "abcdef"} {
		if _, ok := Verify(secret, bad, now, 0); ok {
			t.Fatalf("%q accepted", bad)
		}
	}
	if _, ok := Verify("!!!", c, now, 0); ok {
		t.Fatal("bad secret accepted")
	}
}

func TestOTPAuthURI(t *testing.T) {
	u := OTPAuthURI("CIC App", "a b@c", "ABC234")
	if !strings.HasPrefix(u, "otpauth://totp/CIC%20App:a%20b@c?") || !strings.Contains(u, "secret=ABC234") || !strings.Contains(u, "issuer=CIC+App") {
		t.Fatal(u)
	}
}
