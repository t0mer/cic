package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
)

func TestOpenMigratesOnceAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sub", "cic.db")
	for i := 0; i < 2; i++ {
		db, err := Open(ctx, path)
		if err != nil {
			t.Fatalf("open #%d: %v", i, err)
		}
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("open #%d: %d migrations recorded, want 1", i, n)
		}
		var fk int
		if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
			t.Fatalf("foreign_keys = %d, err %v", fk, err)
		}
		db.Close()
	}
}

func TestMetaRoundTrip(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "cic.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := GetMeta(ctx, db, "k"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	for _, v := range []string{"a", "b"} {
		if err := SetMeta(ctx, db, "k", v); err != nil {
			t.Fatal(err)
		}
		got, err := GetMeta(ctx, db, "k")
		if err != nil || got != v {
			t.Fatalf("got %q, %v; want %q", got, err, v)
		}
	}
}

func memDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", dsn(filepath.Join(t.TempDir(), "m.db")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestOpenPathWithSpecialCharsAndPerms(t *testing.T) {
	ctx := context.Background()
	for _, dir := range []string{"we?ird", "ha#sh", "pct%41", "plain"} {
		t.Run(dir, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), dir, "cic.db")
			db, err := Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			st, err := os.Stat(path)
			if err != nil {
				t.Fatalf("db file not at the requested path: %v", err)
			}
			if st.Mode().Perm() != 0o600 {
				t.Fatalf("mode %v want 0600", st.Mode().Perm())
			}
			var fk int
			if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
				t.Fatalf("pragmas lost: fk=%d err=%v", fk, err)
			}
		})
	}
}

func TestMigrateFailureRollsBack(t *testing.T) {
	ctx := context.Background()
	db := memDB(t)
	fsys := fstest.MapFS{
		"migrations/0001_a.sql": {Data: []byte(`CREATE TABLE a(x);`)},
		"migrations/0002_b.sql": {Data: []byte(`CREATE TABLE b(x); INSERT INTO nope VALUES (1);`)},
	}
	err := migrate(ctx, db, fsys)
	if err == nil || !strings.Contains(err.Error(), "0002_b.sql") {
		t.Fatalf("want error naming 0002_b.sql, got %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = 'b'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("table b survived rollback: n=%d err=%v", n, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("recorded %d migrations, want 1 (only 0001): err=%v", n, err)
	}
}

func TestLoadMigrationsErrors(t *testing.T) {
	tests := []struct {
		name string
		fsys fstest.MapFS
		want string
	}{
		{"duplicate version", fstest.MapFS{
			"migrations/0001_a.sql": {Data: []byte("")},
			"migrations/0001_b.sql": {Data: []byte("")},
		}, "duplicate migration version 1"},
		{"no numeric prefix", fstest.MapFS{"migrations/init.sql": {Data: []byte("")}}, "numeric version"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadMigrations(tt.fsys)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("got %v want containing %q", err, tt.want)
			}
		})
	}
}

func TestMigrateConcurrentStarts(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "c.db")
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			db, err := Open(ctx, path)
			if err == nil {
				db.Close()
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}
