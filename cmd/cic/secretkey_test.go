package main

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadSecretKeyStartup(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(blocked, "secret.key") // parent is a file: the key cannot be created

	k, err := loadSecretKey("", keyPath, log)
	if err != nil || k != nil {
		t.Fatalf("unusable key location must not stop startup: key=%v err=%v", k, err)
	}
	if !strings.Contains(buf.String(), "level=ERROR") {
		t.Fatalf("no ERROR logged: %s", buf.String())
	}

	if _, err := loadSecretKey("not a valid key", keyPath, log); err == nil {
		t.Fatal("an invalid CIC_SECRET_KEY must stop startup")
	}

	good := filepath.Join(t.TempDir(), "secret.key")
	if k, err := loadSecretKey("", good, log); err != nil || len(k) != 32 {
		t.Fatalf("normal path: %v", err)
	}
}
