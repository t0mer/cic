package auth

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// Session cookie names. Over HTTPS (the default) the cookie carries the __Host- prefix, which
// makes browsers refuse any copy set by a sibling subdomain or without Secure/Path=/.
// The plain name is used only with --insecure-cookies (HTTP development); the two are never
// accepted together.
const (
	CookieNameSecure   = "__Host-cic_session"
	CookieNameInsecure = "cic_session"
)

// CSRFHeader carries the per-session CSRF token on state-changing requests.
const CSRFHeader = "X-CSRF-Token"

// Config configures a Service.
type Config struct {
	DB              *sql.DB
	Log             *slog.Logger
	InsecureCookies bool         // drop the Secure cookie attribute (plain-HTTP development only)
	TrustedProxies  []*net.IPNet // peers allowed to set X-Forwarded-For / X-Forwarded-Proto
	BaseURL         string       // public URL; its scheme is the expected scheme of Origin headers
	Now             func() time.Time
	SecretKey       []byte // 32-byte key for TOTP secrets; nil disables two-factor setup
}

// Service ties the store, rate limiter and HTTP handlers together.
type Service struct {
	store    *Store
	limiter  *Limiter
	log      *slog.Logger
	insecure bool
	trusted  []*net.IPNet
	httpsURL bool // base_url is https
	now      func() time.Time
	hashSem  chan struct{} // bounds concurrent argon2 work (64 MiB each); the shared hashPool
	hashWait time.Duration // longest a request queues for a hash slot before 503
	mfa      *mfaStore     // pending second-factor challenges
	audit    func(ctx context.Context, actor, action, detail string) error
}

// NewService builds a Service.
func NewService(c Config) *Service {
	now := c.Now
	if now == nil {
		now = time.Now
	}
	log := c.Log
	if log == nil {
		log = slog.Default()
	}
	st := NewStore(c.DB, now)
	st.SetSecretKey(c.SecretKey)
	return &Service{store: st, limiter: NewLimiter(now), log: log, mfa: newMFAStore(now),
		insecure: c.InsecureCookies, trusted: c.TrustedProxies, now: now, hashSem: hashPool,
		hashWait: hashQueueWait,
		httpsURL: strings.HasPrefix(strings.ToLower(strings.TrimSpace(c.BaseURL)), "https://")}
}

// Store returns the underlying user/session store.
func (s *Service) Store() *Store { return s.store }

// CookieName is the session cookie name in effect.
func (s *Service) CookieName() string {
	if s.insecure {
		return CookieNameInsecure
	}
	return CookieNameSecure
}

// WatchSession registers an open event stream for sess. The returned channel closes as soon
// as the session ends (logout, password change, disabling, eviction); stop must be called
// when the stream finishes. It fails with ErrTooManyStreams past the per-user cap.
func (s *Service) WatchSession(sess Session) (ended <-chan struct{}, stop func(), err error) {
	return s.store.Watch(sess.UserID, sess.IDHash)
}

// SetAuditor registers the function that records security events of this package
// (two-factor changes and backup-code logins) in the audit log. Errors are logged, never
// returned to the caller. Call it before serving.
func (s *Service) SetAuditor(f func(ctx context.Context, actor, action, detail string) error) {
	s.audit = f
}

func (s *Service) auditEvent(ctx context.Context, actor, action, detail string) {
	if s.audit == nil {
		return
	}
	if err := s.audit(context.WithoutCancel(ctx), actor, action, detail); err != nil {
		s.log.Error("audit write failed", "action", action, "err", err)
	}
}

// Limiter returns the login rate limiter.
func (s *Service) Limiter() *Limiter { return s.limiter }

// RunJanitor purges expired sessions and stale limiter entries every interval until ctx ends.
func (s *Service) RunJanitor(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := s.store.PurgeExpired(ctx); err != nil && ctx.Err() == nil {
				s.log.Error("purging expired sessions", "err", err)
			}
			s.limiter.Prune()
			s.mfa.prune()
		}
	}
}

// SessionActive reports whether sess is still valid, without extending it (used by SSE).
func (s *Service) SessionActive(ctx context.Context, sess Session) bool {
	var one int
	err := s.store.db.QueryRowContext(ctx, `SELECT 1 FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.id_hash = ? AND u.disabled = 0 AND s.last_seen >= ? AND s.created_at >= ?`, sess.IDHash,
		s.now().Add(-s.store.IdleTimeout).Unix(), s.now().Add(-s.store.AbsoluteLifetime).Unix()).Scan(&one)
	return err == nil
}

// ---- context ----

type ctxKey int

const (
	userKey ctxKey = iota
	sessKey
)

