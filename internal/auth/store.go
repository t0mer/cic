package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Role is a user's permission level.
type Role string

// Roles. Admin may do everything; viewer is read-only.
const (
	RoleAdmin  Role = "admin"
	RoleViewer Role = "viewer"
)

// Valid reports whether r is a known role.
func (r Role) Valid() bool { return r == RoleAdmin || r == RoleViewer }

// Session lifetimes.
const (
	DefaultIdleTimeout      = 12 * time.Hour
	DefaultAbsoluteLifetime = 7 * 24 * time.Hour
	touchEvery              = time.Minute
	maxUserAgent            = 200

	// MaxSessionsPerUser caps concurrent sessions; creating one more evicts the least
	// recently used.
	MaxSessionsPerUser = 10
	// MaxStreamsPerUser caps concurrent event streams per user.
	MaxStreamsPerUser = 5
)

// Errors returned by the store.
var (
	ErrUserExists  = errors.New("user already exists")
	ErrUserMissing = errors.New("user not found")
	ErrNoSession   = errors.New("no valid session")
	ErrBadUsername = errors.New("username must match " + usernamePattern)
	ErrBadRole     = errors.New("role must be admin or viewer")
	// ErrLastAdmin is returned by UpdateUser when the change would leave no active admin.
	ErrLastAdmin = errors.New("the last active admin cannot be disabled or demoted")

	// ErrTooManyStreams is returned by Watch when a user already has MaxStreamsPerUser streams.
	ErrTooManyStreams = errors.New("too many open event streams")
)

const usernamePattern = `^[A-Za-z0-9._-]{3,64}$`

var usernameRe = regexp.MustCompile(usernamePattern)

// ValidUsername reports whether name is an acceptable username.
func ValidUsername(name string) bool { return usernameRe.MatchString(name) }

// User is an account, without its password hash.
type User struct {
	ID                 int64
	Username           string
	Role               Role
	MustChangePassword bool
	Disabled           bool
	CreatedAt          time.Time
	LastLogin          *time.Time
	TOTPEnabled        bool // two-factor login is on
}

// Session is a server-side login session.
type Session struct {
	IDHash    string
	UserID    int64
	CSRFToken string
	CreatedAt time.Time
	LastSeen  time.Time
	UserAgent string
}

// Store persists users and sessions.
type Store struct {
	db               *sql.DB
	now              func() time.Time
	IdleTimeout      time.Duration
	AbsoluteLifetime time.Duration

	wmu      sync.Mutex
	watchers map[*watcher]struct{}
	key      []byte // encrypts TOTP secrets; nil disables the TOTP methods

	afterLoginRead  func() // test seam: runs in createSession just before the insert
	afterEnableRead func() // test seam: runs in EnableTOTP between the read and the write
}

// watcher is one open event stream bound to a session.
type watcher struct {
	userID int64
	idHash string
	ch     chan struct{}
}

// Watch registers an event stream for the session. The returned channel is closed when the
// session ends in this process (deleted, evicted, password changed, user disabled); stop
// unregisters. A user may hold at most MaxStreamsPerUser streams.
func (s *Store) Watch(userID int64, idHash string) (<-chan struct{}, func(), error) {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	n := 0
	for w := range s.watchers {
		if w.userID == userID {
			n++
		}
	}
	if n >= MaxStreamsPerUser {
		return nil, nil, ErrTooManyStreams
	}
	w := &watcher{userID: userID, idHash: idHash, ch: make(chan struct{})}
	s.watchers[w] = struct{}{}
	return w.ch, func() {
		s.wmu.Lock()
		delete(s.watchers, w)
		s.wmu.Unlock()
	}, nil
}

// endWatchers signals the streams of one session (idHash set) or of every session of a
// user (idHash empty).
func (s *Store) endWatchers(userID int64, idHash string) {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	for w := range s.watchers {
		if (idHash != "" && w.idHash == idHash) || (idHash == "" && w.userID == userID) {
			close(w.ch)
			delete(s.watchers, w)
		}
	}
}

