package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/t0mer/cic/internal/store"
)

func TestMain(m *testing.M) {
	UseFastHashingForTests()
	m.Run()
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

const goodPW = "correct horse battery"

func newSvc(t *testing.T, insecure bool, trusted ...string) (*Service, *fakeClock) {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	clk := &fakeClock{t: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	nets, err := ParseProxies(trusted)
	if err != nil {
		t.Fatal(err)
	}
	return NewService(Config{DB: db, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		InsecureCookies: insecure, TrustedProxies: nets, Now: clk.Now}), clk
}

func mustUser(t *testing.T, s *Service, name string, role Role, must bool) User {
	t.Helper()
	u, err := s.Store().CreateUser(context.Background(), name, goodPW, role, must)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// ---- passwords ----

func TestPasswordHashing(t *testing.T) {
	h, err := HashPassword("s3cret-pass-word")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=") {
		t.Fatalf("not PHC: %q", h)
	}
	h2, _ := HashPassword("s3cret-pass-word")
	if h == h2 {
		t.Fatal("salt not random")
	}
	cases := []struct {
		name, pw, hash string
		want           bool
	}{
		{"ok", "s3cret-pass-word", h, true},
		{"wrong", "s3cret-pass-wor", h, false},
		{"empty", "", h, false},
		{"garbage hash", "x", "not-a-hash", false},
		{"huge cost rejected", "x", "$argon2id$v=19$m=4294967295,t=1,p=1$c2FsdHNhbHRzYWx0$a2V5a2V5a2V5a2V5a2V5", false},
	}
	for _, c := range cases {
		if got := VerifyPassword(c.pw, c.hash); got != c.want {
			t.Errorf("%s: got %v", c.name, got)
		}
	}
	if VerifyDummy("anything") {
		t.Fatal("dummy verify must never succeed")
	}
}

func TestProductionCostParameters(t *testing.T) {
	old := cost
	defer func() { cost = old }()
	cost = defaultCost
	h, _ := HashPassword("x")
	if !strings.Contains(h, "m=65536,t=3,p=2$") {
		t.Fatalf("unexpected cost in %q", h)
	}
}

func TestValidatePassword(t *testing.T) {
	cases := []struct {
		pw, user string
		ok       bool
	}{
		{"correct horse battery", "alice", true},
		{"short", "alice", false},
		{"elevenchars", "alice", false},
		{"twelvechars!", "alice", true},
		{"aaaaaaaaaaaaaaaa", "alice", false},
		{"Alice.Example", "alice.example", false},
		{strings.Repeat("x", 300) + "y", "alice", false},
	}
	for _, c := range cases {
		if err := ValidatePassword(c.pw, c.user); (err == nil) != c.ok {
			t.Errorf("%q: err=%v", c.pw, err)
		}
	}
}

func TestUsernameValidationAndCaseInsensitivity(t *testing.T) {
	s, _ := newSvc(t, false)
	ctx := context.Background()
	for _, bad := range []string{"ab", "has space", "semi;colon", strings.Repeat("a", 65), ""} {
		if _, err := s.Store().CreateUser(ctx, bad, goodPW, RoleViewer, false); !errors.Is(err, ErrBadUsername) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if _, err := s.Store().CreateUser(ctx, "Alice", goodPW, RoleViewer, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store().CreateUser(ctx, "aLICE", goodPW, RoleViewer, false); !errors.Is(err, ErrUserExists) {
		t.Fatalf("want ErrUserExists, got %v", err)
	}
	if u, _, err := s.Store().UserByName(ctx, "ALICE"); err != nil || u.Username != "Alice" {
		t.Fatalf("lookup: %v %v", u, err)
	}
	if _, err := s.Store().CreateUser(ctx, "bob", goodPW, "root", false); !errors.Is(err, ErrBadRole) {
		t.Fatalf("role: %v", err)
	}
}

// ---- sessions ----

func TestSessionLifecycle(t *testing.T) {
	s, clk := newSvc(t, false)
	st := s.Store()
	ctx := context.Background()
	u := mustUser(t, s, "alice", RoleAdmin, false)

	tok, sess, err := st.CreateSession(ctx, u.ID, strings.Repeat("U", 500))
	if err != nil {
		t.Fatal(err)
	}
	if len(sess.UserAgent) != maxUserAgent {
		t.Fatalf("ua not trimmed: %d", len(sess.UserAgent))
	}
	if sess.IDHash == tok || len(sess.IDHash) != 64 {
		t.Fatalf("session id must be stored hashed, got %q", sess.IDHash)
	}
	var stored string
	if err := st.db.QueryRow(`SELECT id_hash FROM sessions`).Scan(&stored); err != nil || strings.Contains(stored, tok) {
		t.Fatalf("raw token stored? %v", err)
	}
	if _, _, err := st.Lookup(ctx, tok); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Lookup(ctx, "bogus"); !errors.Is(err, ErrNoSession) {
		t.Fatalf("bogus: %v", err)
	}

	// Idle expiry: activity keeps it alive; 12h of silence kills it.
	clk.Add(11 * time.Hour)
	if _, _, err := st.Lookup(ctx, tok); err != nil {
		t.Fatalf("should be alive at 11h: %v", err)
	}
	clk.Add(11 * time.Hour)
	if _, _, err := st.Lookup(ctx, tok); err != nil {
		t.Fatalf("activity should refresh idle timer: %v", err)
	}
	clk.Add(12*time.Hour + time.Second)
	if _, _, err := st.Lookup(ctx, tok); !errors.Is(err, ErrNoSession) {
		t.Fatalf("idle expiry: %v", err)
	}

	// Absolute expiry even with constant activity.
	tok, _, _ = st.CreateSession(ctx, u.ID, "")
	for i := 0; i < 20; i++ {
		clk.Add(11 * time.Hour)
		if _, _, err := st.Lookup(ctx, tok); err != nil {
			if (i+1)*11 <= 168 {
				t.Fatalf("expired early at %dh: %v", (i+1)*11, err)
			}
			return
		}
	}
	t.Fatal("absolute lifetime not enforced")
}

func TestSessionRotationPurgeAndInvalidation(t *testing.T) {
	s, clk := newSvc(t, false)
	st := s.Store()
	ctx := context.Background()
	u := mustUser(t, s, "alice", RoleViewer, false)
	tok, sess, _ := st.CreateSession(ctx, u.ID, "")
	other, _, _ := st.CreateSession(ctx, u.ID, "")

	nt, ns, err := st.Rotate(ctx, tok)
	if err != nil {
		t.Fatal(err)
	}
	if nt == tok || ns.CSRFToken == sess.CSRFToken || !ns.CreatedAt.Equal(sess.CreatedAt) {
		t.Fatal("rotation must change id and csrf and keep created_at")
	}
	if _, _, err := st.Lookup(ctx, tok); !errors.Is(err, ErrNoSession) {
		t.Fatal("old id still valid")
	}

	// Password change: other sessions die, current is rotated.
	nt2, _, err := st.ChangePassword(ctx, u.ID, "alice", "another long password", nt)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Lookup(ctx, nt); !errors.Is(err, ErrNoSession) {
		t.Fatal("pre-change id still valid")
	}
	if _, _, err := st.Lookup(ctx, other); !errors.Is(err, ErrNoSession) {
		t.Fatal("other session survived password change")
	}
	if _, _, err := st.Lookup(ctx, nt2); err != nil {
		t.Fatalf("rotated session should live: %v", err)
	}

	// Janitor purge.
	clk.Add(13 * time.Hour)
	n, err := st.PurgeExpired(ctx)
	if err != nil || n != 1 {
		t.Fatalf("purged %d, %v", n, err)
	}

	// Disabling kills sessions and blocks lookups.
	tok, _, _ = st.CreateSession(ctx, u.ID, "")
	if err := st.SetDisabled(ctx, u.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Lookup(ctx, tok); !errors.Is(err, ErrNoSession) {
		t.Fatal("disabled user session valid")
	}
}

// ---- HTTP ----

type client struct {
	t      *testing.T
	h      http.Handler
	cookie *http.Cookie
	csrf   string
}

func newHandler(s *Service) http.Handler {
	r := chi.NewRouter()
	r.Mount("/api/auth", s.Routes())
	r.Group(func(r chi.Router) {
		r.Use(s.RequireAuth)
		r.Get("/api/read", func(w http.ResponseWriter, r *http.Request) {
			u, _ := UserFrom(r.Context())
			_, _ = w.Write([]byte(u.Username))
		})
		r.Post("/api/write", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
		r.Group(func(r chi.Router) {
			r.Use(RequireRole(RoleAdmin))
			r.Get("/api/admin", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
			r.Post("/api/admin", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
		})
	})
	return r
}

func (c *client) do(method, path string, body any, hdr map[string]string) *httptest.ResponseRecorder {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Host = "cic.example"
	req.RemoteAddr = "203.0.113.9:5555"
	if c.cookie != nil {
		req.AddCookie(c.cookie)
	}
	if method != "GET" && method != "HEAD" {
		req.Header.Set("Origin", "http://cic.example") // what a same-origin browser sends
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	return rec
}

// post sends a state-changing request with the session's CSRF token.
func (c *client) post(path string, body any) *httptest.ResponseRecorder {
	return c.do("POST", path, body, map[string]string{CSRFHeader: c.csrf})
}

func (c *client) login(user, pw string) *httptest.ResponseRecorder {
	rec := c.do("POST", "/api/auth/login", map[string]string{"username": user, "password": pw}, nil)
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

func isSessionCookie(name string) bool {
	return name == CookieNameSecure || name == CookieNameInsecure
}

func newClient(t *testing.T, s *Service) *client { return &client{t: t, h: newHandler(s)} }

func TestLoginCookieAttributes(t *testing.T) {
	for _, insecure := range []bool{false, true} {
		t.Run("insecure="+strconv.FormatBool(insecure), func(t *testing.T) {
			s, _ := newSvc(t, insecure)
			mustUser(t, s, "alice", RoleViewer, false)
			c := newClient(t, s)
			rec := c.login("alice", goodPW)
			if rec.Code != 200 {
				t.Fatalf("login %d %s", rec.Code, rec.Body)
			}
			if rec.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("login must be no-store")
			}
			ck := c.cookie
			want := "__Host-cic_session"
			if insecure {
				want = "cic_session"
			}
			if ck == nil || ck.Name != want {
				t.Fatalf("cookie name = %v, want %s", ck, want)
			}
			if !ck.HttpOnly || ck.SameSite != http.SameSiteStrictMode || ck.Path != "/" || ck.Secure == insecure {
				t.Fatalf("attrs: httponly=%v samesite=%v path=%q secure=%v", ck.HttpOnly, ck.SameSite, ck.Path, ck.Secure)
			}
			var v sessionView
			_ = json.Unmarshal(rec.Body.Bytes(), &v)
			if v.User.Username != "alice" || v.User.Role != RoleViewer || v.CSRFToken == "" {
				t.Fatalf("body: %s", rec.Body)
			}
		})
	}
}

func TestLoginGenericFailures(t *testing.T) {
	s, _ := newSvc(t, false)
	mustUser(t, s, "alice", RoleViewer, false)
	d := mustUser(t, s, "dave", RoleViewer, false)
	_ = s.Store().SetDisabled(context.Background(), d.ID, true)
	cases := []struct{ name, user, pw string }{
		{"wrong password", "alice", "nope nope nope nope"},
		{"unknown user", "mallory", goodPW},
		{"disabled user", "dave", goodPW},
		{"empty", "", ""},
	}
	var bodies []string
	for _, c := range cases {
		cl := newClient(t, s)
		rec := cl.login(c.user, c.pw)
		if rec.Code != 401 || cl.cookie != nil {
			t.Errorf("%s: %d", c.name, rec.Code)
		}
		bodies = append(bodies, rec.Body.String())
	}
	for _, b := range bodies {
		if b != bodies[0] || !strings.Contains(b, "invalid credentials") {
			t.Fatalf("responses differ: %q vs %q", b, bodies[0])
		}
	}
	rec := newClient(t, s).do("POST", "/api/auth/login", nil, nil)
	if rec.Code != 400 {
		t.Fatalf("malformed: %d", rec.Code)
	}
}

func TestEndToEndAuthFlow(t *testing.T) {
	s, _ := newSvc(t, false)
	mustUser(t, s, "alice", RoleViewer, false)
	c := newClient(t, s)
	if rec := c.do("GET", "/api/read", nil, nil); rec.Code != 401 {
		t.Fatalf("anon: %d", rec.Code)
	}
	if rec := c.do("GET", "/api/auth/me", nil, nil); rec.Code != 401 {
		t.Fatalf("anon me: %d", rec.Code)
	}
	c.login("alice", goodPW)
	rec := c.do("GET", "/api/auth/me", nil, nil)
	var me map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &me)
	if rec.Code != 200 || me["username"] != "alice" || me["role"] != "viewer" || me["must_change_password"] != false || me["csrf_token"] != c.csrf {
		t.Fatalf("me: %d %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("me must be no-store")
	}
	if rec := c.do("GET", "/api/read", nil, nil); rec.Code != 200 || rec.Body.String() != "alice" {
		t.Fatalf("read: %d", rec.Code)
	}
	old := c.cookie
	if rec := c.post("/api/auth/logout", nil); rec.Code != 204 {
		t.Fatalf("logout: %d", rec.Code)
	} else if ck := rec.Result().Cookies(); len(ck) == 0 || ck[0].MaxAge >= 0 {
		t.Fatal("logout must clear cookie")
	}
	c.cookie = old // replaying the old cookie must fail: the session is gone server-side
	if rec := c.do("GET", "/api/read", nil, nil); rec.Code != 401 {
		t.Fatalf("after logout: %d", rec.Code)
	}
}

func TestCSRFMatrix(t *testing.T) {
	s, _ := newSvc(t, false)
	mustUser(t, s, "alice", RoleAdmin, false)
	c := newClient(t, s)
	c.login("alice", goodPW)
	cases := []struct {
		name string
		hdr  map[string]string
		want int
	}{
		{"valid token", map[string]string{CSRFHeader: c.csrf}, 204},
		{"valid + same origin", map[string]string{CSRFHeader: c.csrf, "Origin": "http://cic.example", "Sec-Fetch-Site": "same-origin"}, 204},
		{"missing token", nil, 403},
		{"wrong token", map[string]string{CSRFHeader: "wrong"}, 403},
		{"cross-site fetch", map[string]string{CSRFHeader: c.csrf, "Sec-Fetch-Site": "cross-site"}, 403},
		{"same-site (sibling) fetch", map[string]string{CSRFHeader: c.csrf, "Sec-Fetch-Site": "same-site"}, 403},
		{"foreign origin", map[string]string{CSRFHeader: c.csrf, "Origin": "https://evil.example"}, 403},
		{"null origin", map[string]string{CSRFHeader: c.csrf, "Origin": "null"}, 403},
		{"neither Origin nor Sec-Fetch-Site", map[string]string{CSRFHeader: c.csrf, "Origin": ""}, 403},
		{"Sec-Fetch-Site same-origin alone", map[string]string{CSRFHeader: c.csrf, "Origin": "", "Sec-Fetch-Site": "same-origin"}, 204},
		{"Sec-Fetch-Site none alone", map[string]string{CSRFHeader: c.csrf, "Origin": "", "Sec-Fetch-Site": "none"}, 204},
		{"https origin on a plain http request", map[string]string{CSRFHeader: c.csrf, "Origin": "https://cic.example"}, 403},
	}
	for _, tc := range cases {
		if rec := c.do("POST", "/api/write", nil, tc.hdr); rec.Code != tc.want {
			t.Errorf("%s: got %d want %d", tc.name, rec.Code, tc.want)
		}
	}
	// GETs need no token.
	if rec := c.do("GET", "/api/read", nil, nil); rec.Code != 200 {
		t.Errorf("GET: %d", rec.Code)
	}
	// Login only needs the origin check.
	for _, tc := range []struct {
		hdr  map[string]string
		want int
	}{
		{nil, 200},
		{map[string]string{"Origin": ""}, 403},
		{map[string]string{"Origin": "https://cic.example"}, 403},
		{map[string]string{"Origin": "null"}, 403},
		{map[string]string{"Sec-Fetch-Site": "cross-site"}, 403},
		{map[string]string{"Origin": "https://evil.example"}, 403},
		{map[string]string{"Origin": "http://cic.example", "Sec-Fetch-Site": "same-origin"}, 200},
	} {
		rec := newClient(t, s).do("POST", "/api/auth/login", map[string]string{"username": "alice", "password": goodPW}, tc.hdr)
		if rec.Code != tc.want {
			t.Errorf("login %v: %d", tc.hdr, rec.Code)
		}
	}
}

func TestRBACMatrix(t *testing.T) {
	s, _ := newSvc(t, false)
	mustUser(t, s, "alice", RoleAdmin, false)
	mustUser(t, s, "vera", RoleViewer, false)
	anon, viewer, admin := newClient(t, s), newClient(t, s), newClient(t, s)
	viewer.login("vera", goodPW)
	admin.login("alice", goodPW)
	cases := []struct {
		method, path string
		want         [3]int // anon, viewer, admin
	}{
		{"GET", "/api/read", [3]int{401, 200, 200}},
		{"POST", "/api/write", [3]int{401, 204, 204}},
		{"GET", "/api/admin", [3]int{401, 403, 204}},
		{"POST", "/api/admin", [3]int{401, 403, 204}},
	}
	for _, tc := range cases {
		for i, c := range []*client{anon, viewer, admin} {
			if rec := c.do(tc.method, tc.path, nil, map[string]string{CSRFHeader: c.csrf}); rec.Code != tc.want[i] {
				t.Errorf("%s %s as %d: got %d want %d", tc.method, tc.path, i, rec.Code, tc.want[i])
			}
		}
	}
}

func TestMustChangePassword(t *testing.T) {
	s, _ := newSvc(t, false)
	mustUser(t, s, "alice", RoleAdmin, true)
	c := newClient(t, s)
	rec := c.login("alice", goodPW)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"must_change_password":true`) {
		t.Fatalf("login: %s", rec.Body)
	}
	rec = c.do("GET", "/api/read", nil, nil)
	if rec.Code != 403 || strings.TrimSpace(rec.Body.String()) != `{"error":"password change required"}` {
		t.Fatalf("blocked: %d %s", rec.Code, rec.Body)
	}
	if rec := c.post("/api/write", nil); rec.Code != 403 {
		t.Fatalf("write: %d", rec.Code)
	}
	if rec := c.do("GET", "/api/auth/me", nil, nil); rec.Code != 200 {
		t.Fatalf("me: %d", rec.Code)
	}
	// wrong current, weak new, same as current
	for name, body := range map[string]map[string]string{
		"wrong current": {"current_password": "wrong wrong wrong", "new_password": "a brand new password"},
		"weak new":      {"current_password": goodPW, "new_password": "short"},
		"same":          {"current_password": goodPW, "new_password": goodPW},
	} {
		if rec := c.post("/api/auth/change-password", body); rec.Code == 200 {
			t.Errorf("%s accepted", name)
		}
	}
	oldCookie := c.cookie
	rec = c.post("/api/auth/change-password", map[string]string{"current_password": goodPW, "new_password": "a brand new password"})
	if rec.Code != 200 {
		t.Fatalf("change: %d %s", rec.Code, rec.Body)
	}
	var newCookie *http.Cookie
	for _, ck := range rec.Result().Cookies() {
		if isSessionCookie(ck.Name) {
			newCookie = ck
		}
	}
	if newCookie == nil || newCookie.Value == oldCookie.Value {
		t.Fatal("session must be rotated to a new id")
	}
	var v sessionView
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	c.cookie, c.csrf = newCookie, v.CSRFToken
	if rec := c.do("GET", "/api/read", nil, nil); rec.Code != 200 {
		t.Fatalf("after change: %d", rec.Code)
	}
	old := &client{t: t, h: c.h, cookie: oldCookie}
	if rec := old.do("GET", "/api/read", nil, nil); rec.Code != 401 {
		t.Fatalf("old session valid: %d", rec.Code)
	}
	if newClient(t, s).login("alice", goodPW).Code != 401 || newClient(t, s).login("alice", "a brand new password").Code != 200 {
		t.Fatal("password not replaced")
	}
}

func TestLoginLockoutAndBackoff(t *testing.T) {
	s, clk := newSvc(t, false)
	mustUser(t, s, "alice", RoleViewer, false)
	c := newClient(t, s)
	for i := 0; i < 5; i++ {
		if rec := c.login("alice", "wrong wrong wrong"); rec.Code != 401 {
			t.Fatalf("attempt %d: %d", i, rec.Code)
		}
	}
	// Locked: even the right password gets 429 and reveals nothing.
	for _, pw := range []string{goodPW, "wrong wrong wrong"} {
		rec := c.login("alice", pw)
		if rec.Code != 429 || rec.Header().Get("Retry-After") != "30" {
			t.Fatalf("locked: %d retry=%q", rec.Code, rec.Header().Get("Retry-After"))
		}
	}
	// Another IP is still limited by the username.
	other := newClient(t, s)
	rec := other.do("POST", "/api/auth/login", map[string]string{"username": "ALICE", "password": goodPW}, nil)
	_ = rec
	// (same RemoteAddr in helper; username key is case-insensitive)
	if rec.Code != 429 {
		t.Fatalf("username case variant: %d", rec.Code)
	}
	clk.Add(31 * time.Second)
	if rec := c.login("alice", "wrong wrong wrong"); rec.Code != 401 {
		t.Fatalf("after first lock: %d", rec.Code)
	}
	// Second lock is doubled to 60 s.
	if rec := c.login("alice", goodPW); rec.Code != 429 || rec.Header().Get("Retry-After") != "60" {
		t.Fatalf("doubled: %d %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	clk.Add(61 * time.Second)
	if rec := c.login("alice", goodPW); rec.Code != 200 {
		t.Fatalf("success after lock: %d", rec.Code)
	}
	// Success resets: 4 fresh failures do not lock.
	for i := 0; i < 4; i++ {
		c.login("alice", "wrong wrong wrong")
	}
	if rec := c.login("alice", goodPW); rec.Code != 200 {
		t.Fatalf("reset: %d", rec.Code)
	}
}

func TestLimiterCap(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	l := NewLimiter(clk.Now)
	for i := 0; i < 40; i++ {
		l.Fail("k")
	}
	if w := l.Check("k"); w != 15*time.Minute {
		t.Fatalf("cap: %v", w)
	}
	clk.Add(2 * time.Hour)
	l.Prune()
	if len(l.m) != 0 {
		t.Fatal("stale entries not pruned")
	}
}

func TestClientIP(t *testing.T) {
	nets, _ := ParseProxies([]string{"10.0.0.0/8", "192.0.2.1"})
	cases := []struct {
		name, remote string
		xff          []string
		trusted      []*net.IPNet
		want         string
	}{
		{"no proxies configured ignores XFF", "203.0.113.9:1", []string{"1.2.3.4"}, nil, "203.0.113.9"},
		{"untrusted peer ignores XFF", "203.0.113.9:1", []string{"1.2.3.4"}, nets, "203.0.113.9"},
		{"trusted peer uses XFF", "10.0.0.5:1", []string{"198.51.100.7"}, nets, "198.51.100.7"},
		{"spoofed left entry ignored", "10.0.0.5:1", []string{"6.6.6.6, 198.51.100.7"}, nets, "198.51.100.7"},
		{"skips trusted hops from the right", "10.0.0.5:1", []string{"198.51.100.7, 10.1.1.1, 192.0.2.1"}, nets, "198.51.100.7"},
		{"multiple header lines", "10.0.0.5:1", []string{"6.6.6.6", "198.51.100.7"}, nets, "198.51.100.7"},
		{"all trusted falls back to peer", "10.0.0.5:1", []string{"10.1.1.1"}, nets, "10.0.0.5"},
		{"garbage falls back to peer", "10.0.0.5:1", []string{"198.51.100.7, not-an-ip"}, nets, "10.0.0.5"},
		{"no header", "10.0.0.5:1", nil, nets, "10.0.0.5"},
		{"ipv6 peer", "[2001:db8::1]:99", []string{"1.2.3.4"}, nets, "2001:db8::1"},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.remote
		for _, x := range c.xff {
			r.Header.Add("X-Forwarded-For", x)
		}
		if got := ClientIP(r, c.trusted); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
	if _, err := ParseProxies([]string{"nonsense"}); err == nil {
		t.Fatal("bad proxy accepted")
	}
}

func TestLockoutUsesTrustedProxyClientIP(t *testing.T) {
	s, _ := newSvc(t, false, "203.0.113.0/24")
	c := newClient(t, s)
	try := func(xff string) int {
		return c.do("POST", "/api/auth/login", map[string]string{"username": "u" + xff, "password": "x"},
			map[string]string{"X-Forwarded-For": xff}).Code
	}
	for i := 0; i < 5; i++ {
		try("198.51.100.1")
	}
	if got := try("198.51.100.1"); got != 429 {
		t.Fatalf("same client behind proxy should be locked, got %d", got)
	}
	if got := try("198.51.100.2"); got != 401 {
		t.Fatalf("different client behind same proxy must not be locked, got %d", got)
	}
}

// ---- bootstrap ----

type capture struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (c *capture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Write(p)
}

func TestBootstrapRandom(t *testing.T) {
	s, _ := newSvc(t, false)
	var out, pwOut capture
	prev := bootstrapOut
	bootstrapOut = &pwOut
	t.Cleanup(func() { bootstrapOut = prev })
	log := slog.New(slog.NewTextHandler(&out, nil))
	if err := Bootstrap(context.Background(), s.Store(), log, func(string) string { return "" }); err != nil {
		t.Fatal(err)
	}
	line := out.buf.String()
	if !strings.Contains(line, "level=WARN") || !strings.Contains(line, "bootstrap admin created") || !strings.Contains(line, "username=admin") {
		t.Fatalf("log line: %q", line)
	}
	if strings.Contains(line, "password=") {
		t.Fatalf("password leaked into the structured log: %q", line)
	}
	printed := pwOut.buf.String()
	pw := printed[strings.Index(printed, "password=")+len("password="):]
	pw = strings.TrimSpace(pw)
	if len(pw) != 20 || strings.ContainsAny(pw, "0OIl1") {
		t.Fatalf("password shape (len %d)", len(pw))
	}
	u, hash, err := s.Store().UserByName(context.Background(), "admin")
	if err != nil || u.Role != RoleAdmin || !u.MustChangePassword || !VerifyPassword(pw, hash) {
		t.Fatalf("admin: %+v %v", u, err)
	}
	// Second start with users present: nothing happens, nothing logged.
	out.buf.Reset()
	if err := Bootstrap(context.Background(), s.Store(), log, func(string) string { return "" }); err != nil || out.buf.Len() != 0 {
		t.Fatalf("second bootstrap: %v %q", err, out.buf.String())
	}
}

func TestBootstrapFromEnv(t *testing.T) {
	const envPW = "operator-chosen-password"
	s, _ := newSvc(t, false)
	var out capture
	log := slog.New(slog.NewTextHandler(&out, nil))
	env := func(k string) string {
		if k == BootstrapPasswordEnv {
			return envPW
		}
		return ""
	}
	if err := Bootstrap(context.Background(), s.Store(), log, env); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.buf.String(), envPW) || strings.Contains(out.buf.String(), "password=") {
		t.Fatalf("password leaked to log: %q", out.buf.String())
	}
	u, hash, _ := s.Store().UserByName(context.Background(), "admin")
	if u.MustChangePassword || !VerifyPassword(envPW, hash) {
		t.Fatalf("env bootstrap wrong: %+v", u)
	}
	// A too-short env password is refused, without echoing it.
	s2, _ := newSvc(t, false)
	err := Bootstrap(context.Background(), s2.Store(), log, func(string) string { return "short" })
	if err == nil || strings.Contains(err.Error(), "short\"") {
		t.Fatalf("short env password: %v", err)
	}
}

func TestGeneratePasswordUnique(t *testing.T) {
	a, _ := GeneratePassword()
	b, _ := GeneratePassword()
	if a == b || len(a) != 20 {
		t.Fatal("not random")
	}
}

func TestSessionActive(t *testing.T) {
	s, clk := newSvc(t, false)
	u := mustUser(t, s, "alice", RoleViewer, false)
	_, sess, _ := s.Store().CreateSession(context.Background(), u.ID, "")
	if !s.SessionActive(context.Background(), sess) {
		t.Fatal("should be active")
	}
	clk.Add(13 * time.Hour)
	if s.SessionActive(context.Background(), sess) {
		t.Fatal("should be idle-expired")
	}
}
