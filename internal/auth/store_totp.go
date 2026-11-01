package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/t0mer/cic/internal/secret"
)

// Errors returned by the TOTP methods.
var (
	// ErrNoSecretKey is returned when the store has no encryption key configured.
	ErrNoSecretKey = errors.New("two-factor authentication needs a secret key (CIC_SECRET_KEY or secret.key)")
	// ErrTOTPNotPending is returned by EnableTOTP when no setup is pending.
	ErrTOTPNotPending = errors.New("no two-factor setup in progress")
	// ErrTOTPEnabled is returned when setup is attempted while 2FA is already on.
	ErrTOTPEnabled = errors.New("two-factor authentication is already enabled")
	// ErrTOTPNotEnabled is returned when an operation needs 2FA to be on.
	ErrTOTPNotEnabled = errors.New("two-factor authentication is not enabled")
	// ErrBadCode is returned for a wrong, replayed or already used code.
	ErrBadCode = errors.New("invalid code")
	// ErrSecondFactorRequired means two-factor was enabled before a password-only session could be created.
	ErrSecondFactorRequired = errors.New("second factor required")
)

// Factor says which second factor VerifySecondFactor accepted.
type Factor string

// Accepted second factors.
const (
	FactorTOTP   Factor = "totp"
	FactorBackup Factor = "backup"
)

const (
	// BackupCodeCount is the number of backup codes issued at once.
	BackupCodeCount = 10
	backupCodeLen   = 10
	// uppercase part of the unambiguous password alphabet, so case-insensitive entry loses nothing
	backupAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
)

// SetSecretKey sets the 32-byte key that encrypts TOTP secrets. Without it every TOTP
// method returns ErrNoSecretKey.
func (s *Store) SetSecretKey(key []byte) { s.key = key }

func (s *Store) totpCtx(userID int64) string { return "totp:" + strconv.FormatInt(userID, 10) }

func (s *Store) encSecret(userID int64, plain string) (string, error) {
	if s.key == nil {
		return "", ErrNoSecretKey
	}
	return secret.EncryptString(s.key, plain, s.totpCtx(userID))
}

// loadSecret returns the decrypted secret, enabled flag and last step of a user.
func (s *Store) loadSecret(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, userID int64) (plain string, enabled bool, last int64, enc string, err error) {
	if s.key == nil {
		return "", false, 0, "", ErrNoSecretKey
	}
	var encN sql.NullString
	var en int
	err = q.QueryRowContext(ctx, `SELECT totp_secret, totp_enabled, totp_last_step FROM users WHERE id = ?`, userID).Scan(&encN, &en, &last)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, 0, "", ErrUserMissing
	}
	if err != nil {
		return "", false, 0, "", fmt.Errorf("reading totp state: %w", err)
	}
	if !encN.Valid || encN.String == "" {
		return "", en != 0, last, "", nil
	}
	plain, err = secret.DecryptString(s.key, encN.String, s.totpCtx(userID))
	if err != nil {
		return "", en != 0, last, "", fmt.Errorf("decrypting totp secret: %w", err)
	}
	return plain, en != 0, last, encN.String, nil
}

// SetPendingTOTP stores an encrypted, not yet active secret (replacing any earlier pending
// one). It fails with ErrTOTPEnabled when 2FA is already on.
func (s *Store) SetPendingTOTP(ctx context.Context, userID int64, secretB32 string) error {
	enc, err := s.encSecret(userID, secretB32)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE users SET totp_secret = ?, totp_last_step = 0 WHERE id = ? AND totp_enabled = 0`, enc, userID)
	if err != nil {
		return fmt.Errorf("storing totp secret: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var one int
		if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM users WHERE id = ?`, userID).Scan(&one); err != nil {
			return ErrUserMissing
		}
		return ErrTOTPEnabled
	}
	return nil
}

// EnableTOTP verifies code against the pending secret, turns 2FA on, records the used step
// and issues a fresh set of backup codes in one transaction. The plaintext codes (formatted
// XXXXX-XXXXX) are returned once and cannot be recovered.
func (s *Store) EnableTOTP(ctx context.Context, userID int64, code string, now time.Time) ([]string, error) {
	return s.enableTOTP(ctx, userID, code, now, nil)
}

// EnableTOTPRevoking is EnableTOTP that, in the same transaction, deletes every other
// session of the user (all but keepIDHash). If that fails nothing changes and an error is
// returned.
func (s *Store) EnableTOTPRevoking(ctx context.Context, userID int64, code string, now time.Time, keepIDHash string) ([]string, error) {
	return s.enableTOTP(ctx, userID, code, now, &keepIDHash)
}

