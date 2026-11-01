package auth

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func withKey(t *testing.T, s *Service) {
	t.Helper()
	k := make([]byte, 32)
	_, _ = rand.Read(k)
	s.Store().SetSecretKey(k)
}

type auditRec struct {
	mu  sync.Mutex
	got []string
}

func (a *auditRec) fn(_ context.Context, actor, action, detail string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.got = append(a.got, actor+"|"+action+"|"+detail)
	return nil
}
func (a *auditRec) has(s string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, g := range a.got {
		if g == s {
			return true
		}
	}
	return false
}

// tfa is a user with two-factor login on.
type tfa struct {
	s      *Service
	clk    *fakeClock
	secret string
	codes  []string
	aud    *auditRec
}

func setup2FA(t *testing.T, role Role) *tfa {
	t.Helper()
	s, clk := newSvc(t, false)
	withKey(t, s)
	a := &auditRec{}
	s.SetAuditor(a.fn)
	mustUser(t, s, "alice", role, false)
	c := newClient(t, s)
	c.login("alice", goodPW)
	rec := c.post("/api/auth/2fa/setup", map[string]string{"current_password": goodPW})
	if rec.Code != 200 {
		t.Fatalf("setup %d %s", rec.Code, rec.Body)
	}
	var v struct{ Secret, OTPAuthURI string }
	_ = json.Unmarshal(rec.Body.Bytes(), &struct {
		Secret *string `json:"secret"`
		URI    *string `json:"otpauth_uri"`
	}{&v.Secret, &v.OTPAuthURI})
	if v.Secret == "" || !strings.HasPrefix(v.OTPAuthURI, "otpauth://totp/CIC:alice?") || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("setup body %s", rec.Body)
	}
	code, _ := Code(v.Secret, TOTPStep(clk.Now()))
	rec = c.post("/api/auth/2fa/enable", map[string]string{"code": code})
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("enable %d %s", rec.Code, rec.Body)
	}
	var e struct {
		Codes []string `json:"backup_codes"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &e)
	if len(e.Codes) != BackupCodeCount {
		t.Fatalf("codes: %s", rec.Body)
	}
	clk.Add(31 * time.Second) // the enabling step is spent
	return &tfa{s: s, clk: clk, secret: v.Secret, codes: e.Codes, aud: a}
}

func (f *tfa) code(t *testing.T) string {
	t.Helper()
	c, _ := Code(f.secret, TOTPStep(f.clk.Now()))
	return c
}

// step1 does the password step and returns the mfa token.
func (f *tfa) step1(t *testing.T, c *client) string {
	t.Helper()
	rec := c.login("alice", goodPW)
	if rec.Code != 200 {
		t.Fatalf("login %d %s", rec.Code, rec.Body)
	}
	if len(rec.Result().Cookies()) != 0 || c.cookie != nil {
		t.Fatal("a cookie was issued before the second factor")
	}
	var v struct {
		MFA   bool   `json:"mfa_required"`
		Token string `json:"mfa_token"`
		CSRF  string `json:"csrf_token"`
		User  any    `json:"user"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	if !v.MFA || len(v.Token) < 43 || v.CSRF != "" || v.User != nil {
		t.Fatalf("step1 body: %s", rec.Body)
	}
	return v.Token
}

