package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6238 default; HMAC-SHA1 is not weakened by SHA-1 collisions
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	totpPeriod = 30 // seconds
	totpDigits = 6
	totpSkew   = 1 // accepted steps either side of now
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// GenerateSecret returns a new random TOTP secret: 20 bytes, base32 without padding.
func GenerateSecret() (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating totp secret: %w", err)
	}
	return b32.EncodeToString(b), nil
}

// TOTPStep returns the RFC 6238 time step of t.
func TOTPStep(t time.Time) int64 { return t.Unix() / totpPeriod }

// Code computes the 6-digit code of a base32 secret for a time step.
func Code(secret string, step int64) (string, error) {
	key, err := b32.DecodeString(strings.ToUpper(strings.TrimRight(strings.ReplaceAll(secret, " ", ""), "=")))
	if err != nil || len(key) == 0 {
		return "", fmt.Errorf("invalid totp secret")
	}
	return hotp(key, uint64(step)), nil
}

func hotp(key []byte, counter uint64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)
	m := hmac.New(sha1.New, key)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", totpDigits, v%1000000)
}

// Verify checks code against the steps now-1..now+1 and returns the matching step. A step
// <= lastStep is rejected (replay). Spaces in code are ignored.
func Verify(secret, code string, now time.Time, lastStep int64) (step int64, ok bool) {
	code = strings.Map(func(r rune) rune {
		if r == ' ' || r == '\t' {
			return -1
		}
		return r
	}, code)
	if len(code) != totpDigits {
		return 0, false
	}
	cur := TOTPStep(now)
	found := int64(0)
	// Check every candidate without early exit so timing does not reveal which matched.
	for d := int64(-totpSkew); d <= totpSkew; d++ {
		s := cur + d
		want, err := Code(secret, s)
		if err != nil {
			return 0, false
		}
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 && s > lastStep && s > found {
			found = s
		}
	}
	if found == 0 {
		return 0, false
	}
	return found, true
}

// OTPAuthURI builds the otpauth:// provisioning URI for authenticator apps.
func OTPAuthURI(issuer, username, secret string) string {
	label := url.PathEscape(issuer) + ":" + url.PathEscape(username)
	q := url.Values{}
	q.Set("secret", secret)
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", "6")
	q.Set("period", "30")
	return "otpauth://totp/" + label + "?" + q.Encode()
}
