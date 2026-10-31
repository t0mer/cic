// Package secret encrypts secrets at rest with AES-256-GCM.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
)

// KeyEnv is the environment variable holding the 32-byte key (64 hex chars or base64).
const KeyEnv = "CIC_SECRET_KEY"

const (
	// version 2 binds a caller-supplied context string as additional authenticated data;
	// version 1 (no context) is no longer accepted.
	version byte = 2
	keySize      = 32

	minDistinctBytes = 16
)

// ErrNoKey is returned when KeyEnv is unset.
var ErrNoKey = errors.New(KeyEnv + " is not set")

// checkKey rejects keys that are obviously not random: all zero, or with fewer than
// minDistinctBytes distinct byte values. Errors never include key material.
func checkKey(b []byte) error {
	var seen [256]bool
	distinct := 0
	for _, c := range b {
		if !seen[c] {
			seen[c] = true
			distinct++
		}
	}
	switch {
	case distinct == 1 && b[0] == 0:
		return errors.New("secret key is all zero bytes")
	case distinct < minDistinctBytes:
		return fmt.Errorf("secret key is not random enough (fewer than %d distinct byte values)", minDistinctBytes)
	}
	return nil
}

// ParseKey decodes a 32-byte key given as 64 hex characters or base64 (std or URL, padded or not).
func ParseKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, ErrNoKey
	}
	if len(s) == 2*keySize {
		if b, err := hex.DecodeString(s); err == nil {
			if err := checkKey(b); err != nil {
				return nil, fmt.Errorf("%s: %w", KeyEnv, err)
			}
			return b, nil
		}
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil && len(b) == keySize {
			if err := checkKey(b); err != nil {
				return nil, fmt.Errorf("%s: %w", KeyEnv, err)
			}
			return b, nil
		}
	}
	return nil, fmt.Errorf("%s must be %d bytes encoded as %d hex characters or base64", KeyEnv, keySize, 2*keySize)
}

// KeyFromEnv reads and parses KeyEnv.
func KeyFromEnv() ([]byte, error) { return ParseKey(os.Getenv(KeyEnv)) }

func gcm(key []byte) (cipher.AEAD, error) {
	if len(key) != keySize {
		return nil, fmt.Errorf("secret key must be %d bytes, got %d", keySize, len(key))
	}
	if err := checkKey(key); err != nil {
		return nil, err
	}
	b, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(b)
}

// aad is the authenticated associated data: the version byte followed by the context.
func aad(context string) []byte { return append([]byte{version}, context...) }

// Encrypt returns version(1) || nonce(12) || ciphertext||tag. The version byte and context
// (for example "table.column") are authenticated but not stored: Decrypt must be given the
// same context, so a ciphertext copied into another column or table fails to open.
func Encrypt(key, plaintext []byte, context string) ([]byte, error) {
	g, err := gcm(key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 1, 1+g.NonceSize()+len(plaintext)+g.Overhead())
	out[0] = version
	nonce := make([]byte, g.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generating nonce: %w", err)
	}
	out = append(out, nonce...)
	return g.Seal(out, nonce, plaintext, aad(context)), nil
}

// Decrypt reverses Encrypt and fails on any tampering, wrong key, wrong context or unknown version.
func Decrypt(key, envelope []byte, context string) ([]byte, error) {
	g, err := gcm(key)
	if err != nil {
		return nil, err
	}
	if len(envelope) < 1+g.NonceSize()+g.Overhead() {
		return nil, errors.New("secret envelope too short")
	}
	if envelope[0] != version {
		return nil, fmt.Errorf("unsupported secret envelope version %d", envelope[0])
	}
	nonce, ct := envelope[1:1+g.NonceSize()], envelope[1+g.NonceSize():]
	pt, err := g.Open(nil, nonce, ct, aad(context))
	if err != nil {
		return nil, errors.New("decrypting secret: authentication failed")
	}
	return pt, nil
}

// EncryptString is Encrypt with a base64 string result, for text columns.
func EncryptString(key []byte, plaintext, context string) (string, error) {
	b, err := Encrypt(key, []byte(plaintext), context)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// DecryptString reverses EncryptString.
func DecryptString(key []byte, s, context string) (string, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return "", errors.New("secret is not valid base64")
	}
	pt, err := Decrypt(key, b, context)
	return string(pt), err
}
