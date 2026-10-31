package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/t0mer/cic/internal/audit"
	"github.com/t0mer/cic/internal/auth"
	"github.com/t0mer/cic/internal/commands"
	"github.com/t0mer/cic/internal/events"
)

type call struct {
	method, user, key string
	args              []any
}

type fakeCommander struct {
	mu    sync.Mutex
	calls []call
	err   error
}

func (f *fakeCommander) rec(m, user, key string, args ...any) (commands.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call{m, user, key, args})
	if f.err != nil {
		return commands.Result{}, f.err
	}
	act := map[string]string{"Ping": "ping", "StopPing": "ping_stop", "Notify": "notify", "SendSMS": "sendsms", "GetStatus": "getstatus", "GetLocation": "getlocation"}[m]
	return commands.Result{ID: "id-" + m, Action: act, DeviceKey: key, SentAt: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)}, nil
}
func (f *fakeCommander) Ping(_ context.Context, u, k string, s int) (commands.Result, error) {
	return f.rec("Ping", u, k, s)
}
func (f *fakeCommander) StopPing(_ context.Context, u, k string) (commands.Result, error) {
	return f.rec("StopPing", u, k)
}
func (f *fakeCommander) Notify(_ context.Context, u, k, t, x string) (commands.Result, error) {
	return f.rec("Notify", u, k, t, x)
}
func (f *fakeCommander) SendSMS(_ context.Context, u, k, p, x string) (commands.Result, error) {
	return f.rec("SendSMS", u, k, p, x)
}
func (f *fakeCommander) GetStatus(_ context.Context, u, k string) (commands.Result, error) {
	return f.rec("GetStatus", u, k)
}
func (f *fakeCommander) GetLocation(_ context.Context, u, k string) (commands.Result, error) {
	return f.rec("GetLocation", u, k)
}
func (f *fakeCommander) n() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.calls) }

type cmdEnv struct {
	h                  http.Handler
	fc                 *fakeCommander
	aud                *audit.Store
	adminCk, viewerCk  *http.Cookie
	adminCSRF, viewCSR string
	adminID            int64
	hub                *events.Hub
}

func newCmdEnv(t *testing.T) *cmdEnv { return newCmdEnvWith(t, nil) }

// newCmdEnvWith lets a test adjust the dependencies (audit wrapper, logger) before the
// router is built.
func newCmdEnvWith(t *testing.T, mut func(d *Deps, e *cmdEnv)) *cmdEnv {
	t.Helper()
	d, _, _ := newDeps(t)
	e := &cmdEnv{fc: &fakeCommander{}, aud: audit.New(testDB, nil), hub: d.Hub}
	d.Commands, d.Audit = e.fc, e.aud
	if mut != nil {
		mut(&d, e)
	}
	mk := func(name string, role auth.Role) (*http.Cookie, string, int64) {
		u, err := d.Auth.Store().CreateUser(context.Background(), name, "test-password-123", role, false)
		if err != nil {
			t.Fatal(err)
		}
		tok, sess, err := d.Auth.Store().CreateSession(context.Background(), u.ID, "go-test")
		if err != nil {
			t.Fatal(err)
		}
		return &http.Cookie{Name: d.Auth.CookieName(), Value: tok}, sess.CSRFToken, u.ID
	}
	e.adminCk, e.adminCSRF, e.adminID = mk("cmdadmin", auth.RoleAdmin)
	e.viewerCk, e.viewCSR, _ = mk("cmdviewer", auth.RoleViewer)
	e.h = router(d)
	return e
}

// send performs a request as who ("", "admin", "viewer"); csrf=false omits the token.
func (e *cmdEnv) send(method, path, body, who string, csrf bool) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Host = "127.0.0.1:8787"
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	r.Header.Set("Origin", "http://127.0.0.1:8787")
	switch who {
	case "admin":
		r.AddCookie(e.adminCk)
		if csrf {
			r.Header.Set(auth.CSRFHeader, e.adminCSRF)
		}
	case "viewer":
		r.AddCookie(e.viewerCk)
		if csrf {
			r.Header.Set(auth.CSRFHeader, e.viewCSR)
		}
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	return rec
}