// NewStore returns a Store on db. now may be nil (time.Now); tests inject a clock.
func NewStore(db *sql.DB, now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{db: db, now: now, IdleTimeout: DefaultIdleTimeout, AbsoluteLifetime: DefaultAbsoluteLifetime,
		watchers: map[*watcher]struct{}{}}
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

const userCols = `id, username, role, must_change_password, disabled, created_at, last_login, totp_enabled`

type scanner interface{ Scan(...any) error }

func scanUser(sc scanner, extra ...any) (User, error) {
	var u User
	var role string
	var must, dis, totp int
	var created int64
	var last sql.NullInt64
	dest := append([]any{&u.ID, &u.Username, &role, &must, &dis, &created, &last, &totp}, extra...)
	if err := sc.Scan(dest...); err != nil {
		return User{}, err
	}
	u.Role, u.MustChangePassword, u.Disabled = Role(role), must != 0, dis != 0
	u.TOTPEnabled = totp != 0
	u.CreatedAt = time.Unix(created, 0).UTC()
	if last.Valid {
		t := time.Unix(last.Int64, 0).UTC()
		u.LastLogin = &t
	}
	return u, nil
}

// CountUsers returns the number of accounts.
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// CreateUser validates and inserts a user. The password policy is enforced here.
func (s *Store) CreateUser(ctx context.Context, username, password string, role Role, mustChange bool) (User, error) {
	if !ValidUsername(username) {
		return User{}, ErrBadUsername
	}
	if !role.Valid() {
		return User{}, ErrBadRole
	}
	if err := ValidatePassword(password, username); err != nil {
		return User{}, err
	}
	hash, err := HashPasswordContext(ctx, password)
	if err != nil {
		return User{}, err
	}
	now := s.now().Unix()
	res, err := s.db.ExecContext(ctx, `INSERT INTO users(username, password_hash, role, must_change_password, disabled, created_at)
		VALUES (?, ?, ?, ?, 0, ?)`, username, hash, string(role), b2i(mustChange), now)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return User{}, ErrUserExists
		}
		return User{}, fmt.Errorf("inserting user: %w", err)
	}
	id, _ := res.LastInsertId()
	return User{ID: id, Username: username, Role: role, MustChangePassword: mustChange, CreatedAt: time.Unix(now, 0).UTC()}, nil
}

// UserByName returns the user and its password hash (case-insensitive name match).
func (s *Store) UserByName(ctx context.Context, username string) (User, string, error) {
	var hash string
	u, err := scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+`, password_hash FROM users WHERE username = ?`, username), &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, "", ErrUserMissing
	}
	if err != nil {
		return User{}, "", fmt.Errorf("reading user: %w", err)
	}
	return u, hash, nil
}

