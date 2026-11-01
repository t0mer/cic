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
	path, err := normalizePath(runtime.GOOS, path)
	if err != nil {
		return nil, err
	}
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

func isLetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

func dsn(path string) string { return dsnFor(runtime.GOOS, path) }

// normalizePath prepares a database path for the connection string. On Windows the driver
// treats the first '?' of a plain DSN as the start of the parameters, and the extended-length
// prefix (\\?\C:\... or \\?\UNC\server\share\...) contains one, so the prefix is removed
// (the paths are equivalent below the classic length limit; longer paths need Windows long
// path support). A '?' left anywhere else is not a valid Windows file name character.
func normalizePath(goos, path string) (string, error) {
	if goos != "windows" {
		return path, nil
	}
	prefixed := false
	switch {
	case len(path) >= 8 && strings.EqualFold(path[:8], `\\?\UNC\`):
		path, prefixed = `\\`+path[8:], true
	case strings.HasPrefix(path, `\\?\`), strings.HasPrefix(path, `\\.\`):
		rest := path[4:]
		// Only drive-letter paths are supported behind the prefix; volume GUIDs, device and
		// pipe names would otherwise turn into a meaningless relative path.
		if len(rest) < 3 || !isLetter(rest[0]) || rest[1] != ':' || rest[2] != '\\' {
			return "", fmt.Errorf("database path %q is not supported: use a drive-letter or UNC path", path)
		}
		path, prefixed = rest, true
	}
	if strings.ContainsAny(path, "?*\"<>|") {
		return "", fmt.Errorf("database path %q contains characters that are not valid in a Windows file name", path)
	}
	if prefixed {
		// Behind the prefix Windows takes names literally; without it the path is normalized,
		// which would change the meaning of '/', '.', '..' and names ending in '.' or ' '.
		if strings.Contains(path, "/") {
			return "", fmt.Errorf("database path %q: '/' is not a separator in an extended-length path; use '\\'", path)
		}
		// A colon is only valid as the drive delimiter (alternate data streams and device
		// names such as NUL: are not supported for the database file).
		colons, drive := strings.Count(path, ":"), len(path) > 1 && path[1] == ':'
		if (drive && colons != 1) || (!drive && colons != 0) {
			return "", fmt.Errorf("database path %q contains a ':' outside the drive letter", path)
		}
		if strings.Contains(path[2:], `\\`) { // empty component (the leading pair of a UNC path excepted)
			return "", fmt.Errorf("database path %q has an empty component that Windows would collapse without the extended-length prefix", path)
		}
		for _, part := range strings.Split(path, `\`) {
			if part == "." || part == ".." || (part != "" && (strings.HasSuffix(part, ".") || strings.HasSuffix(part, " "))) || reservedDOSName(part) {
				return "", fmt.Errorf("database path %q has a component (%q) that Windows would read differently without the extended-length prefix", path, part)
			}
		}
	}
	// SQLite's default Windows VFS limits path names to about 1040 bytes (MAX_PATH*4 in UTF-8;
	// relative paths are resolved inside SQLite, so a long working directory is not covered).
	// Refuse longer paths early with a clear message; the limit is applied after the prefix is
	// stripped.
	if len(path) > maxWindowsPath {
		return "", fmt.Errorf("database path is %d bytes long; the maximum supported on Windows is %d", len(path), maxWindowsPath)
	}
	return path, nil
}

// reservedDOSName reports whether a path component names a DOS device (CON, PRN, AUX, NUL,
// COM1-9, LPT1-9, with or without an extension): behind the extended-length prefix such a name
// is an ordinary file, without it Windows opens the device.
func reservedDOSName(part string) bool {
	base := part
	if i := strings.IndexAny(base, ".:"); i >= 0 { // NUL.db, NUL:stream, NUL:
		base = base[:i]
	}
	base = strings.ToUpper(strings.TrimRight(base, " "))
	switch base {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$":
		return true
	}
	// COM1-9 and LPT1-9, and the superscript digits ¹ ² ³ that Windows also accepts.
	for _, p := range []string{"COM", "LPT"} {
		if rest, ok := strings.CutPrefix(base, p); ok {
			switch rest {
			case "1", "2", "3", "4", "5", "6", "7", "8", "9", "\u00b9", "\u00b2", "\u00b3":
				return true
			}
		}
	}
	return false
}

// maxWindowsPath keeps well below SQLite's 1040-byte Windows VFS pathname limit.
const maxWindowsPath = 1000

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
