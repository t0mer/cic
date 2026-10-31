// Package auth implements local authentication: argon2id passwords, server-side
// sessions, CSRF protection, role checks and login rate limiting.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

type argonParams struct {
	memKiB  uint32
	time    uint32
	threads uint8
}

const (
	saltLen = 16
	keyLen  = 32

	// MinPasswordLength is the minimum length (in characters) of a new password.
	MinPasswordLength = 12
	maxPasswordBytes  = 256
)

var (
	defaultCost = argonParams{memKiB: 64 * 1024, time: 3, threads: 2}
	cost        = defaultCost

	// floor is the weakest cost a stored hash may declare (OWASP argon2id minimum: 19 MiB,
	// t=2, p=1). A row below it is rejected rather than verified, so a tampered database
	// cannot downgrade the work factor.
	floor = argonParams{memKiB: 19456, time: 2, threads: 1}
	// ceiling bounds the work a corrupt row can request.
	ceiling = argonParams{memKiB: 256 * 1024, time: 10, threads: 16}
)

// UseFastHashingForTests lowers the argon2id cost so tests that create many users stay fast.
// It returns a function restoring the production cost. Never call it outside tests.
func UseFastHashingForTests() (restore func()) {
	oldCost, oldFloor := cost, floor
	cost = argonParams{memKiB: 64, time: 1, threads: 1}
	floor = argonParams{memKiB: 8, time: 1, threads: 1}
	return func() { cost, floor = oldCost, oldFloor }
}

// HashPassword returns a PHC-style argon2id encoding of pw.
func HashPassword(pw string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generating salt: %w", err)
	}
	p := cost
	key := argon2.IDKey([]byte(pw), salt, p.time, p.memKiB, p.threads, keyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, p.memKiB, p.time, p.threads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword reports whether pw matches the encoded hash, in constant time.
func VerifyPassword(pw, encoded string) bool {
	ok, _ := verifyPassword(pw, encoded)
	return ok
}

// verifyPassword also reports whether a matching hash was made with parameters other than
// the current ones and should be recomputed.
func verifyPassword(pw, encoded string) (ok, needsRehash bool) {
	p, salt, want, parsed := parseHash(encoded)
	if !parsed {
		return false, false
	}
	got := argon2.IDKey([]byte(pw), salt, p.time, p.memKiB, p.threads, uint32(len(want)))
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return false, false
	}
	return true, p != cost || len(want) != keyLen
}

// phcRe is anchored on both ends: trailing garbage after any field is rejected.
var phcRe = regexp.MustCompile(`^\$argon2id\$v=(0|[1-9][0-9]*)\$m=([1-9][0-9]*),t=([1-9][0-9]*),p=([1-9][0-9]*)\$([A-Za-z0-9+/]+)\$([A-Za-z0-9+/]+)$`)

func parseHash(encoded string) (p argonParams, salt, key []byte, ok bool) {
	m := phcRe.FindStringSubmatch(encoded)
	if m == nil {
		return p, nil, nil, false
	}
	version, err0 := strconv.ParseUint(m[1], 10, 32)
	mem, err1 := strconv.ParseUint(m[2], 10, 32)
	t, err2 := strconv.ParseUint(m[3], 10, 32)
	th, err3 := strconv.ParseUint(m[4], 10, 8)
	if err0 != nil || err1 != nil || err2 != nil || err3 != nil || version != argon2.Version {
		return p, nil, nil, false
	}
	p = argonParams{memKiB: uint32(mem), time: uint32(t), threads: uint8(th)}
	if p.memKiB < floor.memKiB || p.time < floor.time || p.threads < floor.threads ||
		p.memKiB > ceiling.memKiB || p.time > ceiling.time || p.threads > ceiling.threads {
		return argonParams{}, nil, nil, false
	}
	salt, err1 = base64.RawStdEncoding.DecodeString(m[5])
	key, err2 = base64.RawStdEncoding.DecodeString(m[6])
	if err1 != nil || err2 != nil || len(salt) < 8 || len(key) < 16 || len(key) > 64 {
		return argonParams{}, nil, nil, false
	}
	return p, salt, key, true
}

var (
	dummyOnce sync.Once
	dummyHash string
)

// VerifyDummy burns the same CPU/memory as a real verification. It is used for unknown or
// disabled accounts so response timing does not reveal valid usernames. It always returns false.
func VerifyDummy(pw string) bool {
	dummyOnce.Do(func() { dummyHash, _ = HashPassword("dummy-password-for-timing") })
	_ = VerifyPassword(pw, dummyHash)
	return false
}

// ValidatePassword checks the policy for a new password.
func ValidatePassword(pw, username string) error {
	n := utf8.RuneCountInString(pw)
	switch {
	case n < MinPasswordLength:
		return fmt.Errorf("password must be at least %d characters", MinPasswordLength)
	case len(pw) > maxPasswordBytes:
		return fmt.Errorf("password must be at most %d bytes", maxPasswordBytes)
	case strings.EqualFold(pw, username):
		return errors.New("password must not equal the username")
	case allSameRune(pw):
		return errors.New("password is too simple")
	}
	return nil
}

func allSameRune(s string) bool {
	first, _ := utf8.DecodeRuneInString(s)
	for _, r := range s {
		if r != first {
			return false
		}
	}
	return true
}