func (c *client) step2(token, code string) *httptest.ResponseRecorder {
	rec := c.do("POST", "/api/auth/login/2fa", map[string]string{"mfa_token": token, "code": code}, nil)
	if rec.Code == 200 {
		for _, ck := range rec.Result().Cookies() {
			if isSessionCookie(ck.Name) && ck.MaxAge >= 0 {
				c.cookie = ck
			}
		}
		var v struct {
			CSRF string `json:"csrf_token"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &v)
		c.csrf = v.CSRF
	}
	return rec
}

func TestLogin2FAOffUnchanged(t *testing.T) {
	s, _ := newSvc(t, false)
	mustUser(t, s, "alice", RoleViewer, false)
	c := newClient(t, s)
	rec := c.login("alice", goodPW)
	if rec.Code != 200 || c.cookie == nil || strings.Contains(rec.Body.String(), "mfa") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestTwoStepLoginTOTP(t *testing.T) {
	f := setup2FA(t, RoleViewer)
	c := newClient(t, f.s)
	tok := f.step1(t, c)
	if rec := c.step2(tok, "000000"); rec.Code != 401 || !strings.Contains(rec.Body.String(), "invalid code") || c.cookie != nil {
		t.Fatalf("wrong code: %d %s", rec.Code, rec.Body)
	}
	code := f.code(t)
	rec := c.step2(tok, code)
	if rec.Code != 200 || c.cookie == nil || !strings.Contains(rec.Body.String(), `"totp_enabled":true`) {
		t.Fatalf("2fa: %d %s", rec.Code, rec.Body)
	}
	if rec := c.do("GET", "/api/read", nil, nil); rec.Code != 200 {
		t.Fatalf("session unusable: %d", rec.Code)
	}
	if rec := c.do("GET", "/api/auth/me", nil, nil); !strings.Contains(rec.Body.String(), `"totp_enabled":true`) {
		t.Fatalf("me: %s", rec.Body)
	}
	// single use of the token
	if rec := newClient(t, f.s).step2(tok, f.code(t)); rec.Code != 401 || !strings.Contains(rec.Body.String(), "login expired") {
		t.Fatalf("token reuse: %d %s", rec.Code, rec.Body)
	}
	// replay of the same step in a fresh login fails; the next step works
	c2 := newClient(t, f.s)
	tok2 := f.step1(t, c2)
	if rec := c2.step2(tok2, code); rec.Code != 401 {
		t.Fatalf("replay accepted: %d", rec.Code)
	}
	f.clk.Add(31 * time.Second)
	if rec := c2.step2(tok2, f.code(t)); rec.Code != 200 {
		t.Fatalf("next step: %d %s", rec.Code, rec.Body)
	}
	if f.aud.has("alice|2fa_login|factor=backup_code") {
		t.Fatal("totp login must not be audited as backup")
	}
}

func TestTwoStepLoginBackupCode(t *testing.T) {
	f := setup2FA(t, RoleAdmin)
	c := newClient(t, f.s)
	tok := f.step1(t, c)
	if rec := c.step2(tok, f.codes[0]); rec.Code != 200 {
		t.Fatalf("backup: %d %s", rec.Code, rec.Body)
	}
	if !f.aud.has("alice|2fa_login|factor=backup_code") {
		t.Fatalf("no audit: %v", f.aud.got)
	}
	c2 := newClient(t, f.s)
	tok2 := f.step1(t, c2)
	if rec := c2.step2(tok2, f.codes[0]); rec.Code != 401 {
		t.Fatalf("backup reuse: %d", rec.Code)
	}
	if rec := c2.step2(tok2, strings.ToLower(f.codes[1])); rec.Code != 200 {
		t.Fatalf("lowercase backup: %d %s", rec.Code, rec.Body)
	}
}

func TestSecondFactorSharesLockoutWithPassword(t *testing.T) {
	f := setup2FA(t, RoleViewer)
	c := newClient(t, f.s)
	if rec := c.login("alice", "wrong wrong wrong"); rec.Code != 401 {
		t.Fatal(rec.Code)
	}
	if rec := c.login("alice", "wrong wrong wrong"); rec.Code != 401 {
		t.Fatal(rec.Code)
	}
	tok := f.step1(t, c) // correct password does not clear the two failures
	for i := 0; i < 2; i++ {
		if rec := c.step2(tok, "111111"); rec.Code != 401 {
			t.Fatalf("wrong code %d: %d", i, rec.Code)
		}
	}
	// 4 failures so far; the fifth locks the shared user key
	if rec := c.step2(tok, "111111"); rec.Code != 401 {
		t.Fatalf("fifth: %d", rec.Code)
	}
	if rec := c.step2(tok, f.code(t)); rec.Code != 429 || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("locked step2: %d", rec.Code)
	}
	if rec := newClient(t, f.s).login("alice", goodPW); rec.Code != 429 {
		t.Fatalf("locked login: %d", rec.Code)
	}
}

func TestMFATokenLimits(t *testing.T) {
	f := setup2FA(t, RoleViewer)
	reset := func() { f.s.Limiter().Reset("user:alice", "ip:203.0.113.9") }
	// attempt cap: 5 wrong guesses kill the token even if the limiter is fine
	c := newClient(t, f.s)
	tok := f.step1(t, c)
	for i := 0; i < 5; i++ {
		reset()
		if rec := c.step2(tok, "222222"); rec.Code != 401 {
			t.Fatal(rec.Code)
		}
	}
	reset()
	rec := c.step2(tok, f.code(t))
	if rec.Code != 401 || !strings.Contains(rec.Body.String(), "login expired") || c.cookie != nil {
		t.Fatalf("after cap: %d %s", rec.Code, rec.Body)
	}
	// expiry
	reset()
	tok = f.step1(t, c)
	f.clk.Add(mfaTTL + time.Second)
	if rec := c.step2(tok, f.code(t)); rec.Code != 401 || !strings.Contains(rec.Body.String(), "login expired") {
		t.Fatalf("expired: %d %s", rec.Code, rec.Body)
	}
	// garbage token
	if rec := c.step2("nope", "123456"); rec.Code != 401 || !strings.Contains(rec.Body.String(), "login expired") {
		t.Fatalf("unknown: %d", rec.Code)
	}
	// disabled user mid-flow
	reset()
	tok = f.step1(t, c)
	u, _, _ := f.s.Store().UserByName(context.Background(), "alice")
	_ = f.s.Store().SetDisabled(context.Background(), u.ID, true)
	if rec := c.step2(tok, f.code(t)); rec.Code != 401 {
		t.Fatalf("disabled: %d", rec.Code)
	}
}

func TestMFATokenBoundToUser(t *testing.T) {
	f := setup2FA(t, RoleViewer)
	mustUser(t, f.s, "bob", RoleViewer, false)
	// bob has no 2FA; a valid alice token can never be redeemed with another user's code
	c := newClient(t, f.s)
	tok := f.step1(t, c)
	bob := newClient(t, f.s)
	if rec := bob.login("bob", goodPW); rec.Code != 200 || bob.cookie == nil {
		t.Fatal("bob login")
	}
	if rec := c.step2(tok, "654321"); rec.Code != 401 {
		t.Fatal(rec.Code)
	}
	// the session issued for a valid code belongs to alice
	rec := c.step2(tok, f.code(t))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"username":"alice"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestMFAChallengeCaps(t *testing.T) {
	m := newMFAStore(time.Now)
	victim, _ := m.issue(1, 0)
	// one account flooding itself keeps only its newest mfaMaxPerUser challenges
	first, _ := m.issue(2, 0)
	for i := 0; i < 3*mfaMaxPerUser; i++ {
		if _, err := m.issue(2, 0); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, ok := m.peek(first); ok {
		t.Fatal("user's oldest challenge should have been dropped")
	}
	if _, _, ok := m.peek(victim); !ok {
		t.Fatal("another user's challenge was evicted")
	}
	// at the global cap new challenges are refused, nobody is evicted
	m = newMFAStore(time.Now)
	victim, _ = m.issue(1, 0)
	now := time.Now()
	for u := int64(2); len(m.m) < mfaMaxPending; u++ {
		m.m[mfaKey(string(rune(u))+"x")] = &challenge{userID: u, created: now, expires: now.Add(mfaTTL)}
	}
	if _, err := m.issue(999_999, 0); err != errMFAFull {
		t.Fatalf("want errMFAFull, got %v", err)
	}
	if _, _, ok := m.peek(victim); !ok || len(m.m) != mfaMaxPending {
		t.Fatal("full store evicted a challenge")
	}
}

func TestSetupDoesNotLaunderFailures(t *testing.T) {
	f := setup2FA(t, RoleViewer)
	c := newClient(t, f.s)
	tok := f.step1(t, c)
	if rec := c.step2(tok, f.code(t)); rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	f.clk.Add(31 * time.Second)
	disable := func(code string) int {
		return c.post("/api/auth/2fa/disable", map[string]string{"current_password": goodPW, "code": code}).Code
	}
	for i := 0; i < 4; i++ {
		if got := disable("111111"); got != 401 {
			t.Fatalf("wrong disable %d: %d", i, got)
		}
	}
	// setup while enabled (409) must not reset the counter
	if got := c.post("/api/auth/2fa/setup", map[string]string{"current_password": goodPW}).Code; got != 409 {
		t.Fatalf("setup: %d", got)
	}
	// the fifth wrong code locks the key
	if got := disable("111111"); got != 401 {
		t.Fatalf("fifth: %d", got)
	}
	if got := disable(f.code(t)); got != 429 {
		t.Fatalf("not locked after setup 409: %d", got)
	}
}

func TestSetupOKDoesNotLaunderFailures(t *testing.T) {
	s, _ := newSvc(t, false)
	withKey(t, s)
	mustUser(t, s, "bob", RoleViewer, false)
	c := newClient(t, s)
	c.login("bob", goodPW)
	// 3 charged failures on the shared key via a wrong password on setup
	for i := 0; i < 3; i++ {
		if got := c.post("/api/auth/2fa/setup", map[string]string{"current_password": "wrong wrong wrong"}).Code; got != 401 {
			t.Fatalf("%d", got)
		}
	}
	if got := c.post("/api/auth/2fa/setup", map[string]string{"current_password": goodPW}).Code; got != 200 {
		t.Fatalf("setup ok: %d", got)
	}
	if got := c.post("/api/auth/2fa/enable", map[string]string{"code": "111111"}).Code; got != 401 {
		t.Fatalf("bad enable: %d", got)
	}
	if got := c.post("/api/auth/2fa/setup", map[string]string{"current_password": goodPW}).Code; got != 200 {
		t.Fatalf("setup again: %d", got)
	}
	if got := c.post("/api/auth/2fa/setup", map[string]string{"current_password": "wrong wrong wrong"}).Code; got != 401 {
		t.Fatalf("fifth: %d", got)
	}
	if got := c.post("/api/auth/2fa/setup", map[string]string{"current_password": goodPW}).Code; got != 429 {
		t.Fatalf("a successful setup cleared the failures: %d", got)
	}
}

func TestSelfServiceNeedsProofAndRevokesSessions(t *testing.T) {
	for _, role := range []Role{RoleViewer, RoleAdmin} {
		t.Run(string(role), func(t *testing.T) {
			f := setup2FA(t, role)
			c := newClient(t, f.s)
			c.step2(f.step1(t, c), f.code(t))
			other := newClient(t, f.s)
			f.clk.Add(31 * time.Second)
			other.step2(f.step1(t, other), f.code(t))
			if rec := other.do("GET", "/api/read", nil, nil); rec.Code != 200 {
				t.Fatal("other session")
			}
			rec := c.do("GET", "/api/auth/2fa", nil, nil)
			if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"enabled":true`) || !strings.Contains(rec.Body.String(), `"backup_codes_remaining":10`) {
				t.Fatalf("status: %s", rec.Body)
			}
			// disable needs password AND code
			if rec := c.post("/api/auth/2fa/disable", map[string]string{"current_password": "wrong wrong wrong", "code": f.code(t)}); rec.Code != 401 {
				t.Fatalf("bad pw: %d", rec.Code)
			}
			if rec := c.post("/api/auth/2fa/disable", map[string]string{"current_password": goodPW, "code": "000000"}); rec.Code != 401 || !strings.Contains(rec.Body.String(), "invalid code") {
				t.Fatalf("bad code: %d %s", rec.Code, rec.Body)
			}
			if rec := c.post("/api/auth/2fa/disable", map[string]string{"current_password": goodPW}); rec.Code != 401 {
				t.Fatalf("no code: %d", rec.Code)
			}
			// regenerate
			f.s.Limiter().Reset("2fa:alice")
			f.clk.Add(31 * time.Second)
			if rec := c.post("/api/auth/2fa/backup-codes", map[string]string{"code": f.code(t)}); rec.Code != 401 {
				t.Fatalf("regen w/o pw: %d", rec.Code)
			}
			f.s.Limiter().Reset("2fa:alice")
			rec = c.post("/api/auth/2fa/backup-codes", map[string]string{"current_password": goodPW, "code": f.code(t)})
			var bc struct {
				Codes []string `json:"backup_codes"`
			}
			_ = json.Unmarshal(rec.Body.Bytes(), &bc)
			if rec.Code != 200 || len(bc.Codes) != 10 || rec.Header().Get("Cache-Control") != "no-store" || !f.aud.has("alice|2fa_backup_regenerated|") {
				t.Fatalf("regen: %d %s", rec.Code, rec.Body)
			}
			// old codes are dead
			fresh := newClient(t, f.s)
			if rec := fresh.step2(f.step1(t, fresh), f.codes[2]); rec.Code != 401 {
				t.Fatalf("old backup code accepted: %d", rec.Code)
			}
			f.s.Limiter().Reset("user:alice", "ip:203.0.113.9", "2fa:alice")
			f.clk.Add(31 * time.Second)
			rec = c.post("/api/auth/2fa/disable", map[string]string{"current_password": goodPW, "code": bc.Codes[0]})
			if rec.Code != 204 || !f.aud.has("alice|2fa_disabled|") {
				t.Fatalf("disable: %d %s", rec.Code, rec.Body)
			}
			if rec := other.do("GET", "/api/read", nil, nil); rec.Code != 401 {
				t.Fatalf("other session survived disable: %d", rec.Code)
			}
			if rec := c.do("GET", "/api/read", nil, nil); rec.Code != 200 {
				t.Fatalf("current session lost: %d", rec.Code)
			}
			// plain login works again
			if rec := newClient(t, f.s).login("alice", goodPW); rec.Code != 200 || strings.Contains(rec.Body.String(), "mfa") {
				t.Fatalf("login after disable: %s", rec.Body)
			}
		})
	}
}

