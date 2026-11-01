package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

const failSessionDelete = `CREATE TRIGGER fail_session_delete BEFORE DELETE ON sessions BEGIN SELECT RAISE(ABORT, 'boom'); END`

func closed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func TestEnableDisableRevocationIsAtomic(t *testing.T) {
	st, u, _, db := totpStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	_, keepS, err := st.CreateSession(ctx, u.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	otherTok, other, err := st.CreateSession(ctx, u.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	_ = otherTok
	keepCh, stopKeep, _ := st.Watch(u.ID, keepS.IDHash)
	defer stopKeep()
	otherCh, stopOther, _ := st.Watch(u.ID, other.IDHash)
	defer stopOther()

	sec, _ := GenerateSecret()
	if err := st.SetPendingTOTP(ctx, u.ID, sec); err != nil {
		t.Fatal(err)
	}
	code, _ := Code(sec, TOTPStep(now))
	if _, err := db.Exec(failSessionDelete); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnableTOTPRevoking(ctx, u.ID, code, now, keepS.IDHash); err == nil {
		t.Fatal("enable succeeded although revocation failed")
	}
	if en, _, _ := st.TOTPState(ctx, u.ID); en {
		t.Fatal("2FA turned on without revoking sessions")
	}
	if closed(otherCh) || closed(keepCh) {
		t.Fatal("watchers ended although nothing changed")
	}
	if _, err := db.Exec(`DROP TRIGGER fail_session_delete`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnableTOTPRevoking(ctx, u.ID, code, now, keepS.IDHash); err != nil {
		t.Fatal(err)
	}
	if !closed(otherCh) || closed(keepCh) {
		t.Fatal("enable must end the other session's stream only")
	}
	if _, _, err := st.Check(ctx, otherTok); !errors.Is(err, ErrNoSession) {
		t.Fatalf("other session survived enable: %v", err)
	}

	// disable
	otherTok2, other2, _ := st.CreateSession(ctx, u.ID, "")
	otherCh2, stop2, _ := st.Watch(u.ID, other2.IDHash)
	defer stop2()
	if _, err := db.Exec(failSessionDelete); err != nil {
		t.Fatal(err)
	}
	if err := st.DisableTOTPRevoking(ctx, u.ID, keepS.IDHash); err == nil {
		t.Fatal("disable succeeded although revocation failed")
	}
	if en, _, _ := st.TOTPState(ctx, u.ID); !en {
		t.Fatal("2FA turned off without revoking sessions")
	}
	if closed(otherCh2) {
		t.Fatal("watcher ended although nothing changed")
	}
	if _, err := db.Exec(`DROP TRIGGER fail_session_delete`); err != nil {
		t.Fatal(err)
	}
	if err := st.DisableTOTPRevoking(ctx, u.ID, keepS.IDHash); err != nil {
		t.Fatal(err)
	}
	if !closed(otherCh2) || closed(keepCh) {
		t.Fatal("disable must end the other session's stream only")
	}
	if _, _, err := st.Check(ctx, otherTok2); !errors.Is(err, ErrNoSession) {
		t.Fatalf("other session survived disable: %v", err)
	}
}

func TestTwoFactorEndpointsFailWhenRevocationFails(t *testing.T) {
	f := setup2FA(t, RoleViewer) // enabled
	db := f.s.Store().db
	c := newClient(t, f.s)
	c.step2(f.step1(t, c), f.code(t))
	other := newClient(t, f.s)
	f.clk.Add(31 * time.Second)
	other.step2(f.step1(t, other), f.code(t))
	if _, err := db.Exec(failSessionDelete); err != nil {
		t.Fatal(err)
	}
	f.clk.Add(31 * time.Second)
	rec := c.post("/api/auth/2fa/disable", map[string]string{"current_password": goodPW, "code": f.code(t)})
	if rec.Code != 500 {
		t.Fatalf("disable with failing revocation: %d %s", rec.Code, rec.Body)
	}
	if rec := other.do("GET", "/api/read", nil, nil); rec.Code != 200 {
		t.Fatalf("other session lost: %d", rec.Code)
	}
	if en, _, _ := f.s.Store().TOTPState(context.Background(), 1); !en {
		t.Fatal("2FA disabled although the request failed")
	}
	if _, err := db.Exec(`DROP TRIGGER fail_session_delete`); err != nil {
		t.Fatal(err)
	}
	f.s.Limiter().Reset("2fa:alice")
	f.clk.Add(31 * time.Second) // the first attempt spent its time step
	rec = c.post("/api/auth/2fa/disable", map[string]string{"current_password": goodPW, "code": f.code(t)})
	if rec.Code != 204 {
		t.Fatalf("disable retry: %d %s", rec.Code, rec.Body)
	}
	if rec := other.do("GET", "/api/read", nil, nil); rec.Code != 401 {
		t.Fatalf("other session survived: %d", rec.Code)
	}

	// enable path over HTTP
	s, clk := newSvc(t, false)
	withKey(t, s)
	mustUser(t, s, "alice", RoleViewer, false)
	a, b := newClient(t, s), newClient(t, s)
	a.login("alice", goodPW)
	b.login("alice", goodPW)
	rec = a.post("/api/auth/2fa/setup", map[string]string{"current_password": goodPW})
	if rec.Code != 200 {
		t.Fatal(rec.Body)
	}
	var sec string
	{
		i := strings.Index(rec.Body.String(), `"secret":"`)
		sec = strings.SplitN(rec.Body.String()[i+10:], `"`, 2)[0]
	}
	code, _ := Code(sec, TOTPStep(clk.Now()))
	if _, err := s.Store().db.Exec(failSessionDelete); err != nil {
		t.Fatal(err)
	}
	if rec := a.post("/api/auth/2fa/enable", map[string]string{"code": code}); rec.Code != 500 {
		t.Fatalf("enable with failing revocation: %d %s", rec.Code, rec.Body)
	}
	if en, _, _ := s.Store().TOTPState(context.Background(), 1); en {
		t.Fatal("2FA on although the request failed")
	}
	if _, err := s.Store().db.Exec(`DROP TRIGGER fail_session_delete`); err != nil {
		t.Fatal(err)
	}
	if rec := a.post("/api/auth/2fa/enable", map[string]string{"code": code}); rec.Code != 200 {
		t.Fatalf("enable retry: %d %s", rec.Code, rec.Body)
	}
	if rec := b.do("GET", "/api/read", nil, nil); rec.Code != 401 {
		t.Fatalf("other session survived enable: %d", rec.Code)
	}
}

func TestChallengeBoundToAuthGeneration(t *testing.T) {
	ctx := context.Background()
	expired := func(t *testing.T, f *tfa, tok string) {
		t.Helper()
		rec := newClient(t, f.s).step2(tok, f.code(t))
		if rec.Code != 401 || !strings.Contains(rec.Body.String(), "login expired") {
			t.Fatalf("stale challenge accepted: %d %s", rec.Code, rec.Body)
		}
	}
	t.Run("admin or CLI password reset", func(t *testing.T) {
		f := setup2FA(t, RoleViewer)
		tok := f.step1(t, newClient(t, f.s))
		if err := f.s.Store().SetPassword(ctx, 1, "alice", "a different long password", false); err != nil {
			t.Fatal(err)
		}
		expired(t, f, tok)
	})
	t.Run("password change by the user", func(t *testing.T) {
		f := setup2FA(t, RoleViewer)
		c := newClient(t, f.s)
		c.step2(f.step1(t, c), f.code(t))
		f.clk.Add(31 * time.Second)
		tok := f.step1(t, newClient(t, f.s))
		rec := c.post("/api/auth/change-password", map[string]string{"current_password": goodPW, "new_password": "a brand new password"})
		if rec.Code != 200 {
			t.Fatalf("change: %d %s", rec.Code, rec.Body)
		}
		expired(t, f, tok)
	})
	t.Run("disable and re-enable", func(t *testing.T) {
		f := setup2FA(t, RoleViewer)
		tok := f.step1(t, newClient(t, f.s))
		st := f.s.Store()
		if err := st.DisableTOTP(ctx, 1); err != nil {
			t.Fatal(err)
		}
		u, _, _ := st.UserByName(ctx, "alice")
		f.secret, _ = enableTOTP(t, st, u, f.clk.Now())
		f.clk.Add(31 * time.Second)
		expired(t, f, tok)
	})
	t.Run("backup code regeneration", func(t *testing.T) {
		f := setup2FA(t, RoleViewer)
		tok := f.step1(t, newClient(t, f.s))
		if _, err := f.s.Store().RegenerateBackupCodes(ctx, 1); err != nil {
			t.Fatal(err)
		}
		expired(t, f, tok)
	})
	t.Run("unchanged account still logs in", func(t *testing.T) {
		f := setup2FA(t, RoleViewer)
		c := newClient(t, f.s)
		if rec := c.step2(f.step1(t, c), f.code(t)); rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
	})
}

func TestAuthGenBumps(t *testing.T) {
	st, u, _, _ := totpStore(t)
	ctx := context.Background()
	gen := func() int64 { x, _ := st.UserByID(ctx, u.ID); return x.AuthGen }
	last := gen()
	step := func(name string, f func()) {
		t.Helper()
		f()
		if g := gen(); g <= last {
			t.Fatalf("%s did not bump auth_gen (%d -> %d)", name, last, g)
		} else {
			last = g
		}
	}
	step("set password", func() { _ = st.SetPassword(ctx, u.ID, "alice", "yet another long password", false) })
	now := time.Unix(1700000000, 0)
	step("enable", func() { enableTOTP(t, st, u, now) })
	step("regenerate", func() { _, _ = st.RegenerateBackupCodes(ctx, u.ID) })
	step("disable", func() { _ = st.DisableTOTP(ctx, u.ID) })
}

func TestSessionCreatedDuringRevocationIsEnded(t *testing.T) {
	st, u, _, _ := totpStore(t)
	ctx := context.Background()
	_, keep, _ := st.CreateSession(ctx, u.ID, "")
	var lateCh <-chan struct{}
	st.beforeRevoke = func() { // a login lands between "list" and "delete" of the old code
		st.beforeRevoke = nil
		_, late, err := st.CreateSession(ctx, u.ID, "")
		if err != nil {
			t.Error(err)
			return
		}
		lateCh, _, _ = st.Watch(u.ID, late.IDHash)
	}
	keepCh, _, _ := st.Watch(u.ID, keep.IDHash)
	if err := st.DeleteOtherSessions(ctx, u.ID, keep.IDHash); err != nil {
		t.Fatal(err)
	}
	if lateCh == nil || !closed(lateCh) {
		t.Fatal("stream of a session created during revocation was not ended")
	}
	if closed(keepCh) {
		t.Fatal("kept session's stream ended")
	}
	var n int
	_ = st.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE user_id = ?`, u.ID).Scan(&n)
	if n != 1 {
		t.Fatalf("%d sessions left, want 1", n)
	}
}
