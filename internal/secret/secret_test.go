package secret

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
)

const ctxA, ctxB = "channels.config", "users.totp"

func key() []byte { k := make([]byte, 32); _, _ = rand.Read(k); return k }

func TestRoundTrip(t *testing.T) {
	k := key()
	for _, pt := range [][]byte{[]byte("hunter2"), {}, bytes.Repeat([]byte("x"), 10000)} {
		ct, err := Encrypt(k, pt, ctxA)
		if err != nil {
			t.Fatal(err)
		}
		if ct[0] != 2 || bytes.Contains(ct, pt) && len(pt) > 0 {
			t.Fatal("bad envelope")
		}
		got, err := Decrypt(k, ct, ctxA)
		if err != nil || !bytes.Equal(got, pt) {
			t.Fatalf("round trip: %v", err)
		}
	}
	a, _ := Encrypt(k, []byte("same"), ctxA)
	b, _ := Encrypt(k, []byte("same"), ctxA)
	if bytes.Equal(a, b) {
		t.Fatal("nonce reused")
	}
	s, _ := EncryptString(k, "text", ctxA)
	if got, err := DecryptString(k, s, ctxA); err != nil || got != "text" {
		t.Fatalf("string round trip: %q %v", got, err)
	}
}

func TestTamperAndWrongKey(t *testing.T) {
	k := key()
	ct, _ := Encrypt(k, []byte("secret value"), ctxA)
	for i := range ct {
		bad := bytes.Clone(ct)
		bad[i] ^= 0x01
		if _, err := Decrypt(k, bad, ctxA); err == nil {
			t.Fatalf("tampering byte %d undetected", i)
		}
	}
	if _, err := Decrypt(key(), ct, ctxA); err == nil {
		t.Fatal("wrong key accepted")
	}
	if _, err := Decrypt(k, ct[:10], ctxA); err == nil {
		t.Fatal("truncated accepted")
	}
	if _, err := DecryptString(k, "!!!", ctxA); err == nil {
		t.Fatal("bad base64 accepted")
	}
}

func TestParseKey(t *testing.T) {
	raw := key()
	cases := []struct {
		name, in string
		ok       bool
	}{
		{"hex", hex.EncodeToString(raw), true},
		{"base64", base64.StdEncoding.EncodeToString(raw), true},
		{"raw base64", base64.RawStdEncoding.EncodeToString(raw), true},
		{"url base64", base64.URLEncoding.EncodeToString(raw), true},
		{"whitespace", " " + hex.EncodeToString(raw) + "\n", true},
		{"empty", "", false},
		{"short hex", hex.EncodeToString(raw[:16]), false},
		{"long base64", base64.StdEncoding.EncodeToString(append(raw, 1)), false},
		{"non hex 64 chars", string(bytes.Repeat([]byte("z"), 64)), false},
		{"passphrase", "correct horse battery staple", false},
	}
	for _, c := range cases {
		k, err := ParseKey(c.in)
		if (err == nil) != c.ok || (c.ok && !bytes.Equal(k, raw)) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	if _, err := Encrypt(raw[:5], nil, ctxA); err == nil {
		t.Fatal("short key accepted by Encrypt")
	}
	t.Setenv(KeyEnv, "")
	if _, err := KeyFromEnv(); err != ErrNoKey {
		t.Fatalf("unset: %v", err)
	}
	t.Setenv(KeyEnv, hex.EncodeToString(raw))
	if _, err := KeyFromEnv(); err != nil {
		t.Fatal(err)
	}
}

func TestContextBinding(t *testing.T) {
	k := key()
	ct, _ := Encrypt(k, []byte("v"), ctxA)
	if _, err := Decrypt(k, ct, ctxB); err == nil {
		t.Fatal("ciphertext opened under another context")
	}
	if _, err := Decrypt(k, ct, ""); err == nil {
		t.Fatal("ciphertext opened without its context")
	}
	s, _ := EncryptString(k, "v", ctxA)
	if _, err := DecryptString(k, s, ctxB); err == nil {
		t.Fatal("string ciphertext opened under another context")
	}
	// a v1 envelope (old format) is refused
	old := bytes.Clone(ct)
	old[0] = 1
	if _, err := Decrypt(k, old, ctxA); err == nil {
		t.Fatal("version 1 accepted")
	}
}

func TestWeakKeysRejected(t *testing.T) {
	weak := map[string][]byte{
		"zero":        make([]byte, 32),
		"repeated":    bytes.Repeat([]byte{0xAB}, 32),
		"15 distinct": bytes.Repeat([]byte("abcdefghijklmno"), 3)[:32],
		"counter low": []byte("0123456789abcde0123456789abcde01"),
	}
	for name, k := range weak {
		if _, err := Encrypt(k, []byte("x"), ctxA); err == nil {
			t.Errorf("%s: Encrypt accepted a weak key", name)
		}
		_, err := ParseKey(hex.EncodeToString(k))
		if err == nil {
			t.Errorf("%s: ParseKey accepted a weak key", name)
		} else if strings.Contains(err.Error(), hex.EncodeToString(k)) || strings.Contains(err.Error(), base64.StdEncoding.EncodeToString(k)) {
			t.Errorf("%s: error echoes the key: %v", name, err)
		}
		if _, err := ParseKey(base64.StdEncoding.EncodeToString(k)); err == nil {
			t.Errorf("%s: ParseKey(base64) accepted a weak key", name)
		}
	}
	ok := []byte("0123456789abcdef0123456789abcdef") // exactly 16 distinct bytes
	if _, err := Encrypt(ok, []byte("x"), ctxA); err != nil {
		t.Errorf("16 distinct bytes rejected: %v", err)
	}
}
