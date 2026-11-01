package secret

import (
	"bytes"
	"encoding/hex"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
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
