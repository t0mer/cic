// Package store provides SQLite persistence with embedded migrations.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Open opens (creating if needed) the SQLite database at path and applies migrations.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("creating database directory: %w", err)
	}
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}
	if err := connect(ctx, db); err != nil {
		db.Close()
		return nil, fmt.Errorf("connecting to database: %w", err)
	}
	if err := migrate(ctx, db, migrationsFS); err != nil {
		db.Close()
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		db.Close()
		return nil, fmt.Errorf("restricting database permissions: %w", err)
	}
	return db, nil
}

// connect opens the first connection. Switching a fresh database to WAL can fail with
// SQLITE_BUSY immediately, ignoring busy_timeout, while another process is doing the same
// switch; that connection is discarded, so retrying with a short bounded backoff is safe.
// Once the file is in WAL mode the pragma is a no-op for every later connection.
func connect(ctx context.Context, db *sql.DB) error {
	var err error
	for i := range 40 {
		if err = db.PingContext(ctx); err == nil || !strings.Contains(err.Error(), "SQLITE_BUSY") {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(10+5*i) * time.Millisecond):
		}
	}
	return err
}

// pathEscaper protects the characters that would otherwise end or corrupt the
// path part of a SQLite URI; SQLite decodes %XX in URI paths itself.
var pathEscaper = strings.NewReplacer("%", "%25", "?", "%3F", "#", "%23")

func dsn(path string) string { return dsnFor(runtime.GOOS, path) }

// busy_timeout must come first: pragmas run in order on every new connection, and
// switching to WAL takes a lock that fails at once (SQLITE_BUSY) without a busy handler.
const dsnQuery = "_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)"

// dsnFor builds the connection string. On Windows the path is passed as a plain file name
// (the driver strips the query from a DSN without the file: prefix and opens the rest as is),
// because a Windows path such as C:\data\cic.db is not a valid URI path.
func dsnFor(goos, path string) string {
	if goos == "windows" {
		return path + "?" + dsnQuery
	}
	u := url.URL{Scheme: "file", Opaque: pathEscaper.Replace(path), RawQuery: dsnQuery}
	return u.String()
}

type migration struct {
	version int
	name    string
	sql     string
}

func loadMigrations(fsys fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, "migrations")
	if err != nil {
		return nil, fmt.Errorf("reading migrations: %w", err)
	}
	var ms []migration
	for _, e := range entries {
		name := e.Name()
		prefix, _, _ := strings.Cut(name, "_")
		v, err := strconv.Atoi(prefix)
		if err != nil {
			return nil, fmt.Errorf("migration %q has no numeric version prefix", name)
		}
		b, err := fs.ReadFile(fsys, "migrations/"+name)
		if err != nil {
			return nil, fmt.Errorf("reading migration %q: %w", name, err)
		}
		ms = append(ms, migration{version: v, name: name, sql: string(b)})
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].version < ms[j].version })
	for i := 1; i < len(ms); i++ {
		if ms[i].version == ms[i-1].version {
			return nil, fmt.Errorf("duplicate migration version %d: %q and %q", ms[i].version, ms[i-1].name, ms[i].name)
		}
	}
	return ms, nil
}

func migrate(ctx context.Context, db *sql.DB, fsys fs.FS) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY, applied_at TEXT)`); err != nil {
		return fmt.Errorf("creating schema_migrations: %w", err)
	}
	ms, err := loadMigrations(fsys)
	if err != nil {
		return err
	}
	for _, m := range ms {
		if err := apply(ctx, db, m); err != nil {
			return fmt.Errorf("applying migration %s: %w", m.name, err)
		}
	}
	return nil
}

// apply runs m unless it is already recorded. The check and the apply share one
// BEGIN IMMEDIATE transaction, so concurrent starters serialize and the loser
// sees the migration as already applied.
func apply(ctx context.Context, db *sql.DB, m migration) (err error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_, _ = conn.ExecContext(context.WithoutCancel(ctx), `ROLLBACK`)
		}
	}()
	var n int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, m.version).Scan(&n); err != nil {
		return fmt.Errorf("checking migration %d: %w", m.version, err)
	}
	if n > 0 {
		_, err = conn.ExecContext(ctx, `COMMIT`)
		return err
	}
	if _, err := conn.ExecContext(ctx, m.sql); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)`,
		m.version, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, `COMMIT`)
	return err
}

// ErrNotFound is returned when a requested record does not exist.
var ErrNotFound = errors.New("not found")

// GetMeta returns the meta value for key, or ErrNotFound.
func GetMeta(ctx context.Context, db *sql.DB, key string) (string, error) {
	var v string
	err := db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("reading meta %q: %w", key, err)
	}
	return v, nil
}

// SetMeta upserts a meta value.
func SetMeta(ctx context.Context, db *sql.DB, key, value string) error {
	if _, err := db.ExecContext(ctx, `INSERT INTO meta(key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value); err != nil {
		return fmt.Errorf("writing meta %q: %w", key, err)
	}
	return nil
}