// ListUsers returns all users ordered by name.
func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+userCols+` FROM users ORDER BY username`)
	if err != nil {
		return nil, fmt.Errorf("listing users: %w", err)
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// SetPassword replaces a user's password and drops all of the user's sessions.
func (s *Store) SetPassword(ctx context.Context, userID int64, username, password string, mustChange bool) error {
	if err := ValidatePassword(password, username); err != nil {
		return err
	}
	hash, err := HashPasswordContext(ctx, password)
	if err != nil {
		return err
	}
	return s.setHash(ctx, userID, hash, mustChange, "")
}

// setHash updates the hash; sessions of the user are deleted except keepHash (if non-empty).
func (s *Store) setHash(ctx context.Context, userID int64, hash string, mustChange bool, keepHash string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	res, err := tx.ExecContext(ctx, `UPDATE users SET password_hash = ?, must_change_password = ? WHERE id = ?`, hash, b2i(mustChange), userID)
	if err != nil {
		return fmt.Errorf("updating password: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrUserMissing
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ? AND id_hash <> ?`, userID, keepHash); err != nil {
		return fmt.Errorf("dropping sessions: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if keepHash == "" {
		s.endWatchers(userID, "")
	}
	return nil
}

// replaceHash swaps in an upgraded hash for the same password. It is a compare-and-set on
// the old hash (a concurrent password change wins) and leaves sessions alone.
func (s *Store) replaceHash(ctx context.Context, userID int64, oldHash, newHash string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET password_hash = ? WHERE id = ? AND password_hash = ?`, newHash, userID, oldHash)
	return err
}

// SetDisabled enables or disables a user; disabling drops the user's sessions.
func (s *Store) SetDisabled(ctx context.Context, userID int64, disabled bool) error {
	res, err := s.db.ExecContext(ctx, `UPDATE users SET disabled = ? WHERE id = ?`, b2i(disabled), userID)
	if err != nil {
		return fmt.Errorf("updating user: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrUserMissing
	}
	if disabled {
		return s.DeleteUserSessions(ctx, userID)
	}
	return nil
}

// TouchLogin records a successful login.
func (s *Store) TouchLogin(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET last_login = ? WHERE id = ?`, s.now().Unix(), userID)
	return err
}

func randToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("reading random bytes: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func trimUA(ua string) string {
	if len(ua) > maxUserAgent {
		ua = ua[:maxUserAgent]
	}
	return strings.ToValidUTF8(ua, "")
}

// CreateSession starts a session and returns its secret token (shown only once).
func (s *Store) CreateSession(ctx context.Context, userID int64, userAgent string) (token string, sess Session, err error) {
	return s.createSession(ctx, userID, userAgent, false)
}

// createSession is CreateSession; with only2FAOff it inserts only while the user has no
// second factor and returns ErrSecondFactorRequired otherwise.
func (s *Store) createSession(ctx context.Context, userID int64, userAgent string, only2FAOff bool) (token string, sess Session, err error) {
	if token, err = randToken(32); err != nil {
		return "", Session{}, err
	}
	csrf, err := randToken(32)
	if err != nil {
		return "", Session{}, err
	}
	now := s.now().Unix()
	sess = Session{IDHash: hashToken(token), UserID: userID, CSRFToken: csrf,
		CreatedAt: time.Unix(now, 0).UTC(), LastSeen: time.Unix(now, 0).UTC(), UserAgent: trimUA(userAgent)}
	if s.afterLoginRead != nil {
		s.afterLoginRead()
	}
	// A single statement decides on the live two-factor flag, so a login that read the user
	// before two-factor was enabled cannot slip a password-only session in afterwards (the
	// enable flow sets the flag first and only then revokes the other sessions).
	res, err := s.db.ExecContext(ctx, `INSERT INTO sessions(id_hash, user_id, csrf_token, created_at, last_seen, user_agent)
		SELECT ?, ?, ?, ?, ?, ? WHERE ? = 0 OR EXISTS (SELECT 1 FROM users WHERE id = ? AND totp_enabled = 0)`,
		sess.IDHash, userID, csrf, now, now, sess.UserAgent, only2FAOff, userID)
	if err != nil {
		return "", Session{}, fmt.Errorf("inserting session: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", Session{}, ErrSecondFactorRequired
	}
	if err := s.evictOverCap(ctx, userID); err != nil {
		return "", Session{}, err
	}
	return token, sess, nil
}

// evictOverCap deletes the user's least recently used sessions beyond MaxSessionsPerUser.
func (s *Store) evictOverCap(ctx context.Context, userID int64) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id_hash FROM sessions WHERE user_id = ?
		ORDER BY last_seen DESC, created_at DESC, rowid DESC LIMIT -1 OFFSET ?`, userID, MaxSessionsPerUser)
	if err != nil {
		return fmt.Errorf("listing sessions: %w", err)
	}
	var drop []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			rows.Close()
			return err
		}
		drop = append(drop, h)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, h := range drop {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id_hash = ?`, h); err != nil {
			return fmt.Errorf("evicting session: %w", err)
		}
		s.endWatchers(userID, h)
	}
	return nil
}

// Lookup resolves a session token to its session and user, enforcing idle and absolute
// expiry (expired sessions are deleted) and refreshing last_seen.
func (s *Store) Lookup(ctx context.Context, token string) (Session, User, error) {
	return s.lookup(ctx, token, true)
}

// Check is like Lookup but does not refresh last_seen.
func (s *Store) Check(ctx context.Context, token string) (Session, User, error) {
	return s.lookup(ctx, token, false)
}

func (s *Store) lookup(ctx context.Context, token string, touch bool) (Session, User, error) {
	if token == "" || len(token) > 128 {
		return Session{}, User{}, ErrNoSession
	}
	var sess Session
	var created, seen int64
	row := s.db.QueryRowContext(ctx, `SELECT u.id, u.username, u.role, u.must_change_password, u.disabled, u.created_at, u.last_login, u.totp_enabled,
		s.id_hash, s.csrf_token, s.created_at, s.last_seen, s.user_agent
		FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.id_hash = ?`, hashToken(token))
	u, err := scanUser(row, &sess.IDHash, &sess.CSRFToken, &created, &seen, &sess.UserAgent)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, User{}, ErrNoSession
	}
	if err != nil {
		return Session{}, User{}, fmt.Errorf("reading session: %w", err)
	}
	sess.UserID = u.ID
	sess.CreatedAt, sess.LastSeen = time.Unix(created, 0).UTC(), time.Unix(seen, 0).UTC()
	now := s.now()
	if u.Disabled || now.Sub(sess.LastSeen) > s.IdleTimeout || now.Sub(sess.CreatedAt) > s.AbsoluteLifetime {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id_hash = ?`, sess.IDHash)
		s.endWatchers(u.ID, sess.IDHash)
		return Session{}, User{}, ErrNoSession
	}
	if touch && now.Sub(sess.LastSeen) >= touchEvery {
		if _, err := s.db.ExecContext(ctx, `UPDATE sessions SET last_seen = ? WHERE id_hash = ?`, now.Unix(), sess.IDHash); err == nil {
			sess.LastSeen = time.Unix(now.Unix(), 0).UTC()
		}
	}
	return sess, u, nil
}

