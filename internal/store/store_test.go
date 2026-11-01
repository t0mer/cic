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
		ms, err := loadMigrations(migrationsFS)
		if err != nil {
			t.Fatal(err)
		}
		if n != len(ms) {
			t.Fatalf("open #%d: %d migrations recorded, want %d", i, n, len(ms))
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

func TestMigrateConcurrentStartsStress(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "s.db")
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := 0; i < 32; i++ {
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

func TestDSNFor(t *testing.T) {
	const q = "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)"
	cases := []struct{ goos, path, want string }{
		{"linux", "/data/cic.db", "file:/data/cic.db" + q},
		{"linux", "/data/a b?c#d%e.db", "file:/data/a b%3Fc%23d%25e.db" + q},
		{"windows", `C:\ProgramData\cic\cic.db`, `C:\ProgramData\cic\cic.db` + q},
		{"windows", "cic.db", "cic.db" + q},
	}
	for _, c := range cases {
		if got := dsnFor(c.goos, c.path); got != c.want {
			t.Errorf("dsnFor(%q, %q) = %q, want %q", c.goos, c.path, got, c.want)
		}
	}
}

func TestNormalizePath(t *testing.T) {
	cases := []struct {
		goos, in, want string
		bad            bool
	}{
		{"linux", "/data/a?b.db", "/data/a?b.db", false},
		{"windows", `C:\ProgramData\cic\cic.db`, `C:\ProgramData\cic\cic.db`, false},
		{"windows", "cic.db", "cic.db", false},
		{"windows", `\\?\C:\ProgramData\cic\cic.db`, `C:\ProgramData\cic\cic.db`, false},
		{"windows", `\\?\UNC\srv\share\cic.db`, `\\srv\share\cic.db`, false},
		{"windows", `\\.\C:\x\cic.db`, `C:\x\cic.db`, false},
		{"windows", `\\?\unc\srv\share\cic.db`, `\\srv\share\cic.db`, false},
		{"windows", `\\?\Volume{1234}\x\cic.db`, "", true},
		{"windows", `\\.\pipe\x`, "", true},
		{"windows", `\\?\GLOBALROOT\x\cic.db`, "", true},
		{"windows", `\\?\C:\data.\cic.db`, "", true},
		{"windows", `\\?\C:\data \cic.db`, "", true},
		{"windows", `\\?\C:\data\cic.db.`, "", true},
		{"windows", `\\?\C:\data\..\cic.db`, "", true},
		{"windows", `\\?\C:\data\.\cic.db`, "", true},
		{"windows", `\\?\C:\data/cic.db`, "", true},
		{"windows", `\\?\UNC\srv\share.\cic.db`, "", true},
		{"windows", `\\?\UNC\srv\share \cic.db`, "", true},
		{"windows", `\\?\C:\data\\cic.db`, "", true},
		{"windows", `\\?\C:\data\NUL`, "", true},
		{"windows", `\\?\C:\data\NUL:`, "", true},
		{"windows", `\\?\C:\data\NUL:stream`, "", true},
		{"windows", `\\?\C:\data\cic.db:stream`, "", true},
		{"windows", `\\?\C:\data\COM¹`, "", true},
		{"windows", `\\?\C:\data\lpt².db`, "", true},
		{"windows", `\\?\C:\data\COM³.txt`, "", true},
		{"windows", `\\?\UNC\srv\share\COM¹\cic.db`, "", true},
		{"windows", `\\?\UNC\srv\share\NUL:\cic.db`, "", true},
		{"windows", `\\?\C:\data\CONIN$`, "", true},
		{"windows", `\\?\C:\data\CONOUT$.db`, "", true},
		{"windows", `\\?\C:\data\COM0`, `C:\data\COM0`, false},
		{"windows", `\\?\C:\data\COM10`, `C:\data\COM10`, false},
		{"windows", `\\?\C:\data\com1.db`, "", true},
		{"windows", `\\?\C:\data\LPT9`, "", true},
		{"windows", `\\?\C:\my data\a.b\.hidden.db`, `C:\my data\a.b\.hidden.db`, false},
		{"windows", `\\?\C:\data\console.db`, `C:\data\console.db`, false},
		{"windows", `\\?\UNC\srv\my share\a.b\cic.db`, `\\srv\my share\a.b\cic.db`, false},
		{"windows", `C:\` + strings.Repeat("a", maxWindowsPath-3), `C:\` + strings.Repeat("a", maxWindowsPath-3), false},
		{"windows", `C:\` + strings.Repeat("a", maxWindowsPath-2), "", true},
		{"windows", `\\?\C:\` + strings.Repeat("a", maxWindowsPath-3), `C:\` + strings.Repeat("a", maxWindowsPath-3), false},
		{"windows", `\\?\C:\` + strings.Repeat("a", maxWindowsPath-2), "", true},
		{"windows", `C:\data.\cic.db`, `C:\data.\cic.db`, false},
		{"windows", `C:\a?b\cic.db`, "", true},
		{"windows", `\\?\C:\a*b.db`, "", true},
	}
	for _, c := range cases {
		got, err := normalizePath(c.goos, c.in)
		if c.bad {
			if err == nil {
				t.Errorf("normalizePath(%q, %q) = %q, want an error", c.goos, c.in, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("normalizePath(%q, %q) = %q, %v; want %q", c.goos, c.in, got, err, c.want)
		}
		if c.goos == "windows" && strings.Contains(dsnFor(c.goos, got), "?") && strings.Index(dsnFor(c.goos, got), "?") != len(got) {
			t.Errorf("dsn for %q has a '?' inside the path", got)
		}
	}
}
