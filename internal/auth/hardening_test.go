package auth

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/argon2"
)

func (c *client) loginFrom(addr, user, pw string) *httptest.ResponseRecorder {
	rec := c.do("POST", "/api/auth/login", map[string]string{"username": user, "password": pw},
		map[string]string{"X-Forwarded-For": addr})
	return rec
}

func failsOf(l *Limiter, key string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e := l.m[key]; e != nil {
		return e.fails
	}
	return 0
}

// ---- limiter ----

func TestConcurrentWrongPasswordsCannotExceedThreshold(t *testing.T) {
	s, _ := newSvc(t, false)
	mustUser(t, s, "alice", RoleViewer, false)
	h := newHandler(s)
	var mu sync.Mutex
	codes := map[int]int{}
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			c := &client{t: t, h: h}
			code := c.login("alice", "wrong wrong wrong").Code
			mu.Lock()
			codes[code]++
			mu.Unlock()
		}()
	}
	close(start)
	wg.Wait()
	if codes[401] > s.limiter.Threshold || codes[401] == 0 || codes[401]+codes[429] != 200 {
		t.Fatalf("verdicts: %v (threshold %d)", codes, s.limiter.Threshold)
	}
}

func TestSuccessDoesNotClearIPCounter(t *testing.T) {
	s, _ := newSvc(t, false)
	s.limiter.IPThreshold = 6
	mustUser(t, s, "mallory", RoleViewer, false)
	c := newClient(t, s)
	// The attacker guesses at other accounts and logs in as himself in between.
	for i := 0; i < 6; i++ {
		if rec := c.login(fmt.Sprintf("victim%d", i), "wrong wrong wrong"); rec.Code != 401 {
			t.Fatalf("guess %d: %d", i, rec.Code)
		}
		if i < 5 {
			if rec := c.login("mallory", goodPW); rec.Code != 200 {
				t.Fatalf("own login %d: %d", i, rec.Code)
			}
		}
	}
	if got := failsOf(s.limiter, "ip:203.0.113.9"); got != 6 {
		t.Fatalf("ip failures = %d, want 6 (successes must not reset them)", got)
	}
	if rec := c.login("mallory", goodPW); rec.Code != 429 {
		t.Fatalf("source should now be locked: %d", rec.Code)
	}
}

func TestIPBucketIsMuchHigherThanUserBucket(t *testing.T) {
	s, _ := newSvc(t, false)
	if s.limiter.IPThreshold < 6*s.limiter.Threshold {
		t.Fatalf("ip threshold %d not much higher than user threshold %d", s.limiter.IPThreshold, s.limiter.Threshold)
	}
	mustUser(t, s, "alice", RoleViewer, false)
	c := newClient(t, s)
	// One source fails against many different usernames, each only once or twice.
	for i := 0; i < s.limiter.IPThreshold-1; i++ {
		if rec := c.login(fmt.Sprintf("someone%d", i), "wrong wrong wrong"); rec.Code != 401 {
			t.Fatalf("attempt %d: %d", i, rec.Code)
		}
	}
	// A username this client has not failed on is not locked out by those failures.
	if rec := c.login("alice", goodPW); rec.Code != 200 {
		t.Fatalf("untouched user locked out by IP noise: %d", rec.Code)
	}
	// ...until the IP bucket's own (high) threshold is reached.
	if rec := c.login("someoneelse", "wrong wrong wrong"); rec.Code != 401 {
		t.Fatalf("threshold attempt: %d", rec.Code)
	}
	if rec := c.login("alice", goodPW); rec.Code != 429 {
		t.Fatalf("ip lockout expected: %d", rec.Code)
	}
}