var cmdRoutes = []struct{ name, path, body, action string }{
	{"ping", "/api/devices/dradis/a/ping", `{"seconds":20}`, "ping"},
	{"ping default", "/api/devices/dradis/a/ping", ``, "ping"},
	{"stop", "/api/devices/dradis/a/ping/stop", ``, "ping_stop"},
	{"notify", "/api/devices/dradis/a/notify", `{"title":"t","text":"hello"}`, "notify"},
	{"sms", "/api/devices/dradis/a/sms", `{"phone":"+972501234567","text":"hi there"}`, "sms"},
	{"status", "/api/devices/dradis/a/refresh-status", ``, "getstatus"},
	{"location", "/api/devices/dradis/a/refresh-location", ``, "getlocation"},
}

func TestCommandRBACMatrix(t *testing.T) {
	for _, rt := range cmdRoutes {
		t.Run(rt.name, func(t *testing.T) {
			e := newCmdEnv(t)
			if rec := e.send("POST", rt.path, rt.body, "", false); rec.Code != 401 {
				t.Errorf("anonymous: %d", rec.Code)
			}
			if rec := e.send("POST", rt.path, rt.body, "viewer", true); rec.Code != 403 {
				t.Errorf("viewer: %d", rec.Code)
			}
			if e.fc.n() != 0 {
				t.Fatal("commander reached by anonymous/viewer")
			}
			rec := e.send("POST", rt.path, rt.body, "admin", true)
			if rec.Code != 202 {
				t.Fatalf("admin: %d %s", rec.Code, rec.Body)
			}
			var got commandAccepted
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Action != rt.action || got.Device != "dradis/a" || got.ID == "" || got.SentAt != "2026-05-01T12:00:00Z" {
				t.Errorf("body %+v", got)
			}
			if c := e.fc.calls[0]; c.user != strconv.FormatInt(e.adminID, 10) || c.key != "dradis/a" {
				t.Errorf("call %+v: user must be the numeric user id", c)
			}
		})
	}
}

func TestActivityRBAC(t *testing.T) {
	e := newCmdEnv(t)
	for who, want := range map[string]int{"": 401, "viewer": 403, "admin": 200} {
		if rec := e.send("GET", "/api/activity", "", who, false); rec.Code != want {
			t.Errorf("%q: %d want %d", who, rec.Code, want)
		}
	}
}

func TestCommandRequiresCSRF(t *testing.T) {
	for _, rt := range cmdRoutes {
		e := newCmdEnv(t)
		if rec := e.send("POST", rt.path, rt.body, "admin", false); rec.Code != 403 {
			t.Errorf("%s without csrf: %d", rt.name, rec.Code)
		}
		if e.fc.n() != 0 {
			t.Errorf("%s: commander reached without csrf", rt.name)
		}
	}
	// A wrong token is rejected too.
	e := newCmdEnv(t)
	r := httptest.NewRequest("POST", "/api/devices/dradis/a/ping/stop", nil)
	r.Host = "127.0.0.1:8787"
	r.Header.Set("Origin", "http://127.0.0.1:8787")
	r.Header.Set(auth.CSRFHeader, "nope")
	r.AddCookie(e.adminCk)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	if rec.Code != 403 || e.fc.n() != 0 {
		t.Errorf("bad token: %d", rec.Code)
	}
}