func TestEnableRevokesOtherSessionsAndLeaksNoSecrets(t *testing.T) {
	s, clk := newSvc(t, false)
	withKey(t, s)
	mustUser(t, s, "alice", RoleViewer, false)
	c, other := newClient(t, s), newClient(t, s)
	c.login("alice", goodPW)
	other.login("alice", goodPW)
	if rec := c.post("/api/auth/2fa/setup", map[string]string{}); rec.Code != 401 {
		t.Fatalf("setup without pw: %d", rec.Code)
	}
	rec := c.post("/api/auth/2fa/setup", map[string]string{"current_password": goodPW})
	var v struct {
		Secret string `json:"secret"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	// a wrong enable code is a 401; 2FA stays off
	if rec := c.post("/api/auth/2fa/enable", map[string]string{"code": "000000"}); rec.Code != 401 {
		t.Fatalf("enable bad: %d", rec.Code)
	}
	code, _ := Code(v.Secret, TOTPStep(clk.Now()))
	if rec := c.post("/api/auth/2fa/enable", map[string]string{"code": code}); rec.Code != 200 {
		t.Fatalf("enable: %d %s", rec.Code, rec.Body)
	}
	if rec := other.do("GET", "/api/read", nil, nil); rec.Code != 401 {
		t.Fatalf("other session survived enable: %d", rec.Code)
	}
	// setup again while enabled -> 409, and no secret in status/me/login responses
	if rec := c.post("/api/auth/2fa/setup", map[string]string{"current_password": goodPW}); rec.Code != 409 || strings.Contains(rec.Body.String(), v.Secret) {
		t.Fatalf("setup when enabled: %d %s", rec.Code, rec.Body)
	}
	if rec := c.post("/api/auth/2fa/enable", map[string]string{"code": code}); rec.Code != 409 {
		t.Fatalf("enable twice: %d", rec.Code)
	}
	for _, p := range []string{"/api/auth/2fa", "/api/auth/me"} {
		if rec := c.do("GET", p, nil, nil); strings.Contains(rec.Body.String(), v.Secret) || strings.Contains(rec.Body.String(), "otpauth") {
			t.Fatalf("%s leaks: %s", p, rec.Body)
		}
	}
	if rec := newClient(t, s).do("POST", "/api/auth/2fa/setup", map[string]string{"current_password": goodPW}, nil); rec.Code != 401 {
		t.Fatalf("anon setup: %d", rec.Code)
	}
}

func TestSetupWithoutKeyIs503(t *testing.T) {
	s, _ := newSvc(t, false)
	mustUser(t, s, "alice", RoleViewer, false)
	c := newClient(t, s)
	c.login("alice", goodPW)
	rec := c.post("/api/auth/2fa/setup", map[string]string{"current_password": goodPW})
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "secret key") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	// the server problem is not a wrong guess: many repeats never lock the key
	for i := 0; i < 8; i++ {
		if rec := c.post("/api/auth/2fa/setup", map[string]string{"current_password": goodPW}); rec.Code != 503 {
			t.Fatalf("repeat %d: %d", i, rec.Code)
		}
	}
	if rec := c.post("/api/auth/2fa/enable", map[string]string{"code": "123456"}); rec.Code != 503 {
		t.Fatalf("enable w/o key: %d", rec.Code)
	}
}

// A password login that read the user before two-factor was enabled must not end up with a
// password-only session: the session insert re-checks the live flag.
func TestLoginRacingTwoFactorEnableGetsChallenge(t *testing.T) {
	s, clk := newSvc(t, false)
	withKey(t, s)
	u := mustUser(t, s, "alice", RoleViewer, false)
	c := newClient(t, s)
	fired := false
	s.Store().afterLoginRead = func() {
		if fired {
			return
		}
		fired = true
		// the enable flow completes (flag set, other sessions revoked) mid-login
		enableTOTP(t, s.Store(), u, clk.Now())
	}
	rec := c.login("alice", goodPW)
	if !fired {
		t.Fatal("race hook never ran")
	}
	if rec.Code != 200 || len(rec.Result().Cookies()) != 0 || c.cookie != nil {
		t.Fatalf("login %d cookies=%v body=%s", rec.Code, rec.Result().Cookies(), rec.Body)
	}
	var v struct {
		MFA bool `json:"mfa_required"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	if !v.MFA {
		t.Fatalf("want mfa_required, got %s", rec.Body)
	}
	var n int
	if err := s.store.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE user_id = ?`, u.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("a session exists (n=%d err=%v)", n, err)
	}
}

// Losing the race must not clear the account's password-failure counter.
func TestLoginRaceFallbackKeepsFailureCount(t *testing.T) {
	s, clk := newSvc(t, false)
	withKey(t, s)
	u := mustUser(t, s, "alice", RoleViewer, false)
	c := newClient(t, s)
	for i := 0; i < 3; i++ {
		if rec := c.login("alice", "wrong-password-"+strings.Repeat("x", i)); rec.Code != 401 {
			t.Fatalf("setup guess %d: %d", i, rec.Code)
		}
	}
	fired := false
	s.Store().afterLoginRead = func() {
		if !fired {
			fired = true
			enableTOTP(t, s.Store(), u, clk.Now())
		}
	}
	if rec := c.login("alice", goodPW); !strings.Contains(rec.Body.String(), "mfa_required") {
		t.Fatalf("want challenge, got %d %s", rec.Code, rec.Body)
	}
	// 3 failures are still on the account: two more reach the threshold of 5
	for i := 0; i < 2; i++ {
		c.login("alice", "wrong-again")
	}
	if rec := c.login("alice", goodPW); rec.Code != 429 {
		t.Fatalf("failure counter was cleared: got %d", rec.Code)
	}
}

// A login that loses the race to two-factor enablement only gets a challenge; the session it
// came in with (which the enable flow may have retained) must survive.
func TestLoginRaceFallbackKeepsExistingSession(t *testing.T) {
	s, clk := newSvc(t, false)
	withKey(t, s)
	u := mustUser(t, s, "alice", RoleViewer, false)
	c := newClient(t, s)
	if rec := c.login("alice", goodPW); rec.Code != 200 {
		t.Fatalf("first login %d", rec.Code)
	}
	old := c.cookie
	fired := false
	s.Store().afterLoginRead = func() {
		if !fired {
			fired = true
			enableTOTP(t, s.Store(), u, clk.Now()) // enabled, this session retained
		}
	}
	rec := c.do("POST", "/api/auth/login", map[string]string{"username": "alice", "password": goodPW}, nil)
	if !strings.Contains(rec.Body.String(), "mfa_required") {
		t.Fatalf("want challenge, got %d %s", rec.Code, rec.Body)
	}
	if c.cookie != old {
		t.Fatal("test client lost its cookie")
	}
	if rec := c.do("GET", "/api/auth/me", nil, nil); rec.Code != 200 {
		t.Fatalf("existing session was destroyed: /me %d", rec.Code)
	}
}

// A successful login still retires the session it came in with.
func TestLoginRetiresPreviousSession(t *testing.T) {
	s, _ := newSvc(t, false)
	mustUser(t, s, "alice", RoleViewer, false)
	c := newClient(t, s)
	c.login("alice", goodPW)
	old := c.cookie
	if rec := c.login("alice", goodPW); rec.Code != 200 {
		t.Fatalf("second login %d", rec.Code)
	}
	if c.cookie == nil || c.cookie.Value == old.Value {
		t.Fatal("session cookie was not replaced")
	}
	stale := newClient(t, s)
	stale.cookie = old
	if rec := stale.do("GET", "/api/auth/me", nil, nil); rec.Code != 401 {
		t.Fatalf("old session still valid: %d", rec.Code)
	}
}

// Logging in at the session cap while carrying a session that is about to be retired must
// not cost another device its session.
func TestLoginAtCapRetiresOnlyThePreviousSession(t *testing.T) {
	s, _ := newSvc(t, false)
	u := mustUser(t, s, "alice", RoleViewer, false)
	ctx := context.Background()
	var toks []string
	for i := 0; i < MaxSessionsPerUser; i++ {
		tok, _, err := s.Store().CreateSession(ctx, u.ID, "")
		if err != nil {
			t.Fatal(err)
		}
		toks = append(toks, tok)
	}
	c := newClient(t, s)
	c.cookie = &http.Cookie{Name: s.CookieName(), Value: toks[len(toks)-1]} // the most recent one
	if rec := c.login("alice", goodPW); rec.Code != 200 {
		t.Fatalf("login %d", rec.Code)
	}
	for i, tok := range toks[:len(toks)-1] {
		if _, _, err := s.Store().Check(ctx, tok); err != nil {
			t.Fatalf("session %d of another device was lost: %v", i, err)
		}
	}
}