func (s *Store) enableTOTP(ctx context.Context, userID int64, code string, now time.Time, keep *string) ([]string, error) {
	plain, enabled, _, enc, err := s.loadSecret(ctx, s.db, userID)
	if err != nil {
		return nil, err
	}
	if enabled {
		return nil, ErrTOTPEnabled
	}
	if plain == "" {
		return nil, ErrTOTPNotPending
	}
	step, ok := Verify(plain, code, now, 0)
	if !ok {
		return nil, ErrBadCode
	}
	if s.afterEnableRead != nil {
		s.afterEnableRead()
	}
	codes, hashes, err := newBackupCodes()
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	// First statement is the write (compare-and-set) so the transaction never upgrades a read
	// lock. It is bound to the exact ciphertext that was verified: a concurrent setup that
	// replaced the pending secret makes this match nothing.
	res, err := tx.ExecContext(ctx, `UPDATE users SET totp_enabled = 1, totp_last_step = ?, auth_gen = auth_gen + 1 WHERE id = ? AND totp_enabled = 0 AND totp_secret = ?`, step, userID, enc)
	if err != nil {
		return nil, fmt.Errorf("enabling totp: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var en int
		if err := tx.QueryRowContext(ctx, `SELECT totp_enabled FROM users WHERE id = ?`, userID).Scan(&en); err == nil && en == 0 {
			return nil, ErrTOTPNotPending
		}
		return nil, ErrTOTPEnabled
	}
	if err := replaceBackupCodes(ctx, tx, userID, hashes); err != nil {
		return nil, err
	}
	if keep != nil {
		if err := s.dropOtherSessions(ctx, tx, userID, *keep); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if keep != nil {
		s.endOtherWatchers(userID, *keep)
	}
	return codes, nil
}

// TOTPState reports whether 2FA is on and how many unused backup codes remain.
func (s *Store) TOTPState(ctx context.Context, userID int64) (enabled bool, remaining int, err error) {
	var en int
	err = s.db.QueryRowContext(ctx, `SELECT totp_enabled, (SELECT COUNT(*) FROM backup_codes b WHERE b.user_id = users.id AND b.used_at IS NULL)
		FROM users WHERE id = ?`, userID).Scan(&en, &remaining)
	if errors.Is(err, sql.ErrNoRows) {
		return false, 0, ErrUserMissing
	}
	if err != nil {
		return false, 0, fmt.Errorf("reading totp state: %w", err)
	}
	return en != 0, remaining, nil
}

// VerifySecondFactor accepts either a current TOTP code (each time step usable once) or an
// unused backup code (consumed on success). Anything else yields ErrBadCode. A server
// problem (no key, undecryptable secret, database) is a different error, so callers must
// not count it as a wrong guess.
func (s *Store) VerifySecondFactor(ctx context.Context, userID int64, input string, now time.Time) (Factor, error) {
	plain, enabled, last, _, err := s.loadSecret(ctx, s.db, userID)
	if err != nil {
		return "", err
	}
	if !enabled || plain == "" {
		return "", ErrTOTPNotEnabled
	}
	if compact := strings.ReplaceAll(strings.TrimSpace(input), " ", ""); len(compact) == totpDigits && isDigits(compact) {
		step, ok := Verify(plain, compact, now, last)
		if !ok {
			return "", ErrBadCode
		}
		res, err := s.db.ExecContext(ctx, `UPDATE users SET totp_last_step = ? WHERE id = ? AND totp_enabled = 1 AND totp_last_step < ?`, step, userID, step)
		if err != nil {
			return "", fmt.Errorf("recording totp step: %w", err)
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return "", ErrBadCode // a concurrent request consumed this step
		}
		return FactorTOTP, nil
	}
	norm := normalizeBackup(input)
	if len(norm) != backupCodeLen {
		return "", ErrBadCode
	}
	res, err := s.db.ExecContext(ctx, `UPDATE backup_codes SET used_at = ? WHERE user_id = ? AND code_hash = ? AND used_at IS NULL`,
		s.now().Unix(), userID, hashBackup(norm))
	if err != nil {
		return "", fmt.Errorf("consuming backup code: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return "", ErrBadCode
	}
	return FactorBackup, nil
}

// RegenerateBackupCodes replaces all backup codes (used or not) with a new set and returns
// the plaintext once. 2FA must be on.
func (s *Store) RegenerateBackupCodes(ctx context.Context, userID int64) ([]string, error) {
	codes, hashes, err := newBackupCodes()
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	res, err := tx.ExecContext(ctx, `UPDATE users SET auth_gen = auth_gen + 1 WHERE id = ? AND totp_enabled = 1`, userID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrTOTPNotEnabled
	}
	if err := replaceBackupCodes(ctx, tx, userID, hashes); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return codes, nil
}

// DisableTOTP clears the secret, the flags and all backup codes. It needs no key, so the
// CLI can use it to rescue a locked-out account. Calling it with 2FA off is not an error.
func (s *Store) DisableTOTP(ctx context.Context, userID int64) error {
	return s.disableTOTP(ctx, userID, nil)
}

// DisableTOTPRevoking is DisableTOTP that, in the same transaction, deletes the user's
// sessions except keepIDHash (empty deletes all of them, as the CLI rescue does). If that
// fails nothing changes and an error is returned.
func (s *Store) DisableTOTPRevoking(ctx context.Context, userID int64, keepIDHash string) error {
	return s.disableTOTP(ctx, userID, &keepIDHash)
}

func (s *Store) disableTOTP(ctx context.Context, userID int64, keep *string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	res, err := tx.ExecContext(ctx, `UPDATE users SET totp_secret = NULL, totp_enabled = 0, totp_last_step = 0, auth_gen = auth_gen + 1 WHERE id = ?`, userID)
	if err != nil {
		return fmt.Errorf("disabling totp: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrUserMissing
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM backup_codes WHERE user_id = ?`, userID); err != nil {
		return fmt.Errorf("deleting backup codes: %w", err)
	}
	if keep != nil {
		if err := s.dropOtherSessions(ctx, tx, userID, *keep); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if keep != nil {
		s.endOtherWatchers(userID, *keep)
	}
	return nil
}

func replaceBackupCodes(ctx context.Context, tx *sql.Tx, userID int64, hashes []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM backup_codes WHERE user_id = ?`, userID); err != nil {
		return fmt.Errorf("clearing backup codes: %w", err)
	}
	for _, h := range hashes {
		if _, err := tx.ExecContext(ctx, `INSERT INTO backup_codes(user_id, code_hash) VALUES (?, ?)`, userID, h); err != nil {
			return fmt.Errorf("storing backup code: %w", err)
		}
	}
	return nil
}

func isDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return s != ""
}

// normalizeBackup uppercases and drops dashes and whitespace.
func normalizeBackup(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '-' || r == ' ' || r == '\t' {
			return -1
		}
		return r
	}, strings.ToUpper(strings.TrimSpace(s)))
}

func hashBackup(norm string) string {
	sum := sha256.Sum256([]byte(norm))
	return hex.EncodeToString(sum[:])
}

// newBackupCodes returns BackupCodeCount display codes (XXXXX-XXXXX) and their hashes.
func newBackupCodes() (codes, hashes []string, err error) {
	n := big.NewInt(int64(len(backupAlphabet)))
	for len(codes) < BackupCodeCount {
		b := make([]byte, backupCodeLen)
		for i := range b {
			v, err := rand.Int(rand.Reader, n)
			if err != nil {
				return nil, nil, fmt.Errorf("generating backup code: %w", err)
			}
			b[i] = backupAlphabet[v.Int64()]
		}
		c := string(b)
		h := hashBackup(c)
		dup := false
		for _, e := range hashes {
			dup = dup || e == h
		}
		if dup {
			continue
		}
		codes = append(codes, c[:5]+"-"+c[5:])
		hashes = append(hashes, h)
	}
	return codes, hashes, nil
}

// CheckTOTPKey reports whether the loaded key decrypts the secret of one user who has
// two-factor login on. It returns nil when nobody has it on or no key is loaded. The error
// never contains secret material.
func (s *Store) CheckTOTPKey(ctx context.Context) error {
	if s.key == nil {
		return nil
	}
	var id int64
	var enc string
	err := s.db.QueryRowContext(ctx, `SELECT id, totp_secret FROM users WHERE totp_enabled = 1 AND totp_secret IS NOT NULL LIMIT 1`).Scan(&id, &enc)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading totp secret: %w", err)
	}
	if _, err := secret.DecryptString(s.key, enc, s.totpCtx(id)); err != nil {
		return errors.New("a stored two-factor secret does not decrypt with the loaded secret key")
	}
	return nil
}

// HasTOTPUsers reports whether any account has two-factor enabled.
func (s *Store) HasTOTPUsers(ctx context.Context) (bool, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE totp_enabled = 1`).Scan(&n); err != nil {
		return false, fmt.Errorf("counting two-factor users: %w", err)
	}
	return n > 0, nil
}