func TestCancelledRequestsAreNotFailures(t *testing.T) {
	s, _ := newSvc(t, false)
	mustUser(t, s, "alice", RoleViewer, false)
	s.hashSem = make(chan struct{}, 1)
	s.hashSem <- struct{}{} // no slot: only the context can end the wait
	h := newHandler(s)
	for i := 0; i < 20; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		req := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(`{"username":"alice","password":"wrong wrong wrong"}`)).WithContext(ctx)
		req.Host, req.RemoteAddr = "cic.example", "203.0.113.9:1"
		req.Header.Set("Origin", "http://cic.example")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Body.Len() != 0 {
			t.Fatalf("a cancelled request got a verdict: %d %s", rec.Code, rec.Body)
		}
	}
	<-s.hashSem
	if f, u := failsOf(s.limiter, "ip:203.0.113.9"), failsOf(s.limiter, "user:alice"); f != 0 || u != 0 {
		t.Fatalf("cancelled requests counted as failures: ip=%d user=%d", f, u)
	}
	if rec := newClient(t, s).login("alice", goodPW); rec.Code != 200 {
		t.Fatalf("login after cancellations: %d", rec.Code)
	}
}

func TestBusyReturns503WithoutFailure(t *testing.T) {
	s, _ := newSvc(t, false)
	mustUser(t, s, "alice", RoleViewer, false)
	s.hashSem = make(chan struct{}, 1)
	s.hashWait = 40 * time.Millisecond
	s.hashSem <- struct{}{}
	c := newClient(t, s)
	for i := 0; i < 10; i++ {
		rec := c.login("alice", "wrong wrong wrong")
		if rec.Code != 503 || strings.TrimSpace(rec.Body.String()) != `{"error":"busy"}` {
			t.Fatalf("busy: %d %s", rec.Code, rec.Body)
		}
	}
	if f := failsOf(s.limiter, "user:alice"); f != 0 {
		t.Fatalf("busy counted as failure: %d", f)
	}
	<-s.hashSem
	if rec := c.login("alice", goodPW); rec.Code != 200 {
		t.Fatalf("after busy: %d", rec.Code)
	}
}

func TestChangePasswordHasItsOwnLockoutKey(t *testing.T) {
	s, _ := newSvc(t, false)
	mustUser(t, s, "alice", RoleViewer, false)
	c := newClient(t, s)
	if rec := c.login("alice", goodPW); rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	// An attacker locks the login key...
	atk := newClient(t, s)
	for i := 0; i < 5; i++ {
		atk.login("alice", "wrong wrong wrong")
	}
	if rec := atk.login("alice", goodPW); rec.Code != 429 {
		t.Fatalf("login should be locked: %d", rec.Code)
	}
	// ...but the signed-in user can still change the password.
	if rec := c.post("/api/auth/change-password", map[string]string{"current_password": "wrong wrong wrong", "new_password": "a brand new password"}); rec.Code != 401 {
		t.Fatalf("change with wrong current while login is locked: %d (want 401, not 429)", rec.Code)
	}
	s2, _ := newSvc(t, false)
	mustUser(t, s2, "bob", RoleViewer, false)
	b := newClient(t, s2)
	b.login("bob", goodPW)
	// The user's own wrong current-password attempts lock change-password, not login.
	for i := 0; i < 5; i++ {
		if rec := b.post("/api/auth/change-password", map[string]string{"current_password": "wrong wrong wrong", "new_password": "a brand new password"}); rec.Code != 401 {
			t.Fatalf("attempt %d: %d", i, rec.Code)
		}
	}
	if rec := b.post("/api/auth/change-password", map[string]string{"current_password": goodPW, "new_password": "a brand new password"}); rec.Code != 429 {
		t.Fatalf("change-password should be locked: %d", rec.Code)
	}
	if rec := newClient(t, s2).login("bob", goodPW); rec.Code != 200 {
		t.Fatalf("login must not be locked by chpw failures: %d", rec.Code)
	}
}

