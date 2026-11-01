package secret

import (
	"bytes"
	"encoding/hex"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestLoadOrCreateKey(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sub")
	p := filepath.Join(dir, "secret.key")
	k1, created, err := LoadOrCreateKey("", p, nil)
	if err != nil || !created || len(k1) != 32 {
		t.Fatalf("create: %v %v %d", err, created, len(k1))
	}
	fi, _ := os.Stat(p)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("perm %v", fi.Mode().Perm())
	}
	di, _ := os.Stat(dir)
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("dir perm %v", di.Mode().Perm())
	}
	k2, created, err := LoadOrCreateKey("", p, nil)
	if err != nil || created || !bytes.Equal(k1, k2) {
		t.Fatalf("reuse: %v %v", err, created)
	}
}

func TestLoadOrCreateKeyEnvWins(t *testing.T) {
	p := filepath.Join(t.TempDir(), "secret.key")
	env := hex.EncodeToString(key())
	k, created, err := LoadOrCreateKey(env, p, nil)
	if err != nil || created || hex.EncodeToString(k) != env {
		t.Fatal("env not used")
	}
	if _, err := os.Stat(p); err == nil {
		t.Fatal("file created despite env")
	}
	if _, _, err := LoadOrCreateKey("zz", p, nil); err == nil {
		t.Fatal("bad env accepted")
	}
}

func TestLoadOrCreateKeyRace(t *testing.T) {
	p := filepath.Join(t.TempDir(), "secret.key")
	const n = 16
	keys := make([][]byte, n)
	var wg sync.WaitGroup
	var mu sync.Mutex
	made := 0
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			k, c, err := LoadOrCreateKey("", p, nil)
			if err != nil {
				t.Error(err)
				return
			}
			keys[i] = k
			if c {
				mu.Lock()
				made++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if made != 1 {
		t.Fatalf("created %d times", made)
	}
	for _, k := range keys {
		if !bytes.Equal(k, keys[0]) {
			t.Fatal("processes disagree on key")
		}
	}
}

func TestLoadOrCreateKeyCorrupt(t *testing.T) {
	keyReadWait = time.Millisecond
	t.Cleanup(func() { keyReadWait = time.Second })
	d := t.TempDir()
	secretish := strings.Repeat("ab", 10) // wrong length, distinct-looking content
	for name, content := range map[string]string{
		"short": secretish, "nothex": strings.Repeat("zz", 32), "zero": strings.Repeat("00", 32), "empty": "",
	} {
		p := filepath.Join(d, name)
		os.WriteFile(p, []byte(content), 0o600)
		_, created, err := LoadOrCreateKey("", p, nil)
		if err == nil || created {
			t.Fatalf("%s: accepted", name)
		}
		if content != "" && strings.Contains(err.Error(), content) {
			t.Fatalf("%s: error leaks content", name)
		}
	}
}

func TestLoadOrCreateKeyLeavesNoTemp(t *testing.T) {
	d := t.TempDir()
	if _, _, err := LoadOrCreateKey("", filepath.Join(d, "secret.key"), nil); err != nil {
		t.Fatal(err)
	}
	ents, _ := os.ReadDir(d)
	if len(ents) != 1 {
		t.Fatalf("leftover files: %v", ents)
	}
}

func TestLoadOrCreateKeyEnvMismatchWarns(t *testing.T) {
	p := filepath.Join(t.TempDir(), "secret.key")
	if _, _, err := LoadOrCreateKey("", p, nil); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	env := hex.EncodeToString(key())
	if _, _, err := LoadOrCreateKey(env, p, log); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "differs from the key file") || strings.Contains(buf.String(), env) {
		t.Fatalf("log: %s", buf.String())
	}
}

func noLinks(t *testing.T, err error) {
	t.Helper()
	old := linkFile
	linkFile = func(string, string) error { return &os.LinkError{Op: "link", Err: err} }
	t.Cleanup(func() { linkFile = old })
}

func TestLoadOrCreateKeyWithoutHardLinks(t *testing.T) {
	for _, e := range []error{syscall.EPERM, syscall.ENOTSUP, syscall.EXDEV} {
		noLinks(t, e)
		d := t.TempDir()
		p := filepath.Join(d, "secret.key")
		k, created, err := LoadOrCreateKey("", p, nil)
		if err != nil || !created || len(k) != 32 {
			t.Fatalf("%v: %v created=%v", e, err, created)
		}
		if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
			t.Fatalf("mode %v", fi.Mode())
		}
		k2, created, err := LoadOrCreateKey("", p, nil)
		if err != nil || created || !bytes.Equal(k, k2) {
			t.Fatalf("reload: %v", err)
		}
		if ents, _ := os.ReadDir(d); len(ents) != 1 {
			t.Fatalf("leftover files: %v", ents)
		}
	}
}

func TestLoadOrCreateKeyNoLinkRace(t *testing.T) {
	noLinks(t, syscall.EPERM)
	p := filepath.Join(t.TempDir(), "secret.key")
	const n = 16
	keys := make([][]byte, n)
	var wg sync.WaitGroup
	var mu sync.Mutex
	made := 0
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			k, c, err := LoadOrCreateKey("", p, nil)
			if err != nil {
				t.Error(err)
				return
			}
			keys[i] = k
			if c {
				mu.Lock()
				made++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if made != 1 {
		t.Fatalf("created %d times", made)
	}
	for _, k := range keys {
		if !bytes.Equal(k, keys[0]) {
			t.Fatal("processes disagree on key")
		}
	}
}

func TestReadKeyWaitsForWriter(t *testing.T) {
	p := filepath.Join(t.TempDir(), "secret.key")
	key := "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"
	for _, partial := range []string{"", key[:20]} {
		os.Remove(p)
		os.WriteFile(p, []byte(partial), 0o600)
		go func() {
			time.Sleep(100 * time.Millisecond)
			os.WriteFile(p, []byte(key+"\n"), 0o600)
		}()
		k, created, err := LoadOrCreateKey("", p, nil)
		if err != nil || created || hex.EncodeToString(k) != key {
			t.Fatalf("partial %q: %v", partial, err)
		}
	}
}

func TestLinkFailureOtherThanExistIsNotSwallowed(t *testing.T) {
	// A directory in place of the key file makes the exclusive create fail for a reason that
	// is not ErrExist-with-a-key; the error must surface, not hang or succeed.
	noLinks(t, syscall.EPERM)
	keyReadWait = 50 * time.Millisecond
	t.Cleanup(func() { keyReadWait = time.Second })
	p := filepath.Join(t.TempDir(), "sub", "secret.key")
	if err := os.MkdirAll(p, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadOrCreateKey("", p, nil); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("got %v", err)
	}
}