func TestCommandValidation(t *testing.T) {
	big := `{"text":"` + strings.Repeat("x", 17<<10) + `"}`
	tests := []struct {
		name, path, body, ct string
		want                 int
	}{
		{"unknown field ping", "/api/devices/dradis/a/ping", `{"seconds":5,"x":1}`, "application/json", 400},
		{"unknown field sms", "/api/devices/dradis/a/sms", `{"phone":"+972501234567","text":"a","cc":"b"}`, "application/json", 400},
		{"trailing value", "/api/devices/dradis/a/notify", `{"text":"a"} {"text":"b"}`, "application/json", 400},
		{"trailing garbage", "/api/devices/dradis/a/notify", `{"text":"a"}x`, "application/json", 400},
		{"not an object", "/api/devices/dradis/a/notify", `["a"]`, "application/json", 400},
		{"float seconds", "/api/devices/dradis/a/ping", `{"seconds":1.5}`, "application/json", 400},
		{"string seconds", "/api/devices/dradis/a/ping", `{"seconds":"5"}`, "application/json", 400},
		{"empty sms body", "/api/devices/dradis/a/sms", ``, "application/json", 400},
		{"empty notify body", "/api/devices/dradis/a/notify", ``, "", 415},
		{"wrong content type", "/api/devices/dradis/a/notify", `{"text":"a"}`, "text/plain", 415},
		{"missing content type", "/api/devices/dradis/a/notify", `{"text":"a"}`, "", 415},
		{"too big", "/api/devices/dradis/a/notify", big, "application/json", 413},
		{"slash in device", "/api/devices/dradis/a%2fb/ping/stop", ``, "", 400},
		{"slash in prefix", "/api/devices/dr%2fadis/a/ping/stop", ``, "", 400},
		{"plus in device", "/api/devices/dradis/a+b/ping/stop", ``, "", 400},
		{"hash in device", "/api/devices/dradis/a%23/ping/stop", ``, "", 400},
		{"wildcard", "/api/devices/dradis/%23/refresh-status", ``, "", 400},
		{"percent", "/api/devices/dradis/a%252f/refresh-status", ``, "", 400},
		{"dots", "/api/devices/dradis/%2e%2e/refresh-status", ``, "", 400},
		{"space", "/api/devices/dradis/a%20b/refresh-status", ``, "", 400},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newCmdEnv(t)
			r := httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body))
			r.Host = "127.0.0.1:8787"
			r.Header.Set("Origin", "http://127.0.0.1:8787")
			r.Header.Set(auth.CSRFHeader, e.adminCSRF)
			if tc.ct != "" {
				r.Header.Set("Content-Type", tc.ct)
			}
			r.AddCookie(e.adminCk)
			rec := httptest.NewRecorder()
			e.h.ServeHTTP(rec, r)
			if rec.Code != tc.want {
				t.Fatalf("code %d want %d: %s", rec.Code, tc.want, rec.Body)
			}
			var eb commandError
			if err := json.Unmarshal(rec.Body.Bytes(), &eb); err != nil || eb.Code != "invalid" || eb.Error == "" {
				t.Errorf("error body %q (%v)", rec.Body, err)
			}
			if e.fc.n() != 0 {
				t.Errorf("commander reached: %+v", e.fc.calls)
			}
		})
	}
}

func TestCommandErrorMapping(t *testing.T) {
	tests := []struct {
		err        error
		status     int
		code       string
		retryAfter bool
	}{
		{fmt.Errorf("%w: seconds must be between 1 and 600", commands.ErrInvalid), 400, "invalid", false},
		{commands.ErrUnknownDevice, 404, "unknown_device", false},
		{commands.ErrRateLimited, 429, "rate_limited", true},
		{commands.ErrSMSCap, 429, "sms_cap", true},
		{commands.ErrRecipientLimited, 429, "recipient_limited", true},
		{commands.ErrSMSDisabled, 403, "sms_disabled", false},
		{commands.ErrNotConnected, 503, "not_connected", false},
		{fmt.Errorf("%w: looking up device: boom", commands.ErrUnavailable), 503, "unavailable", false},
		{fmt.Errorf("%w: publishing ping: %w", commands.ErrOutcomeUnknown, context.DeadlineExceeded), 504, "outcome_unknown", false},
		{errors.New("surprise"), 503, "unavailable", false},
	}
	for _, tc := range tests {
		t.Run(tc.code, func(t *testing.T) {
			e := newCmdEnv(t)
			e.fc.err = tc.err
			rec := e.send("POST", "/api/devices/dradis/a/refresh-status", "", "admin", true)
			if rec.Code != tc.status {
				t.Fatalf("status %d want %d: %s", rec.Code, tc.status, rec.Body)
			}
			var eb commandError
			_ = json.Unmarshal(rec.Body.Bytes(), &eb)
			if eb.Code != tc.code || eb.Error == "" {
				t.Errorf("body %s", rec.Body)
			}
			if ra := rec.Header().Get("Retry-After"); (ra != "") != tc.retryAfter {
				t.Errorf("Retry-After %q", ra)
			} else if n, err := strconv.Atoi(ra); ra != "" && (err != nil || n < 1) {
				t.Errorf("Retry-After %q", ra)
			}
			if strings.Contains(rec.Body.String(), "boom") || strings.Contains(rec.Body.String(), "surprise") {
				t.Errorf("internal detail leaked: %s", rec.Body)
			}
			if tc.code == "outcome_unknown" && (!strings.Contains(rec.Body.String(), "MAY still be delivered") || !strings.Contains(rec.Body.String(), "do not retry blindly")) {
				t.Errorf("outcome_unknown message: %s", rec.Body)
			}
			page, _ := e.aud.List(context.Background(), 10, 0)
			wantStatus := "rejected:" + tc.code
			if tc.code == "outcome_unknown" {
				wantStatus = "unknown"
			}
			if len(page.Items) != 1 || page.Items[0].Status != wantStatus {
				t.Errorf("audit %+v", page.Items)
			}
		})
	}
}