// DeleteSession removes the session for token (no error if it does not exist).
func (s *Store) DeleteSession(ctx context.Context, token string) error {
	h := hashToken(token)
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id_hash = ?`, h)
	s.endWatchers(0, h)
	return err
}

// DeleteUserSessions removes all sessions of a user.
func (s *Store) DeleteUserSessions(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID)
	s.endWatchers(userID, "")
	return err
}

// Rotate gives the session a new ID and CSRF token, keeping its creation time, and
// returns the new token. The old token stops working.
func (s *Store) Rotate(ctx context.Context, oldToken string) (token string, sess Session, err error) {
	old, _, err := s.lookup(ctx, oldToken, false)
	if err != nil {
		return "", Session{}, err
	}
	return s.rotateFrom(ctx, s.db, old)
}

type execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func (s *Store) rotateFrom(ctx context.Context, x execer, old Session) (string, Session, error) {
	token, err := randToken(32)
	if err != nil {
		return "", Session{}, err
	}
	csrf, err := randToken(32)
	if err != nil {
		return "", Session{}, err
	}
	now := s.now().Unix()
	res, err := x.ExecContext(ctx, `UPDATE sessions SET id_hash = ?, csrf_token = ?, last_seen = ? WHERE id_hash = ?`,
		hashToken(token), csrf, now, old.IDHash)
	if err != nil {
		return "", Session{}, fmt.Errorf("rotating session: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", Session{}, ErrNoSession
	}
	old.IDHash, old.CSRFToken, old.LastSeen = hashToken(token), csrf, time.Unix(now, 0).UTC()
	return token, old, nil
}

// ChangePassword sets a new password for the user, ends every other session of that user
// and rotates the session identified by curToken to a new ID. It returns the new token.
func (s *Store) ChangePassword(ctx context.Context, userID int64, username, newPassword, curToken string) (string, Session, error) {
	if err := ValidatePassword(newPassword, username); err != nil {
		return "", Session{}, err
	}
	hash, err := HashPasswordContext(ctx, newPassword)
	if err != nil {
		return "", Session{}, err
	}
	cur, _, err := s.lookup(ctx, curToken, false)
	if err != nil {
		return "", Session{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", Session{}, err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx, `UPDATE users SET password_hash = ?, must_change_password = 0 WHERE id = ?`, hash, userID); err != nil {
		return "", Session{}, fmt.Errorf("updating password: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ? AND id_hash <> ?`, userID, cur.IDHash); err != nil {
		return "", Session{}, fmt.Errorf("dropping sessions: %w", err)
	}
	token, sess, err := s.rotateFrom(ctx, tx, cur)
	if err != nil {
		return "", Session{}, err
	}
	if err := tx.Commit(); err != nil {
		return "", Session{}, err
	}
	s.endWatchers(userID, "")
	return token, sess, nil
}