func TestLimiterEntryCapIsStrict(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	l := NewLimiter(clk.Now)
	l.maxEntries = 1000
	for i := 0; i < 5000; i++ {
		clk.Add(time.Millisecond)
		k := fmt.Sprintf("user:u%d", i)
		l.Fail(k)
		if len(l.m) > 1000 {
			t.Fatalf("entry %d: %d entries exceed cap", i, len(l.m))
		}
	}
	// A locked key survives a spray of unlocked ones.
	for i := 0; i < 5; i++ {
		l.Fail("user:victim")
	}
	for i := 0; i < 3000; i++ {
		clk.Add(time.Millisecond)
		l.Fail(fmt.Sprintf("user:spray%d", i))
	}
	if l.Check("user:victim") == 0 {
		t.Fatal("locked key evicted by a spray")
	}
	// In-flight attempts are never lost.
	att, _ := l.Begin("user:inflight")
	for i := 0; i < 3000; i++ {
		l.Fail(fmt.Sprintf("user:more%d", i))
	}
	att.Fail()
	if got := failsOf(l, "user:inflight"); got != 1 {
		t.Fatalf("in-flight attempt lost, fails=%d", got)
	}
}

func TestIPv6SprayStaysBounded(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	l := NewLimiter(clk.Now) // default cap: 100k
	for i := 0; i < 150_000; i++ {
		ip := net.IP{0x20, 0x01, 0x0d, 0xb8, byte(i >> 24), byte(i >> 16), byte(i >> 8), byte(i), 0, 0, 0, 0, 0, 0, 0, 1}
		att, _ := l.Begin(ipPrefix + limiterIP(ip.String()))
		att.Fail()
		if len(l.m) > maxEntries {
			t.Fatalf("after %d addresses: %d entries", i, len(l.m))
		}
	}
	if len(l.m) == 0 || len(l.m) > maxEntries {
		t.Fatalf("entries: %d", len(l.m))
	}
}