func TestCancelledRequestWritesNothing(t *testing.T) {
	for _, err := range []error{
		fmt.Errorf("ping_stop not attempted: %w", context.Canceled),
		fmt.Errorf("%w: publishing ping: %w", commands.ErrOutcomeUnknown, context.Canceled),
	} {
		fc := &fakeCommander{err: err}
		s := &server{Deps: Deps{Commands: fc}}
		h := s.command(cmdSpec{"ping_stop", simple("", func(c Commander, ctx context.Context, user, key string) (commands.Result, error) {
			return c.StopPing(ctx, user, key)
		})})
		ctx, cancel := context.WithCancel(auth.WithUser(context.Background(), auth.User{ID: 1, Username: "x"}, auth.Session{}))
		cancel()
		r := httptest.NewRequest("POST", "/x", nil).WithContext(ctx)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("prefix", "dradis")
		rctx.URLParams.Add("device", "a")
		r = r.WithContext(context.WithValue(ctx, chi.RouteCtxKey, rctx))
		rec := httptest.NewRecorder()
		h(rec, r)
		if fc.n() != 1 || rec.Body.Len() != 0 || rec.Header().Get("Content-Type") != "" {
			t.Errorf("%v: calls=%d wrote %q", err, fc.n(), rec.Body)
		}
	}
}

