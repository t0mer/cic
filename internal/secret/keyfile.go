package secret

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// LoadOrCreateKey returns the encryption key. A non-empty envValue wins (ParseKey rules).
// Otherwise the hex key in keyPath is used; if the file does not exist it is created with
// 32 random bytes (mode 0600, O_EXCL) and created is true. Two processes racing to create it
// converge on the same key. A corrupt key file is an error; messages never contain key
// material. log may be nil.
func LoadOrCreateKey(envValue, keyPath string, log *slog.Logger) (key []byte, created bool, err error) {
	if strings.TrimSpace(envValue) != "" {
		k, err := ParseKey(envValue)
		if err == nil && keyPath != "" && log != nil {
			if fk, ferr := readKeyFile(keyPath, nil); ferr == nil && !bytes.Equal(fk, k) {
				log.Warn(KeyEnv+" differs from the key file; the environment variable wins, so two-factor secrets encrypted with the key file will not decrypt", "path", keyPath)
			}
		}
		return k, false, err
	}
	if keyPath == "" {
		return nil, false, ErrNoKey
	}
	if k, err := readKeyFile(keyPath, log); err == nil {
		return k, false, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, false, err
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		return nil, false, fmt.Errorf("creating key directory: %w", err)
	}
	k := make([]byte, keySize)
	for {
		if _, err := rand.Read(k); err != nil {
			return nil, false, fmt.Errorf("generating key: %w", err)
		}
		if checkKey(k) == nil {
			break
		}
	}
	// Write the key to a private temp file in the same directory, sync it, then hard-link
	// it into place. Link fails if the target exists, so exactly one process wins, and the
	// published file is always complete: a crash can never leave an empty key file.
	tmp, err := os.CreateTemp(filepath.Dir(keyPath), ".secret.key.*")
	if err != nil {
		return nil, false, fmt.Errorf("creating key file: %w", err)
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return nil, false, fmt.Errorf("creating key file: %w", err)
	}
	if _, err := tmp.WriteString(hex.EncodeToString(k) + "\n"); err != nil {
		tmp.Close()
		return nil, false, fmt.Errorf("writing key file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return nil, false, fmt.Errorf("writing key file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return nil, false, fmt.Errorf("writing key file: %w", err)
	}
	if err := os.Link(tmp.Name(), keyPath); err != nil {
		if errors.Is(err, fs.ErrExist) { // lost the race: use the winner's key
			k, err := readKeyFile(keyPath, log)
			return k, false, err
		}
		return nil, false, fmt.Errorf("installing key file: %w", err)
	}
	return k, true, nil
}

var errEmptyKey = errors.New("empty")

func readKeyFile(path string, log *slog.Logger) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return nil, fmt.Errorf("key file %s is empty (made by hand or a failed copy); restore it from backup, or delete it to generate a new key (only if no two-factor secrets exist): %w", path, errEmptyKey)
	}
	if fi, err := os.Stat(path); err == nil && fi.Mode().Perm()&0o077 != 0 && log != nil {
		log.Warn("secret key file is accessible by group/other; restrict it to mode 0600", "path", path)
	}
	s := strings.TrimSpace(string(b))
	if len(s) != 2*keySize {
		return nil, fmt.Errorf("key file %s is invalid: expected %d hex characters", path, 2*keySize)
	}
	k, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("key file %s is invalid: not hex", path)
	}
	if err := checkKey(k); err != nil {
		return nil, fmt.Errorf("key file %s is invalid: %w", path, err)
	}
	return k, nil
}