// PurgeExpired deletes sessions past their idle timeout or absolute lifetime.
func (s *Store) PurgeExpired(ctx context.Context) (int64, error) {
	now := s.now()
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE last_seen < ? OR created_at < ?`,
		now.Add(-s.IdleTimeout).Unix(), now.Add(-s.AbsoluteLifetime).Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// PasswordHash returns the stored hash for a user id (used to verify the current password).
func (s *Store) passwordHash(ctx context.Context, userID int64) (string, error) {
	var h string
	err := s.db.QueryRowContext(ctx, `SELECT password_hash FROM users WHERE id = ?`, userID).Scan(&h)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrUserMissing
	}
	return h, err
}

// UserByID returns the user with id, or ErrUserMissing.
func (s *Store) UserByID(ctx context.Context, id int64) (User, error) {
	u, err := scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrUserMissing
	}
	if err != nil {
		return User{}, fmt.Errorf("reading user: %w", err)
	}
	return u, nil
}

// UpdateUser changes a user's role and/or disabled flag (nil = unchanged) and returns the
// result. A change that takes effect drops the user's sessions, except re-enabling.
// Demoting or disabling the only active admin fails with ErrLastAdmin: the check is part of
// the UPDATE statement itself, so two concurrent requests cannot both succeed.
func (s *Store) UpdateUser(ctx context.Context, id int64, role *Role, disabled *bool) (User, error) {
	if role != nil && !role.Valid() {
		return User{}, ErrBadRole
	}
	for range 5 {
		cur, err := s.UserByID(ctx, id)
		if err != nil {
			return User{}, err
		}
		next := cur
		if role != nil {
			next.Role = *role
		}
		if disabled != nil {
			next.Disabled = *disabled
		}
		if next.Role == cur.Role && next.Disabled == cur.Disabled {
			return cur, nil
		}
		q := `UPDATE users SET role = ?, disabled = ? WHERE id = ? AND role = ? AND disabled = ?`
		args := []any{string(next.Role), b2i(next.Disabled), id, string(cur.Role), b2i(cur.Disabled)}
		guarded := cur.Role == RoleAdmin && !cur.Disabled && !(next.Role == RoleAdmin && !next.Disabled)
		if guarded {
			q += ` AND EXISTS (SELECT 1 FROM users o WHERE o.id <> ? AND o.role = 'admin' AND o.disabled = 0)`
			args = append(args, id)
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return User{}, fmt.Errorf("updating user: %w", err)
		}
		res, err := tx.ExecContext(ctx, q, args...)
		if err != nil {
			_ = tx.Rollback()
			return User{}, fmt.Errorf("updating user: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 1 {
			// The session delete shares the transaction: either both happen or neither.
			drop := next.Role != cur.Role || next.Disabled
			if drop {
				if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, id); err != nil {
					_ = tx.Rollback()
					return User{}, fmt.Errorf("dropping sessions: %w", err)
				}
			}
			if err := tx.Commit(); err != nil {
				return User{}, fmt.Errorf("updating user: %w", err)
			}
			if drop {
				s.endWatchers(id, "")
			}
			return next, nil
		}
		_ = tx.Rollback()
		again, err := s.UserByID(ctx, id)
		if err != nil {
			return User{}, err
		}
		if again.Role == cur.Role && again.Disabled == cur.Disabled {
			if guarded {
				return User{}, ErrLastAdmin
			}
			return User{}, errors.New("updating user: no row changed")
		}
	}
	return User{}, errors.New("updating user: changed concurrently, try again")
}

// DeleteOtherSessions removes every session of the user except the one identified by
// keepIDHash, and ends the event streams bound to the removed ones.
func (s *Store) DeleteOtherSessions(ctx context.Context, userID int64, keepIDHash string) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id_hash FROM sessions WHERE user_id = ? AND id_hash <> ?`, userID, keepIDHash)
	if err != nil {
		return fmt.Errorf("listing sessions: %w", err)
	}
	var gone []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			rows.Close()
			return err
		}
		gone = append(gone, h)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ? AND id_hash <> ?`, userID, keepIDHash); err != nil {
		return fmt.Errorf("dropping sessions: %w", err)
	}
	for _, h := range gone {
		s.endWatchers(userID, h)
	}
	return nil
}
