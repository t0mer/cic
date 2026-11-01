package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/t0mer/cic/internal/store"
)

func totpStore(t *testing.T) (*Store, User, string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "t.db")
	db, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	st := NewStore(db, nil)
	k := make([]byte, 32)
	_, _ = rand.Read(k)
	st.SetSecretKey(k)
	u, err := st.CreateUser(context.Background(), "alice", goodPW, RoleViewer, false)
	if err != nil {
		t.Fatal(err)
	}
	return st, u, path, db
}

// enable runs the full setup and returns the secret and backup codes.
func enableTOTP(t *testing.T, st *Store, u User, now time.Time) (string, []string) {
	t.Helper()
	ctx := context.Background()
	sec, _ := GenerateSecret()
	if err := st.SetPendingTOTP(ctx, u.ID, sec); err != nil {
		t.Fatal(err)
	}
	c, _ := Code(sec, TOTPStep(now))
	codes, err := st.EnableTOTP(ctx, u.ID, c, now)
	if err != nil {
		t.Fatal(err)
	}
	return sec, codes
}

func TestTOTPLifecycle(t *testing.T) {
	st, u, path, db := totpStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)

	if en, rem, err := st.TOTPState(ctx, u.ID); err != nil || en || rem != 0 {
		t.Fatalf("initial state %v %d %v", en, rem, err)
	}
	if _, err := st.EnableTOTP(ctx, u.ID, "123456", now); !errors.Is(err, ErrTOTPNotPending) {
		t.Fatalf("enable without setup: %v", err)
	}
	sec, _ := GenerateSecret()
	if err := st.SetPendingTOTP(ctx, u.ID, sec); err != nil {
		t.Fatal(err)
	}
	if _, err := st.VerifySecondFactor(ctx, u.ID, "123456", now); !errors.Is(err, ErrTOTPNotEnabled) {
		t.Fatalf("pending must not verify: %v", err)
	}
	if _, err := st.EnableTOTP(ctx, u.ID, "000000", now); !errors.Is(err, ErrBadCode) {
		t.Fatalf("bad enable code: %v", err)
	}
	c, _ := Code(sec, TOTPStep(now))
	codes, err := st.EnableTOTP(ctx, u.ID, c, now)
	if err != nil || len(codes) != BackupCodeCount {
		t.Fatalf("enable: %v %d", err, len(codes))
	}
	for _, bc := range codes {
		if len(bc) != 11 || bc[5] != '-' {
			t.Fatalf("code format %q", bc)
		}
	}
	if en, rem, _ := st.TOTPState(ctx, u.ID); !en || rem != BackupCodeCount {
		t.Fatalf("state after enable %v %d", en, rem)
	}
	if got, _ := st.UserByID(ctx, u.ID); !got.TOTPEnabled {
		t.Fatal("User.TOTPEnabled not set")
	}
	if err := st.SetPendingTOTP(ctx, u.ID, sec); !errors.Is(err, ErrTOTPEnabled) {
		t.Fatalf("setup while enabled: %v", err)
	}
	// the enabling step is burned; the next step works once
	if _, err := st.VerifySecondFactor(ctx, u.ID, c, now); !errors.Is(err, ErrBadCode) {
		t.Fatalf("replay of enable step: %v", err)
	}
	next := now.Add(30 * time.Second)
	c2, _ := Code(sec, TOTPStep(next))
	if f, err := st.VerifySecondFactor(ctx, u.ID, c2[:3]+" "+c2[3:], next); err != nil || f != FactorTOTP {
		t.Fatalf("totp: %v %v", f, err)
	}
	if _, err := st.VerifySecondFactor(ctx, u.ID, c2, next); !errors.Is(err, ErrBadCode) {
		t.Fatalf("replay: %v", err)
	}
	// backup code: lowercase and without dash also accepted, single use
	bc := strings.ToLower(strings.ReplaceAll(codes[0], "-", " "))
	if f, err := st.VerifySecondFactor(ctx, u.ID, bc, next); err != nil || f != FactorBackup {
		t.Fatalf("backup: %v %v", f, err)
	}
	if _, err := st.VerifySecondFactor(ctx, u.ID, codes[0], next); !errors.Is(err, ErrBadCode) {
		t.Fatalf("backup reuse: %v", err)
	}
	if _, err := st.VerifySecondFactor(ctx, u.ID, "AAAAA-AAAAA", next); !errors.Is(err, ErrBadCode) {
		t.Fatalf("unknown backup: %v", err)
	}
	if _, rem, _ := st.TOTPState(ctx, u.ID); rem != BackupCodeCount-1 {
		t.Fatalf("remaining %d", rem)
	}

	// regenerate invalidates the old set
	fresh, err := st.RegenerateBackupCodes(ctx, u.ID)
	if err != nil || len(fresh) != BackupCodeCount {
		t.Fatal(err)
	}
	if _, err := st.VerifySecondFactor(ctx, u.ID, codes[1], next); !errors.Is(err, ErrBadCode) {
		t.Fatalf("old code still valid: %v", err)
	}
	if f, err := st.VerifySecondFactor(ctx, u.ID, fresh[0], next); err != nil || f != FactorBackup {
		t.Fatalf("new code: %v", err)
	}

	// plaintext secret and codes never in the database file (checkpoint WAL first)
	_, _ = db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	for _, f := range []string{path, path + "-wal"} {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		if bytes.Contains(b, []byte(sec)) {
			t.Fatalf("plaintext secret in %s", f)
		}
		for _, c := range append(append([]string{}, codes...), fresh...) {
			if bytes.Contains(b, []byte(c)) || bytes.Contains(b, []byte(strings.ReplaceAll(c, "-", ""))) {
				t.Fatalf("plaintext backup code in %s", f)
			}
		}
	}

	if err := st.DisableTOTP(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if en, rem, _ := st.TOTPState(ctx, u.ID); en || rem != 0 {
		t.Fatalf("after disable %v %d", en, rem)
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM users WHERE id=? AND totp_secret IS NOT NULL`, u.ID).Scan(&n)
	if n != 0 {
		t.Fatal("secret left behind")
	}
	if _, err := st.RegenerateBackupCodes(ctx, u.ID); !errors.Is(err, ErrTOTPNotEnabled) {
		t.Fatalf("regenerate when off: %v", err)
	}
}

func TestTOTPKeyHandling(t *testing.T) {
	st, u, _, _ := totpStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	sec, _ := enableTOTP(t, st, u, now)

	wrong := make([]byte, 32)
	_, _ = rand.Read(wrong)
	st2 := NewStore(st.db, nil)
	st2.SetSecretKey(wrong)
	c, _ := Code(sec, TOTPStep(now)+1)
	_, err := st2.VerifySecondFactor(ctx, u.ID, c, now.Add(30*time.Second))
	if err == nil || errors.Is(err, ErrBadCode) || strings.Contains(err.Error(), sec) {
		t.Fatalf("wrong key must be a server error, not a bad code: %v", err)
	}
	st3 := NewStore(st.db, nil) // no key
	for name, e := range map[string]error{
		"pending": st3.SetPendingTOTP(ctx, u.ID, sec),
		"verify":  func() error { _, err := st3.VerifySecondFactor(ctx, u.ID, c, now); return err }(),
		"enable":  func() error { _, err := st3.EnableTOTP(ctx, u.ID, c, now); return err }(),
	} {
		if !errors.Is(e, ErrNoSecretKey) {
			t.Fatalf("%s without key: %v", name, e)
		}
	}
	// a ciphertext copied to another user's row fails (bound to user id)
	other, _ := st.CreateUser(ctx, "bobby", goodPW, RoleViewer, false)
	_, _ = st.db.Exec(`UPDATE users SET totp_secret=(SELECT totp_secret FROM users WHERE id=?), totp_enabled=1 WHERE id=?`, u.ID, other.ID)
	if _, err := st.VerifySecondFactor(ctx, other.ID, c, now); err == nil || errors.Is(err, ErrBadCode) {
		t.Fatalf("copied secret decrypted: %v", err)
	}
}

func TestTOTPConcurrentBackupCode(t *testing.T) {
	st, u, _, _ := totpStore(t)
	_, codes := enableTOTP(t, st, u, time.Unix(1700000000, 0))
	var wins int32
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := st.VerifySecondFactor(context.Background(), u.ID, codes[3], time.Unix(1700000100, 0)); err == nil {
				atomic.AddInt32(&wins, 1)
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("%d winners", wins)
	}
}

func TestTOTPConcurrentSameStep(t *testing.T) {
	st, u, _, _ := totpStore(t)
	now := time.Unix(1700000000, 0)
	sec, _ := enableTOTP(t, st, u, now)
	later := now.Add(60 * time.Second)
	c, _ := Code(sec, TOTPStep(later))
	var wins int32
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := st.VerifySecondFactor(context.Background(), u.ID, c, later); err == nil {
				atomic.AddInt32(&wins, 1)
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("%d winners", wins)
	}
}

func TestMeExposesTOTPEnabled(t *testing.T) {
	if !viewOf(User{TOTPEnabled: true}).TOTPEnabled || viewOf(User{}).TOTPEnabled {
		t.Fatal("viewOf")
	}
}

func TestEnableBoundToVerifiedSecret(t *testing.T) {
	st, u, _, _ := totpStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	first, _ := GenerateSecret()
	if err := st.SetPendingTOTP(ctx, u.ID, first); err != nil {
		t.Fatal(err)
	}
	// a concurrent setup replaces the pending secret after Enable has verified the code
	st.afterEnableRead = func() {
		second, _ := GenerateSecret()
		if err := st.SetPendingTOTP(ctx, u.ID, second); err != nil {
			t.Error(err)
		}
	}
	c, _ := Code(first, TOTPStep(now))
	if _, err := st.EnableTOTP(ctx, u.ID, c, now); !errors.Is(err, ErrTOTPNotPending) {
		t.Fatalf("want ErrTOTPNotPending, got %v", err)
	}
	if en, _, _ := st.TOTPState(ctx, u.ID); en {
		t.Fatal("2FA enabled with a secret nobody verified")
	}
}

func TestCheckTOTPKey(t *testing.T) {
	st, u, _, _ := totpStore(t)
	ctx := context.Background()
	if err := st.CheckTOTPKey(ctx); err != nil {
		t.Fatal(err)
	}
	enableTOTP(t, st, u, time.Unix(1700000000, 0))
	if err := st.CheckTOTPKey(ctx); err != nil {
		t.Fatal(err)
	}
	k := make([]byte, 32)
	_, _ = rand.Read(k)
	st.SetSecretKey(k)
	if err := st.CheckTOTPKey(ctx); err == nil {
		t.Fatal("wrong key not detected")
	}
}