func TestLimiterIPKeys(t *testing.T) {
	cases := map[string]string{
		"203.0.113.9":                          "203.0.113.9",
		"2001:db8:1:2:aaaa:bbbb:cccc:dddd":     "2001:db8:1:2::/64",
		"2001:db8:1:2:1111:2222:3333:4444":     "2001:db8:1:2::/64",
		"fe80::1%eth0":                         "fe80::/64",
		"::ffff:203.0.113.9":                   "203.0.113.9",
		"not-an-ip":                            "not-an-ip",
		"2001:db8:1:3:aaaa:bbbb:cccc:dddd%en0": "2001:db8:1:3::/64",
	}
	for in, want := range cases {
		if got := limiterIP(in); got != want {
			t.Errorf("limiterIP(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSameIPv6PrefixSharesOneBucket(t *testing.T) {
	s, _ := newSvc(t, false)
	s.limiter.IPThreshold = 3
	c := newClient(t, s)
	try := func(addr, user string) int {
		req := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(fmt.Sprintf(`{"username":%q,"password":"wrong wrong wrong"}`, user)))
		req.Host, req.RemoteAddr = "cic.example", addr
		req.Header.Set("Origin", "http://cic.example")
		rec := httptest.NewRecorder()
		c.h.ServeHTTP(rec, req)
		return rec.Code
	}
	for i := 0; i < 3; i++ {
		try(fmt.Sprintf("[2001:db8:5:5:%x::1]:1000", i+1), fmt.Sprintf("user%d", i))
	}
	if got := try("[2001:db8:5:5:ffff::9]:1000", "another"); got != 429 {
		t.Fatalf("same /64 should be locked: %d", got)
	}
	if got := try("[2001:db8:5:6::9]:1000", "another"); got != 401 {
		t.Fatalf("other /64 should not be locked: %d", got)
	}
}

// ---- origin / cookies ----

func TestOriginSchemeMustMatch(t *testing.T) {
	mk := func(baseURL string, trusted ...string) *Service {
		s, _ := newSvc(t, false, trusted...)
		s.httpsURL = strings.HasPrefix(baseURL, "https://")
		return s
	}
	req := func(origin, sfs string, withTLS bool, remote string, xfp string) *http.Request {
		r := httptest.NewRequest("POST", "/", nil)
		r.Host, r.RemoteAddr = "cic.example", remote
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if sfs != "" {
			r.Header.Set("Sec-Fetch-Site", sfs)
		}
		if xfp != "" {
			r.Header.Set("X-Forwarded-Proto", xfp)
		}
		if withTLS {
			r.TLS = &tls.ConnectionState{}
		}
		return r
	}
	plain, httpsBase, proxied := mk(""), mk("https://cic.example"), mk("", "10.0.0.0/8")
	cases := []struct {
		name string
		s    *Service
		r    *http.Request
		want bool
	}{
		{"http origin, plain http", plain, req("http://cic.example", "", false, "1.2.3.4:1", ""), true},
		{"https origin, plain http", plain, req("https://cic.example", "", false, "1.2.3.4:1", ""), false},
		{"http origin, https base_url", httpsBase, req("http://cic.example", "", false, "1.2.3.4:1", ""), false},
		{"https origin, https base_url", httpsBase, req("https://cic.example", "", false, "1.2.3.4:1", ""), true},
		{"http origin over TLS", plain, req("http://cic.example", "", true, "1.2.3.4:1", ""), false},
		{"https origin over TLS", plain, req("https://cic.example", "", true, "1.2.3.4:1", ""), true},
		{"trusted proxy says https", proxied, req("https://cic.example", "", false, "10.0.0.5:1", "https"), true},
		{"trusted proxy says https, http origin", proxied, req("http://cic.example", "", false, "10.0.0.5:1", "https"), false},
		{"untrusted peer cannot claim https", proxied, req("https://cic.example", "", false, "1.2.3.4:1", "https"), false},
		{"wrong host", plain, req("http://evil.example", "", false, "1.2.3.4:1", ""), false},
		{"null", plain, req("null", "", false, "1.2.3.4:1", ""), false},
		{"neither header", plain, req("", "", false, "1.2.3.4:1", ""), false},
		{"fetch-site none only", plain, req("", "none", false, "1.2.3.4:1", ""), true},
		{"fetch-site cross-site", plain, req("http://cic.example", "cross-site", false, "1.2.3.4:1", ""), false},
	}
	for _, c := range cases {
		if got := c.s.sameOrigin(c.r); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestOnlyTheActiveCookieNameIsAccepted(t *testing.T) {
	for _, insecure := range []bool{false, true} {
		s, _ := newSvc(t, insecure)
		mustUser(t, s, "alice", RoleViewer, false)
		c := newClient(t, s)
		c.login("alice", goodPW)
		wrong := CookieNameInsecure
		if insecure {
			wrong = CookieNameSecure
		}
		if s.CookieName() == wrong || c.cookie.Name != s.CookieName() {
			t.Fatalf("insecure=%v: cookie %q service %q", insecure, c.cookie.Name, s.CookieName())
		}
		other := &client{t: t, h: c.h, cookie: &http.Cookie{Name: wrong, Value: c.cookie.Value}}
		if rec := other.do("GET", "/api/read", nil, nil); rec.Code != 401 {
			t.Errorf("insecure=%v: cookie %q accepted: %d", insecure, wrong, rec.Code)
		}
		if rec := c.do("GET", "/api/read", nil, nil); rec.Code != 200 {
			t.Errorf("insecure=%v: right cookie rejected: %d", insecure, rec.Code)
		}
		if !insecure && (!c.cookie.Secure || c.cookie.Path != "/" || c.cookie.Domain != "") {
			t.Errorf("__Host- cookie must be Secure, Path=/, no Domain: %+v", c.cookie)
		}
	}
}

// ---- passwords ----

func TestStrictPHCParsing(t *testing.T) {
	old := floor
	floor = argonParams{memKiB: 19456, time: 2, threads: 1}
	defer func() { floor = old }()
	salt := []byte("0123456789abcdef")
	key := argon2.IDKey([]byte("pw"), salt, 2, 19456, 1, 32)
	enc := func(frag string) string {
		return fmt.Sprintf("$argon2id$v=19$%s$%s$%s", frag, base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key))
	}
	if !VerifyPassword("pw", enc("m=19456,t=2,p=1")) {
		t.Fatal("floor parameters rejected")
	}
	b64s, b64k := base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)
	bad := map[string]string{
		"trailing after v":       "$argon2id$v=19x$m=19456,t=2,p=1$" + b64s + "$" + b64k,
		"trailing after p":       "$argon2id$v=19$m=19456,t=2,p=1x$" + b64s + "$" + b64k,
		"trailing after p comma": "$argon2id$v=19$m=19456,t=2,p=1,q=3$" + b64s + "$" + b64k,
		"leading zero":           "$argon2id$v=19$m=019456,t=2,p=1$" + b64s + "$" + b64k,
		"wrong version":          "$argon2id$v=16$m=19456,t=2,p=1$" + b64s + "$" + b64k,
		"extra field":            enc("m=19456,t=2,p=1") + "$x",
		"trailing newline":       enc("m=19456,t=2,p=1") + "\n",
		"memory below floor":     enc("m=19455,t=2,p=1"),
		"time below floor":       enc("m=19456,t=1,p=1"),
		"zero parallelism":       enc("m=19456,t=2,p=0"),
		"memory above ceiling":   enc("m=262145,t=2,p=1"),
		"time above ceiling":     enc("m=19456,t=11,p=1"),
		"parallelism > ceiling":  enc("m=19456,t=2,p=17"),
		"p overflows uint8":      enc("m=19456,t=2,p=257"),
		"not argon2id":           strings.Replace(enc("m=19456,t=2,p=1"), "argon2id", "argon2i", 1),
		"empty":                  "",
		"bad base64":             "$argon2id$v=19$m=19456,t=2,p=1$!!!$" + b64k,
		"downgraded fast params": "$argon2id$v=19$m=64,t=1,p=1$" + b64s + "$" + b64k,
	}
	for name, h := range bad {
		if VerifyPassword("pw", h) {
			t.Errorf("%s: accepted", name)
		}
		if _, _, _, ok := parseHash(h); ok {
			t.Errorf("%s: parsed", name)
		}
	}
}

func TestLoginRehashesOutdatedParameters(t *testing.T) {
	s, _ := newSvc(t, false)
	u := mustUser(t, s, "alice", RoleViewer, false)
	_, before, _ := s.Store().UserByName(context.Background(), "alice")
	oldCost := cost
	defer func() { cost = oldCost }()
	cost = argonParams{memKiB: 128, time: 2, threads: 1} // "operator raised the cost"
	if rec := newClient(t, s).login("alice", "wrong wrong wrong"); rec.Code != 401 {
		t.Fatal(rec.Code)
	}
	if _, h, _ := s.Store().UserByName(context.Background(), "alice"); h != before {
		t.Fatal("a failed login must not rehash")
	}
	c := newClient(t, s)
	if rec := c.login("alice", goodPW); rec.Code != 200 {
		t.Fatalf("login: %d", rec.Code)
	}
	_, after, _ := s.Store().UserByName(context.Background(), "alice")
	if after == before || !strings.Contains(after, "m=128,t=2,p=1$") {
		t.Fatalf("hash not upgraded: %q", after)
	}
	if ok, stale := verifyPassword(goodPW, after); !ok || stale {
		t.Fatalf("upgraded hash: ok=%v stale=%v", ok, stale)
	}
	if rec := newClient(t, s).login("alice", goodPW); rec.Code != 200 {
		t.Fatalf("login with upgraded hash: %d", rec.Code)
	}
	// sessions survive the transparent upgrade
	if rec := c.do("GET", "/api/read", nil, nil); rec.Code != 200 {
		t.Fatalf("session dropped by rehash: %d", rec.Code)
	}
	// a concurrent password change wins over a stale upgrade
	if err := s.Store().replaceHash(context.Background(), u.ID, before, "$x"); err != nil {
		t.Fatal(err)
	}
	if _, h, _ := s.Store().UserByName(context.Background(), "alice"); h != after {
		t.Fatal("replaceHash is not compare-and-set")
	}
}

// ---- sessions & streams ----

func TestSessionCapEvictsLeastRecentlyUsed(t *testing.T) {
	s, clk := newSvc(t, false)
	u := mustUser(t, s, "alice", RoleViewer, false)
	other := mustUser(t, s, "bob", RoleViewer, false)
	ctx := context.Background()
	var toks []string
	for i := 0; i < MaxSessionsPerUser; i++ {
		clk.Add(2 * time.Minute)
		tok, _, err := s.Store().CreateSession(ctx, u.ID, "")
		if err != nil {
			t.Fatal(err)
		}
		toks = append(toks, tok)
	}
	bobTok, _, _ := s.Store().CreateSession(ctx, other.ID, "")
	clk.Add(2 * time.Minute)
	if _, _, err := s.Store().Lookup(ctx, toks[0]); err != nil { // touch: now most recently used
		t.Fatal(err)
	}
	clk.Add(2 * time.Minute)
	if _, _, err := s.Store().CreateSession(ctx, u.ID, ""); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = s.Store().db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE user_id = ?`, u.ID).Scan(&n)
	if n != MaxSessionsPerUser {
		t.Fatalf("sessions = %d, want %d", n, MaxSessionsPerUser)
	}
	if _, _, err := s.Store().Check(ctx, toks[1]); err == nil {
		t.Fatal("least recently used session survived")
	}
	for _, i := range []int{0, 2, 9} {
		if _, _, err := s.Store().Check(ctx, toks[i]); err != nil {
			t.Fatalf("session %d evicted: %v", i, err)
		}
	}
	if _, _, err := s.Store().Check(ctx, bobTok); err != nil {
		t.Fatal("another user's session evicted")
	}
}

func TestWatchCapAndSignals(t *testing.T) {
	s, _ := newSvc(t, false)
	u := mustUser(t, s, "alice", RoleViewer, false)
	ctx := context.Background()
	tok, sess, _ := s.Store().CreateSession(ctx, u.ID, "")
	var stops []func()
	var chans []<-chan struct{}
	for i := 0; i < MaxStreamsPerUser; i++ {
		ch, stop, err := s.WatchSession(sess)
		if err != nil {
			t.Fatal(err)
		}
		chans, stops = append(chans, ch), append(stops, stop)
	}
	if _, _, err := s.WatchSession(sess); err != ErrTooManyStreams {
		t.Fatalf("6th stream: %v", err)
	}
	stops[0]()
	ch, stop, err := s.WatchSession(sess)
	if err != nil {
		t.Fatalf("slot not freed: %v", err)
	}
	defer stop()
	chans = append(chans[1:], ch)
	select {
	case <-chans[0]:
		t.Fatal("signalled early")
	default:
	}
	if err := s.Store().DeleteSession(ctx, tok); err != nil {
		t.Fatal(err)
	}
	for i, c := range chans {
		select {
		case <-c:
		case <-time.After(time.Second):
			t.Fatalf("stream %d not signalled", i)
		}
	}
	// disabling and password change signal too
	for name, end := range map[string]func(){
		"disable":         func() { _ = s.Store().SetDisabled(ctx, u.ID, true) },
		"password change": func() { _ = s.Store().SetPassword(ctx, u.ID, "alice", "another long password", false) },
	} {
		_ = s.Store().SetDisabled(ctx, u.ID, false)
		_, sess, _ := s.Store().CreateSession(ctx, u.ID, "")
		ch, stop, _ := s.WatchSession(sess)
		end()
		select {
		case <-ch:
		case <-time.After(time.Second):
			t.Fatalf("%s did not end the stream", name)
		}
		stop()
	}
}