// WithUser returns ctx carrying the authenticated user and session.
func WithUser(ctx context.Context, u User, sess Session) context.Context {
	return context.WithValue(context.WithValue(ctx, userKey, u), sessKey, sess)
}

// UserFrom returns the authenticated user set by RequireAuth.
func UserFrom(ctx context.Context) (User, bool) {
	u, ok := ctx.Value(userKey).(User)
	return u, ok
}

// SessionFrom returns the session set by RequireAuth.
func SessionFrom(ctx context.Context) (Session, bool) {
	s, ok := ctx.Value(sessKey).(Session)
	return s, ok
}

// ---- JSON helpers ----

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// ---- cookies ----

func (s *Service) setCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name: s.CookieName(), Value: token, Path: "/",
		MaxAge:   int(s.store.AbsoluteLifetime / time.Second),
		HttpOnly: true, Secure: !s.insecure, SameSite: http.SameSiteStrictMode,
	})
}

func (s *Service) clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: s.CookieName(), Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: !s.insecure, SameSite: http.SameSiteStrictMode,
	})
}

// ---- request checks ----

// sameOrigin vets a state-changing request. It needs positive evidence of a same-origin
// caller: an Origin header, or Sec-Fetch-Site of same-origin/none; a request with neither is
// refused. Sec-Fetch-Site must not say cross-site/same-site, and an Origin must match both
// the Host and the expected scheme (https behind TLS, a https base_url or a trusted proxy
// reporting https), so an http:// page cannot act on an https deployment. "null" fails.
func (s *Service) sameOrigin(r *http.Request) bool {
	sfs, origin := r.Header.Get("Sec-Fetch-Site"), r.Header.Get("Origin")
	switch sfs {
	case "", "same-origin", "none":
	default:
		return false
	}
	if sfs == "" && origin == "" {
		return false
	}
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || !strings.EqualFold(u.Host, r.Host) {
		return false
	}
	scheme := "http"
	if r.TLS != nil || s.httpsURL || ViaTrustedProxyHTTPS(r, s.trusted) {
		scheme = "https"
	}
	return u.Scheme == scheme
}

func safeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// ---- middleware ----

// RequireAuth authenticates the session cookie, enforces CSRF on state-changing methods and
// blocks users who must change their password (403). On success the user is in the context.
func (s *Service) RequireAuth(next http.Handler) http.Handler { return s.guard(next, false) }

// RequireAuthAllowPasswordChange is RequireAuth without the must-change-password block.
func (s *Service) RequireAuthAllowPasswordChange(next http.Handler) http.Handler {
	return s.guard(next, true)
}

func (s *Service) guard(next http.Handler, allowMustChange bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(s.CookieName())
		if err != nil {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		sess, u, err := s.store.Lookup(r.Context(), c.Value)
		if err != nil {
			if errors.Is(err, ErrNoSession) {
				s.clearCookie(w)
				writeError(w, http.StatusUnauthorized, "authentication required")
				return
			}
			s.log.Error("session lookup", "err", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if !safeMethod(r.Method) {
			if !s.sameOrigin(r) {
				writeError(w, http.StatusForbidden, "cross-site request rejected")
				return
			}
			if subtle.ConstantTimeCompare([]byte(r.Header.Get(CSRFHeader)), []byte(sess.CSRFToken)) != 1 {
				writeError(w, http.StatusForbidden, "invalid csrf token")
				return
			}
		}
		if u.MustChangePassword && !allowMustChange {
			writeError(w, http.StatusForbidden, "password change required")
			return
		}
		next.ServeHTTP(w, r.WithContext(WithUser(r.Context(), u, sess)))
	})
}

// RequireRole allows only users holding role (admin also satisfies viewer). It must run
// after RequireAuth.
func RequireRole(role Role) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u, ok := UserFrom(r.Context())
			if !ok {
				writeError(w, http.StatusUnauthorized, "authentication required")
				return
			}
			if u.Role != role && u.Role != RoleAdmin {
				writeError(w, http.StatusForbidden, "forbidden")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ---- handlers ----

// Routes returns the /auth sub-router (mount it at /api/auth).
func (s *Service) Routes() http.Handler {
	r := chi.NewRouter()
	r.Post("/login", s.login)
	r.Post("/login/2fa", s.login2FA)
	r.Group(func(r chi.Router) {
		r.Use(s.RequireAuthAllowPasswordChange)
		r.Post("/logout", s.logout)
		r.Get("/me", s.me)
		r.Post("/change-password", s.changePassword)
	})
	r.Group(func(r chi.Router) {
		r.Use(s.RequireAuth)
		r.Get("/2fa", s.twoFAStatus)
		r.Post("/2fa/setup", s.twoFASetup)
		r.Post("/2fa/enable", s.twoFAEnable)
		r.Post("/2fa/disable", s.twoFADisable)
		r.Post("/2fa/backup-codes", s.twoFABackupCodes)
	})
	r.NotFound(func(w http.ResponseWriter, _ *http.Request) { writeError(w, http.StatusNotFound, "not found") })
	r.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	})
	return r
}