func TestAuditRowsAndPrivacy(t *testing.T) {
	e := newCmdEnv(t)
	phone, text := "+972501234567", "Secret message body that is far longer than twenty chars"
	reqs := []struct{ path, body string }{
		{"/api/devices/dradis/a/sms", fmt.Sprintf(`{"phone":%q,"text":%q}`, phone, text)},
		{"/api/devices/dradis/a/notify", fmt.Sprintf(`{"title":"T","text":%q}`, text)},
		{"/api/devices/dradis/a/ping", `{"seconds":30}`},
		{"/api/devices/dradis/a/ping", ``},
		{"/api/devices/dradis/a/ping/stop", ``},
		{"/api/devices/dradis/a/refresh-location", ``},
	}
	for _, r := range reqs {
		if rec := e.send("POST", r.path, r.body, "admin", true); rec.Code != 202 {
			t.Fatalf("%s: %d %s", r.path, rec.Code, rec.Body)
		}
	}
	e.fc.err = fmt.Errorf("%w: phone must be ...", commands.ErrInvalid)
	e.send("POST", "/api/devices/dradis/a/sms", fmt.Sprintf(`{"phone":%q,"text":%q}`, phone, text), "admin", true)

	page, err := e.aud.List(context.Background(), 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 7 || page.NextBefore != nil {
		t.Fatalf("items %d next %v", len(page.Items), page.NextBefore)
	}
	// newest first
	want := []struct{ action, status, detail string }{
		{"sms", "rejected:invalid", `phone=+*********567 len=56 "Secret message body "`},
		{"getlocation", "sent", ""},
		{"ping_stop", "sent", ""},
		{"ping", "sent", "seconds=default"},
		{"ping", "sent", "seconds=30"},
		{"notify", "sent", `len=56 "Secret message body "`},
		{"sms", "sent", `phone=+*********567 len=56 "Secret message body "`},
	}
	for i, w := range want {
		it := page.Items[i]
		if it.Action != w.action || it.Status != w.status || it.Detail != w.detail || it.User != "cmdadmin" || it.Device != "dradis/a" {
			t.Errorf("row %d = %+v, want %+v", i, it, w)
		}
	}
	// The detail may hold ONLY these forms (owner spec): a phone masked to its last 3
	// digits (or "invalid"), the text length, at most the first 20 characters of the text.
	allowed := map[string]*regexp.Regexp{
		"sms":         regexp.MustCompile(`^phone=(\+\*+[0-9]{3}|invalid) len=[0-9]+ "[^"]{0,20}"$`),
		"notify":      regexp.MustCompile(`^len=[0-9]+ "[^"]{0,20}"$`),
		"ping":        regexp.MustCompile(`^seconds=(default|[0-9]+)$`),
		"ping_stop":   regexp.MustCompile(`^$`),
		"getlocation": regexp.MustCompile(`^$`),
	}
	for _, it := range page.Items {
		if !allowed[it.Action].MatchString(it.Detail) {
			t.Errorf("detail %q of %s row is not in the allowed form", it.Detail, it.Action)
		}
		if strings.Contains(it.Detail, text[:21]) || strings.Contains(it.Detail, phone[4:]) {
			t.Errorf("detail %q holds more than the 20-char preview or an unmasked phone", it.Detail)
		}
	}
	// Scan every column of every row: no full phone, no text beyond the 20-char preview.
	rows, err := testDB.Query(`SELECT user||'|'||device||'|'||action||'|'||status||'|'||detail||'|'||command_id FROM audit_log`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		if strings.Contains(s, phone) || strings.Contains(s, "501234567") || strings.Contains(s, "twenty chars") || strings.Contains(s, text[:25]) {
			t.Errorf("sensitive data in audit row %q", s)
		}
	}
}

func TestActivityPagination(t *testing.T) {
	e := newCmdEnv(t)
	for i := 0; i < 7; i++ {
		if err := e.aud.Log(context.Background(), audit.Entry{User: "u", Device: "dradis/a", Action: "ping", Status: "sent", At: time.Unix(int64(1000+i), 0)}); err != nil {
			t.Fatal(err)
		}
	}
	get := func(q string) (int, struct {
		Items      []audit.Item `json:"items"`
		NextBefore *int64       `json:"next_before"`
	}) {
		rec := e.send("GET", "/api/activity"+q, "", "admin", false)
		var out struct {
			Items      []audit.Item `json:"items"`
			NextBefore *int64       `json:"next_before"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	var seen []int64
	q := "?limit=3"
	for pages := 0; pages < 5; pages++ {
		code, out := get(q)
		if code != 200 {
			t.Fatalf("code %d", code)
		}
		for _, it := range out.Items {
			seen = append(seen, it.ID)
		}
		if out.NextBefore == nil {
			break
		}
		if len(out.Items) != 3 {
			t.Fatalf("full page expected, got %d", len(out.Items))
		}
		q = fmt.Sprintf("?limit=3&before=%d", *out.NextBefore)
	}
	if len(seen) != 7 {
		t.Fatalf("seen %v", seen)
	}
	for i := 1; i < len(seen); i++ {
		if seen[i] >= seen[i-1] {
			t.Fatalf("not newest first: %v", seen)
		}
	}
	// Exactly one full page: no next_before.
	if _, out := get("?limit=7"); out.NextBefore != nil || len(out.Items) != 7 {
		t.Errorf("limit=7: next=%v n=%d", out.NextBefore, len(out.Items))
	}
	if _, out := get(""); len(out.Items) != 7 {
		t.Errorf("default limit: %d", len(out.Items))
	}
	for _, bad := range []string{"?limit=0", "?limit=201", "?limit=x", "?before=0", "?before=abc", "?before=-3"} {
		if code, _ := get(bad); code != 400 {
			t.Errorf("%s: %d", bad, code)
		}
	}
	if code, _ := get("?limit=200"); code != 200 {
		t.Errorf("limit=200: %d", code)
	}
	// An empty log is an empty array, not null.
	e2 := newCmdEnv(t)
	rec := e2.send("GET", "/api/activity", "", "admin", false)
	if !strings.Contains(rec.Body.String(), `"items":[]`) || !strings.Contains(rec.Body.String(), `"next_before":null`) {
		t.Errorf("empty: %s", rec.Body)
	}
}

func TestNoCommanderAnswersNotConnected(t *testing.T) {
	e := newCmdEnv(t)
	// A server built without a commander (no broker configured) still has the routes.
	d, _, _ := newDeps(t)
	d.Audit = audit.New(testDB, nil)
	e.h = router(d)
	sess, _, err := d.Auth.Store().Check(context.Background(), testCookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	e.adminCk, e.adminCSRF = testCookie, sess.CSRFToken
	r := e.send("POST", "/api/devices/dradis/a/refresh-status", "", "admin", true)
	if r.Code != 503 || !strings.Contains(r.Body.String(), `"code":"not_connected"`) {
		t.Fatalf("%d %s", r.Code, r.Body)
	}
}