type userView struct {
	Username           string `json:"username"`
	Role               Role   `json:"role"`
	MustChangePassword bool   `json:"must_change_password"`
	TOTPEnabled        bool   `json:"totp_enabled"`
}

func viewOf(u User) userView {
	return userView{Username: u.Username, Role: u.Role, MustChangePassword: u.MustChangePassword, TOTPEnabled: u.TOTPEnabled}
}

type sessionView struct {
	User      userView `json:"user"`
	CSRFToken string   `json:"csrf_token"`
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	return true
}

// verify checks pw against hash on a bounded pool of argon2 slots. It returns ErrBusy when
// no slot frees up within hashWait and ctx.Err() when the request ends first; in both cases
// there is no verdict and the caller must not count a failure. newHash is non-empty when
// the stored hash matched but uses outdated parameters and should be replaced.
func (s *Service) verify(ctx context.Context, pw, hash string, real bool) (ok bool, newHash string, err error) {
	release, err := acquireSlot(ctx, s.hashSem, s.hashWait)
	if err != nil {
		return false, "", err
	}
	defer release()
	if !real {
		return VerifyDummy(pw), "", ctx.Err()
	}
	ok, stale := verifyPassword(pw, hash)
	if ok && stale {
		if h, herr := HashPassword(pw); herr == nil { // slot already held
			newHash = h
		}
	}
	return ok, newHash, ctx.Err()
}

// errIfBusy passes only ErrBusy through, for handlers that treat other errors themselves.
func errIfBusy(err error) error {
	if errors.Is(err, ErrBusy) {
		return err
	}
	return nil
}

func tooMany(w http.ResponseWriter, wait time.Duration) {
	secs := int((wait + time.Second - 1) / time.Second)
	w.Header().Set("Retry-After", strconv.Itoa(max(secs, 1)))
	writeError(w, http.StatusTooManyRequests, "too many attempts, try again later")
}

// verdictErr answers a verify error. It reports whether err was set, i.e. whether the
// request is finished: busy answers 503, a cancelled request gets no answer at all.
func verdictErr(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, ErrBusy):
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusServiceUnavailable, "busy")
	}
	return true
}

func (s *Service) login(w http.ResponseWriter, r *http.Request) {
	if !s.sameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross-site request rejected")
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decode(w, r, &req) {
		return
	}
	ipKey := ipPrefix + limiterIP(ClientIP(r, s.trusted))
	userKey := "user:" + strings.ToLower(req.Username)
	// Locked keys answer 429 before any password check, so a lockout never reveals
	// whether the password was right. The attempt is reserved before the (slow) hash so a
	// burst of parallel requests cannot all slip under the threshold.
	att, wait := s.limiter.Begin(ipKey, userKey)
	if att == nil {
		tooMany(w, wait)
		return
	}
	defer att.Cancel() // no-op once settled; refunds on every early return
	u, hash, err := s.store.UserByName(r.Context(), req.Username)
	if err != nil && !errors.Is(err, ErrUserMissing) {
		if r.Context().Err() != nil {
			return // client went away; nothing to answer
		}
		s.log.Error("login lookup", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	known := err == nil && !u.Disabled
	ok, newHash, err := s.verify(r.Context(), req.Password, hash, known)
	if verdictErr(w, err) {
		return
	}
	if !ok || !known {
		att.Fail()
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if newHash != "" {
		if err := s.store.replaceHash(r.Context(), u.ID, hash, newHash); err != nil {
			s.log.Warn("upgrading password hash", "err", err)
		}
	}
	if u.TOTPEnabled {
		s.challenge(w, att, u)
		return
	}
	// u was read before the (slow) password check, so two-factor may have been enabled in
	// the meantime. The insert itself re-checks the flag; a lost race turns into a challenge,
	// so the attempt is only settled afterwards (a challenge must not clear failure counters).
	switch s.startSession(w, r, u, true, -1) {
	case sessionNeedsChallenge:
		s.challenge(w, att, u)
		return
	case sessionFailed:
		return
	}
	// Only the account's own counter is cleared: the source address keeps its failures, so
	// logging in to one's own account cannot launder guesses against others.
	att.Success(userKey)
}

// challenge answers a correct password of a two-factor account: no session yet, only a
// short-lived token to present with the second factor. The attempt is refunded WITHOUT
// clearing any failure count (Cancel, not Success): the password failure counters only
// reset once the second factor passes, so a stream of correct-password / never-finishes
// logins cannot be used to launder failures, and the real guessing (the second factor) is
// limited on the same user:/ip: keys.
func (s *Service) challenge(w http.ResponseWriter, att *Attempt, u User) {
	att.Cancel()
	token, err := s.mfa.issue(u.ID, u.AuthGen)
	if errors.Is(err, errMFAFull) {
		w.Header().Set("Retry-After", "30")
		writeError(w, http.StatusServiceUnavailable, "too many sign-ins in progress, try again shortly")
		return
	}
	if err != nil {
		s.log.Error("creating mfa challenge", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"mfa_required": true, "mfa_token": token})
}

// sessionResult tells callers of startSession what happened to the request.
type sessionResult int

const (
	sessionIssued         sessionResult = iota // a session was created and the answer written
	sessionNeedsChallenge                      // two-factor was enabled meanwhile; nothing written, nothing changed
	sessionFailed                              // an error answer was written, nothing changed
)

// startSession atomically creates a fresh session and retires the request's old one (see
// Store.issueSession), records the login and answers with the session view. With only2FAOff
// it creates the session only while the account still has no second factor and reports
// sessionNeedsChallenge, having written nothing and left every session alone, when
// two-factor was enabled in the meantime.
func (s *Service) startSession(w http.ResponseWriter, r *http.Request, u User, only2FAOff bool, gen int64) sessionResult {
	var old string
	if c, err := r.Cookie(s.CookieName()); err == nil {
		old = c.Value // never reuse a pre-login session
	}
	token, sess, err := s.store.issueSession(r.Context(), u.ID, r.UserAgent(), only2FAOff, gen, old)
	if errors.Is(err, ErrSecondFactorRequired) {
		return sessionNeedsChallenge
	}
	if errors.Is(err, errAuthChanged) {
		writeError(w, http.StatusUnauthorized, "login expired, sign in again")
		return sessionFailed
	}
	if err != nil {
		s.log.Error("creating session", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return sessionFailed
	}
	if err := s.store.TouchLogin(r.Context(), u.ID); err != nil {
		s.log.Warn("recording login time", "err", err)
	}
	s.setCookie(w, token)
	writeJSON(w, http.StatusOK, sessionView{User: viewOf(u), CSRFToken: sess.CSRFToken})
	return sessionIssued
}

func (s *Service) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(s.CookieName()); err == nil {
		if err := s.store.DeleteSession(r.Context(), c.Value); err != nil {
			s.log.Error("deleting session", "err", err)
		}
	}
	s.clearCookie(w)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) me(w http.ResponseWriter, r *http.Request) {
	u, _ := UserFrom(r.Context())
	sess, _ := SessionFrom(r.Context())
	v := viewOf(u)
	writeJSON(w, http.StatusOK, struct {
		userView
		CSRFToken string `json:"csrf_token"`
	}{v, sess.CSRFToken})
}

func (s *Service) changePassword(w http.ResponseWriter, r *http.Request) {
	u, _ := UserFrom(r.Context())
	var req struct {
		Current string `json:"current_password"`
		New     string `json:"new_password"`
	}
	if !decode(w, r, &req) {
		return
	}
	// A separate key from login: failed logins by an attacker must not block a signed-in
	// user from changing the password, and typos here must not lock the account's login.
	key := "chpw:" + strings.ToLower(u.Username)
	att, wait := s.limiter.Begin(key)
	if att == nil {
		tooMany(w, wait)
		return
	}
	defer att.Cancel()
	hash, err := s.store.passwordHash(r.Context(), u.ID)
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	ok, _, err := s.verify(r.Context(), req.Current, hash, true)
	if verdictErr(w, err) {
		return
	}
	if !ok {
		att.Fail()
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	att.Success(key)
	if req.New == req.Current {
		writeError(w, http.StatusBadRequest, "new password must differ from the current one")
		return
	}
	if err := ValidatePassword(req.New, u.Username); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	c, _ := r.Cookie(s.CookieName())
	token, sess, err := s.store.ChangePassword(r.Context(), u.ID, u.Username, req.New, c.Value)
	if verdictErr(w, errIfBusy(err)) {
		return
	}
	if err != nil {
		s.log.Error("changing password", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	u.MustChangePassword = false
	s.setCookie(w, token)
	writeJSON(w, http.StatusOK, sessionView{User: viewOf(u), CSRFToken: sess.CSRFToken})
}
