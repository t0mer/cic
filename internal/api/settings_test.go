package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/t0mer/cic/internal/audit"
	"github.com/t0mer/cic/internal/auth"
	"github.com/t0mer/cic/internal/devices"
	"github.com/t0mer/cic/internal/events"
)

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuf) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

type setSess struct {
	ck   *http.Cookie
	csrf string
	id   int64
}

type setEnv struct {
	h    http.Handler
	d    Deps
	fs   *fakeStore
	logs *lockedBuf
	who  map[string]*setSess
}

func newSetEnv(t *testing.T) *setEnv { return newSetEnvWith(t, nil) }

// newSetEnvWith is newSetEnv with a hook to adjust the Deps before the feeds start.
func newSetEnvWith(t *testing.T, mut func(*Deps)) *setEnv {
	t.Helper()
	d, fs, _ := newDeps(t)
	if mut != nil {
		mut(&d)
	}
	e := &setEnv{fs: fs, logs: &lockedBuf{}, who: map[string]*setSess{}}
	d.Log = slog.New(slog.NewTextHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	d.Audit = audit.New(testDB, nil)
	e.d = d
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	StartFeeds(ctx, d)
	e.mk(t, "admin", auth.RoleAdmin)
	e.mk(t, "admin2", auth.RoleAdmin)
	e.mk(t, "viewer", auth.RoleViewer)
	e.h = router(d)
	return e
}

// mk creates a user (named name, session key name) with a session.
func (e *setEnv) mk(t *testing.T, name string, role auth.Role) *setSess {
	t.Helper()
	st := e.d.Auth.Store()
	u, err := st.CreateUser(context.Background(), name, "test-password-123", role, false)
	if err != nil {
		t.Fatal(err)
	}
	tok, sess, err := st.CreateSession(context.Background(), u.ID, "go-test")
	if err != nil {
		t.Fatal(err)
	}
	s := &setSess{&http.Cookie{Name: e.d.Auth.CookieName(), Value: tok}, sess.CSRFToken, u.ID}
	e.who[name] = s
	return s
}

func (e *setEnv) send(who, method, path, body string, csrf bool) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Host = "127.0.0.1:8787"
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	r.Header.Set("Origin", "http://127.0.0.1:8787")
	if s := e.who[who]; s != nil {
		r.AddCookie(s.ck)
		if csrf {
			r.Header.Set(auth.CSRFHeader, s.csrf)
		}
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	return rec
}

func (e *setEnv) ok(t *testing.T, who, method, path, body string, want int) map[string]any {
	t.Helper()
	rec := e.send(who, method, path, body, true)
	if rec.Code != want {
		t.Fatalf("%s %s: %d %s, want %d", method, path, rec.Code, rec.Body, want)
	}
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	return m
}

func (e *setEnv) userID(t *testing.T, name string) int64 {
	t.Helper()
	u, _, err := e.d.Auth.Store().UserByName(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	return u.ID
}

func auditText(t *testing.T) string {
	t.Helper()
	rows, err := testDB.Query(`SELECT user, device, action, status, detail FROM audit_log ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var u, d, a, s, det string
		if err := rows.Scan(&u, &d, &a, &s, &det); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "%s|%s|%s|%s|%s\n", u, d, a, s, det)
	}
	return b.String()
}

func TestSettingsRBACAndCSRF(t *testing.T) {
	routes := []struct{ method, path, body string }{
		{"PATCH", "/api/devices/dradis/a", `{"hidden":false}`},
		{"DELETE", "/api/devices/dradis/a", ``},
		{"GET", "/api/users", ``},
		{"POST", "/api/users", `{"username":"newbie","role":"viewer"}`},
		{"POST", "/api/users/%d/reset-password", ``},
		{"PATCH", "/api/users/%d", `{"role":"viewer"}`},
	}
	for _, rt := range routes {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			e := newSetEnv(t)
			e.fs.devs = append(e.fs.devs, devices.Device{Key: "dradis/a", Prefix: "dradis", Device: "a", FullName: "a"})
			path := rt.path
			if strings.Contains(path, "%d") {
				path = fmt.Sprintf(path, e.userID(t, "viewer"))
			}
			if rec := e.send("", rt.method, path, rt.body, false); rec.Code != 401 {
				t.Errorf("anonymous: %d", rec.Code)
			}
			if rec := e.send("viewer", rt.method, path, rt.body, true); rec.Code != 403 || !strings.Contains(rec.Body.String(), "forbidden") {
				t.Errorf("viewer: %d %s", rec.Code, rec.Body)
			}
			if rt.method != "GET" {
				if rec := e.send("admin", rt.method, path, rt.body, false); rec.Code != 403 {
					t.Errorf("admin without csrf: %d", rec.Code)
				}
			}
			rec := e.send("admin", rt.method, path, rt.body, true)
			if rec.Code >= 300 && rec.Code != 204 {
				t.Errorf("admin: %d %s", rec.Code, rec.Body)
			}
			if rec.Header().Get("Cache-Control") != "no-store" {
				t.Errorf("cache-control %q", rec.Header().Get("Cache-Control"))
			}
		})
	}
}

func TestPatchDeviceValidation(t *testing.T) {
	long := strings.Repeat("é", 81)
	tests := []struct {
		name, body string
		code       int
		wantName   string
	}{
		{"name ok", `{"full_name":"  Kitchen tablet  "}`, 200, "Kitchen tablet"},
		{"name 80 runes", `{"full_name":"` + strings.Repeat("é", 80) + `"}`, 200, strings.Repeat("é", 80)},
		{"name 81 runes", `{"full_name":"` + long + `"}`, 400, ""},
		{"name reset", `{"full_name":""}`, 200, "a"},
		{"name spaces reset", `{"full_name":"   "}`, 200, "a"},
		{"name hebrew", `{"full_name":"טאבלט מטבח"}`, 200, "טאבלט מטבח"},
		{"newline", `{"full_name":"a\nb"}`, 400, ""},
		{"tab", `{"full_name":"a\tb"}`, 400, ""},
		{"nul", `{"full_name":"a\u0000b"}`, 400, ""},
		{"c1 control", `{"full_name":"a\u0085b"}`, 400, ""},
		{"bidi override", `{"full_name":"a\u202eb"}`, 400, ""},
		{"zero width", `{"full_name":"a\u200bb"}`, 400, ""},
		{"bom", `{"full_name":"\ufeffab"}`, 400, ""},
		{"line separator", `{"full_name":"a\u2028b"}`, 400, ""},
		{"paragraph separator", `{"full_name":"a\u2029b"}`, 400, ""},
		{"hangul filler only", `{"full_name":"\u3164"}`, 400, ""},
		{"braille blank only", `{"full_name":" \u2800 "}`, 400, ""},
		{"hangul choseong filler", `{"full_name":"\u115f\u1160"}`, 400, ""},
		{"halfwidth hangul filler", `{"full_name":"\uffa0\u3164"}`, 400, ""},
		{"blank beside visible ok", `{"full_name":"A\u3164B"}`, 200, "A\u3164B"},
		{"internal spaces collapse", `{"full_name":"  a    b \u00a0 c  "}`, 200, "a b c"},
		{"color ok", `{"color":"#A1b2C3"}`, 200, ""},
		{"color reset", `{"color":""}`, 200, ""},
		{"color short", `{"color":"#abc"}`, 400, ""},
		{"color no hash", `{"color":"aabbcc"}`, 400, ""},
		{"color named", `{"color":"red"}`, 400, ""},
		{"color alpha", `{"color":"#aabbccdd"}`, 400, ""},
		{"color bad hex", `{"color":"#gggggg"}`, 400, ""},
		{"color spaces", `{"color":" #aabbcc"}`, 400, ""},
		{"hidden", `{"hidden":true}`, 200, ""},
		{"unknown field", `{"nope":1}`, 400, ""},
		{"wrong type", `{"hidden":"yes"}`, 400, ""},
		{"not json", `nope`, 400, ""},
		{"two objects", `{"hidden":true}{"hidden":false}`, 400, ""},
		{"empty body", ``, 415, ""},
		{"empty object", `{}`, 200, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newSetEnv(t)
			e.fs.devs = []devices.Device{{Key: "dradis/a", Prefix: "dradis", Device: "a", FullName: "Old"}}
			rec := e.send("admin", "PATCH", "/api/devices/dradis/a", tt.body, true)
			if rec.Code != tt.code {
				t.Fatalf("%d %s, want %d", rec.Code, rec.Body, tt.code)
			}
			if tt.code == 400 {
				var m map[string]string
				_ = json.Unmarshal(rec.Body.Bytes(), &m)
				if m["code"] != "invalid" || m["error"] == "" {
					t.Errorf("body %s", rec.Body)
				}
				if e.fs.devs[0].FullName != "Old" {
					t.Errorf("rejected request changed the device")
				}
			}
			if tt.wantName != "" {
				var d devices.Device
				_ = json.Unmarshal(rec.Body.Bytes(), &d)
				if d.FullName != tt.wantName {
					t.Errorf("full_name %q, want %q", d.FullName, tt.wantName)
				}
			}
		})
	}
}

func TestPatchDeviceContentTypeAndSize(t *testing.T) {
	e := newSetEnv(t)
	e.fs.devs = []devices.Device{{Key: "dradis/a", Prefix: "dradis", Device: "a"}}
	r := httptest.NewRequest("PATCH", "/api/devices/dradis/a", strings.NewReader(`{"hidden":true}`))
	r.Host = "127.0.0.1:8787"
	r.Header.Set("Origin", "http://127.0.0.1:8787")
	r.Header.Set("Content-Type", "text/plain")
	r.Header.Set(auth.CSRFHeader, e.who["admin"].csrf)
	r.AddCookie(e.who["admin"].ck)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	if rec.Code != 415 {
		t.Errorf("content-type: %d", rec.Code)
	}
	big := `{"full_name":"` + strings.Repeat("x", 17<<10) + `"}`
	if rec := e.send("admin", "PATCH", "/api/devices/dradis/a", big, true); rec.Code != 413 {
		t.Errorf("oversize: %d", rec.Code)
	}
}

func TestPatchDeviceUnknownAndBadPath(t *testing.T) {
	e := newSetEnv(t)
	rec := e.send("admin", "PATCH", "/api/devices/dradis/ghost", `{"hidden":true}`, true)
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), `"code":"unknown_device"`) {
		t.Errorf("unknown: %d %s", rec.Code, rec.Body)
	}
	if rec := e.send("admin", "PATCH", "/api/devices/dr%20x/a", `{"hidden":true}`, true); rec.Code != 400 {
		t.Errorf("bad segment: %d", rec.Code)
	}
	if rec := e.send("admin", "DELETE", "/api/devices/dradis/ghost", ``, true); rec.Code != 404 || !strings.Contains(rec.Body.String(), `"code":"unknown_device"`) {
		t.Errorf("delete unknown: %d %s", rec.Code, rec.Body)
	}
}

func TestPatchDevicePartialEventAndAudit(t *testing.T) {
	e := newSetEnv(t)
	e.fs.devs = []devices.Device{{Key: "dradis/a", Prefix: "dradis", Device: "a", FullName: "Old", Color: "#112233"}}
	srv := httptest.NewServer(e.h)
	t.Cleanup(srv.Close)
	adm := sseConnect(t, srv, e.who["admin"].ck)
	adm.until(t, "event: hello")

	m := e.ok(t, "admin", "PATCH", "/api/devices/dradis/a", `{"full_name":"New name"}`, 200)
	if m["full_name"] != "New name" || m["color"] != "#112233" || m["hidden"] != false {
		t.Errorf("partial update: %v", m)
	}
	adm.until(t, "event: device.updated")
	adm.sc.Scan()
	if data := adm.sc.Text(); !strings.Contains(data, `"full_name":"New name"`) || !strings.Contains(data, `"key":"dradis/a"`) {
		t.Errorf("event data %s", data)
	}
	// An identical request changes nothing: no audit row, no event.
	e.ok(t, "admin", "PATCH", "/api/devices/dradis/a", `{"full_name":"New name"}`, 200)
	e.ok(t, "admin", "PATCH", "/api/devices/dradis/a", `{"color":"#ABCDEF","hidden":true}`, 200)

	text := auditText(t)
	for _, want := range []string{"admin|dradis/a|device_update|ok|fields=full_name\n", "admin|dradis/a|device_update|ok|fields=color,hidden\n"} {
		if !strings.Contains(text, want) {
			t.Errorf("audit lacks %q in:\n%s", want, text)
		}
	}
	if strings.Count(text, "device_update") != 2 {
		t.Errorf("expected two audit rows:\n%s", text)
	}
	if strings.Contains(text, "New name") || strings.Contains(text, "ABCDEF") || strings.Contains(text, "Old") {
		t.Errorf("audit holds values:\n%s", text)
	}
}

func TestDeleteDeviceEventAndAudit(t *testing.T) {
	e := newSetEnv(t)
	e.fs.devs = []devices.Device{{Key: "dradis/a", Prefix: "dradis", Device: "a"}, {Key: "dradis/b", Prefix: "dradis", Device: "b"}}
	srv := httptest.NewServer(e.h)
	t.Cleanup(srv.Close)
	adm := sseConnect(t, srv, e.who["admin"].ck)
	adm.until(t, "event: hello")
	if rec := e.send("admin", "DELETE", "/api/devices/dradis/a", ``, true); rec.Code != 204 || rec.Body.Len() != 0 {
		t.Fatalf("delete: %d %q", rec.Code, rec.Body)
	}
	adm.until(t, "event: device.removed")
	adm.sc.Scan()
	if adm.sc.Text() != `data: {"key":"dradis/a"}` {
		t.Errorf("event data %q", adm.sc.Text())
	}
	if rec := e.send("admin", "GET", "/api/devices/dradis/a", ``, false); rec.Code != 404 {
		t.Errorf("deleted device still served: %d", rec.Code)
	}
	if rec := e.send("admin", "DELETE", "/api/devices/dradis/a", ``, true); rec.Code != 404 {
		t.Errorf("second delete: %d", rec.Code)
	}
	if !strings.Contains(auditText(t), "admin|dradis/a|device_delete|ok|\n") {
		t.Errorf("audit:\n%s", auditText(t))
	}
}

func TestHiddenDevicesNeverReachViewers(t *testing.T) {
	e := newSetEnv(t)
	e.fs.devs = []devices.Device{
		{Key: "dradis/a", Prefix: "dradis", Device: "a", FullName: "A"},
		{Key: "dradis/h", Prefix: "dradis", Device: "h", FullName: "H", Hidden: true},
	}
	keys := func(who, q string) string {
		rec := e.send(who, "GET", "/api/devices"+q, ``, false)
		var ds []devices.Device
		if err := json.Unmarshal(rec.Body.Bytes(), &ds); err != nil || rec.Code != 200 {
			t.Fatalf("%s%s: %d %s", who, q, rec.Code, rec.Body)
		}
		var ks []string
		for _, d := range ds {
			ks = append(ks, d.Key)
		}
		return strings.Join(ks, ",")
	}
	if got := keys("viewer", ""); got != "dradis/a" {
		t.Errorf("viewer list: %s", got)
	}
	if got := keys("viewer", "?include_hidden=1"); got != "dradis/a" {
		t.Errorf("viewer include_hidden: %s", got)
	}
	if got := keys("admin", ""); got != "dradis/a" {
		t.Errorf("admin list: %s", got)
	}
	if got := keys("admin", "?include_hidden=1"); got != "dradis/a,dradis/h" {
		t.Errorf("admin include_hidden: %s", got)
	}
	if rec := e.send("viewer", "GET", "/api/devices/dradis/h", ``, false); rec.Code != 404 {
		t.Errorf("viewer get hidden: %d", rec.Code)
	}
	if rec := e.send("admin", "GET", "/api/devices/dradis/h", ``, false); rec.Code != 200 {
		t.Errorf("admin get hidden: %d", rec.Code)
	}

	srv := httptest.NewServer(e.h)
	t.Cleanup(srv.Close)
	view, adm := sseConnect(t, srv, e.who["viewer"].ck), sseConnect(t, srv, e.who["admin"].ck)
	view.until(t, "event: hello")
	adm.until(t, "event: hello")
	// A hidden device changes, is deleted, and a visible one becomes hidden.
	e.fs.fire(e.fs.devs[1])
	e.ok(t, "admin", "PATCH", "/api/devices/dradis/h", `{"color":"#010203"}`, 200)
	e.ok(t, "admin", "PATCH", "/api/devices/dradis/a", `{"hidden":true}`, 200)
	e.ok(t, "admin", "DELETE", "/api/devices/dradis/h", ``, 204)
	e.d.Hub.Publish(events.TypeBrokerState, brokerView{}) // canary

	var seen []string
	done := make(chan struct{})
	go func() {
		defer close(done)
		for view.sc.Scan() {
			seen = append(seen, view.sc.Text())
			if view.sc.Text() == "event: broker.state" {
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("viewer never saw the canary")
	}
	all := strings.Join(seen, "\n")
	if strings.Contains(all, "dradis/h") || strings.Contains(all, "#010203") || strings.Contains(all, "device.updated") {
		t.Errorf("viewer stream leaked a hidden device:\n%s", all)
	}
	if !strings.Contains(all, "event: device.removed\ndata: {\"key\":\"dradis/a\"}") {
		t.Errorf("viewer was not told dradis/a is gone:\n%s", all)
	}
	// The admin sees everything.
	adm.until(t, "event: device.updated")
	adm.until(t, "event: device.updated")
	adm.until(t, "event: device.updated")
	adm.until(t, "event: device.removed")
}

func TestListAndCreateUsers(t *testing.T) {
	e := newSetEnv(t)
	rec := e.send("admin", "GET", "/api/users", ``, false)
	var us []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &us); err != nil || rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var names []string
	for _, u := range us {
		names = append(names, u["username"].(string))
		for _, k := range []string{"id", "role", "disabled", "must_change_password", "created_at", "last_login"} {
			if _, ok := u[k]; !ok {
				t.Errorf("user lacks %q: %v", k, u)
			}
		}
		if _, ok := u["password_hash"]; ok || strings.Contains(rec.Body.String(), "argon") {
			t.Errorf("hash leaked: %s", rec.Body)
		}
	}
	if strings.Join(names, ",") != "admin,admin2,tester,viewer" {
		t.Errorf("order %v", names)
	}

	// Generated password: shown once, forces a change.
	m := e.ok(t, "admin", "POST", "/api/users", `{"username":"Alice.B","role":"viewer"}`, 201)
	pw, _ := m["initial_password"].(string)
	user := m["user"].(map[string]any)
	if len(pw) < 12 || user["username"] != "Alice.B" || user["role"] != "viewer" || user["must_change_password"] != true || user["disabled"] != false || user["last_login"] != nil {
		t.Fatalf("create: %v", m)
	}
	// The password works for login and demands a change.
	lr := e.send("", "POST", "/api/auth/login", `{"username":"alice.b","password":"`+pw+`"}`, false)
	if lr.Code != 200 || !strings.Contains(lr.Body.String(), `"must_change_password":true`) {
		t.Errorf("login with initial password: %d %s", lr.Code, lr.Body)
	}
	// Supplied password: nothing is returned.
	m = e.ok(t, "admin", "POST", "/api/users", `{"username":"bob","role":"admin","password":"correct-horse-battery"}`, 201)
	if v, present := m["initial_password"]; !present || v != nil || m["user"].(map[string]any)["must_change_password"] != true {
		t.Errorf("provided password response: %v", m)
	}
	lr = e.send("", "POST", "/api/auth/login", `{"username":"bob","password":"correct-horse-battery"}`, false)
	if lr.Code != 200 {
		t.Errorf("login with chosen password: %d", lr.Code)
	}
}

func TestCreateUserValidation(t *testing.T) {
	tests := []struct {
		name, body string
		code       int
		errCode    string
	}{
		{"duplicate", `{"username":"viewer","role":"viewer"}`, 409, "user_exists"},
		{"duplicate other case", `{"username":"VIEWER","role":"viewer"}`, 409, "user_exists"},
		{"duplicate admin mixed", `{"username":"AdMiN2","role":"admin"}`, 409, "user_exists"},
		{"short name", `{"username":"ab","role":"viewer"}`, 400, "invalid"},
		{"long name", `{"username":"` + strings.Repeat("a", 65) + `","role":"viewer"}`, 400, "invalid"},
		{"space in name", `{"username":"a b c","role":"viewer"}`, 400, "invalid"},
		{"unicode name", `{"username":"jürgen","role":"viewer"}`, 400, "invalid"},
		{"empty name", `{"username":"","role":"viewer"}`, 400, "invalid"},
		{"bad role", `{"username":"carol","role":"root"}`, 400, "invalid"},
		{"no role", `{"username":"carol"}`, 400, "invalid"},
		{"short pw", `{"username":"carol","role":"viewer","password":"short-pw"}`, 400, "invalid"},
		{"pw equals name", `{"username":"carol-carol-1","role":"viewer","password":"CAROL-CAROL-1"}`, 400, "invalid"},
		{"pw same char", `{"username":"carol","role":"viewer","password":"aaaaaaaaaaaaaaaa"}`, 400, "invalid"},
		{"unknown field", `{"username":"carol","role":"viewer","admin":true}`, 400, "invalid"},
		{"empty body", ``, 415, "invalid"},
		{"max name", `{"username":"` + strings.Repeat("a", 64) + `","role":"viewer"}`, 201, ""},
		{"dots dashes", `{"username":"a.b_c-d","role":"viewer","password":"a-fine-password"}`, 201, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newSetEnv(t)
			rec := e.send("admin", "POST", "/api/users", tt.body, true)
			if rec.Code != tt.code {
				t.Fatalf("%d %s, want %d", rec.Code, rec.Body, tt.code)
			}
			if tt.errCode != "" && !strings.Contains(rec.Body.String(), `"code":"`+tt.errCode+`"`) {
				t.Errorf("body %s", rec.Body)
			}
			if tt.body != "" && strings.Contains(tt.body, "password") && strings.Contains(rec.Body.String(), "password\":\"") {
				t.Errorf("response echoes a password: %s", rec.Body)
			}
		})
	}
}

func TestUserIDParsing(t *testing.T) {
	e := newSetEnv(t)
	for _, id := range []string{"0", "-1", "abc", "1.5", "1e3", "99999999999999999999", "+1"} {
		for _, c := range []struct{ method, suffix, body string }{{"PATCH", "", `{"role":"viewer"}`}, {"POST", "/reset-password", ``}} {
			rec := e.send("admin", c.method, "/api/users/"+id+c.suffix, c.body, true)
			if rec.Code != 400 || !strings.Contains(rec.Body.String(), `"code":"invalid"`) {
				t.Errorf("%s id %q: %d %s", c.method, id, rec.Code, rec.Body)
			}
		}
	}
	for _, c := range []struct{ method, suffix, body string }{{"PATCH", "", `{"role":"viewer"}`}, {"POST", "/reset-password", ``}} {
		if rec := e.send("admin", c.method, "/api/users/987654"+c.suffix, c.body, true); rec.Code != 404 {
			t.Errorf("%s unknown id: %d %s", c.method, rec.Code, rec.Body)
		}
	}
}

func TestResetPasswordInvalidatesSessionsAndStreams(t *testing.T) {
	e := newSetEnv(t)
	srv := httptest.NewServer(e.h)
	t.Cleanup(srv.Close)
	v := sseConnect(t, srv, e.who["viewer"].ck)
	v.until(t, "event: hello")
	if rec := e.send("viewer", "GET", "/api/devices", ``, false); rec.Code != 200 {
		t.Fatalf("viewer before: %d", rec.Code)
	}
	m := e.ok(t, "admin", "POST", fmt.Sprintf("/api/users/%d/reset-password", e.who["viewer"].id), ``, 200)
	pw, _ := m["initial_password"].(string)
	if len(pw) < 12 || len(m) != 1 {
		t.Fatalf("response %v", m)
	}
	v.waitEnd(t, 3*time.Second)
	if rec := e.send("viewer", "GET", "/api/devices", ``, false); rec.Code != 401 {
		t.Errorf("old session after reset: %d", rec.Code)
	}
	if rec := e.send("", "POST", "/api/auth/login", `{"username":"viewer","password":"test-password-123"}`, false); rec.Code != 401 {
		t.Errorf("old password still works: %d", rec.Code)
	}
	lr := e.send("", "POST", "/api/auth/login", `{"username":"viewer","password":"`+pw+`"}`, false)
	if lr.Code != 200 || !strings.Contains(lr.Body.String(), `"must_change_password":true`) {
		t.Errorf("new password: %d %s", lr.Code, lr.Body)
	}
	if !strings.Contains(auditText(t), "admin|-|user_reset_password|ok|user=viewer\n") {
		t.Errorf("audit:\n%s", auditText(t))
	}
}

func TestPatchUserSafeguards(t *testing.T) {
	e := newSetEnv(t)
	a, b, v := e.who["admin"].id, e.who["admin2"].id, e.who["viewer"].id
	patch := func(who string, id int64, body string) *httptest.ResponseRecorder {
		return e.send(who, "PATCH", fmt.Sprintf("/api/users/%d", id), body, true)
	}
	for _, body := range []string{`{"disabled":true}`, `{"role":"viewer"}`, `{"role":"viewer","disabled":false}`} {
		rec := patch("admin", a, body)
		if rec.Code != 409 || !strings.Contains(rec.Body.String(), `"code":"forbidden_change"`) {
			t.Errorf("self %s: %d %s", body, rec.Code, rec.Body)
		}
	}
	// Harmless self edits are fine.
	if rec := patch("admin", a, `{"role":"admin","disabled":false}`); rec.Code != 200 {
		t.Errorf("self no-op: %d %s", rec.Code, rec.Body)
	}
	if rec := patch("admin", a, `{"role":"root"}`); rec.Code != 400 {
		t.Errorf("bad role: %d", rec.Code)
	}
	if rec := patch("admin", a, `{"colour":"x"}`); rec.Code != 400 {
		t.Errorf("unknown field: %d", rec.Code)
	}
	// Promote the viewer, demote the second admin, disable the promoted one: sessions end.
	m := e.ok(t, "admin", "PATCH", fmt.Sprintf("/api/users/%d", v), `{"role":"admin"}`, 200)
	if m["role"] != "admin" || m["username"] != "viewer" {
		t.Errorf("promote: %v", m)
	}
	if rec := e.send("viewer", "GET", "/api/devices", ``, false); rec.Code != 401 {
		t.Errorf("session survived a role change: %d", rec.Code)
	}
	if rec := patch("admin", b, `{"disabled":true}`); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"disabled":true`) {
		t.Fatalf("disable: %d %s", rec.Code, rec.Body)
	}
	if rec := e.send("admin2", "GET", "/api/devices", ``, false); rec.Code != 401 {
		t.Errorf("session survived disabling: %d", rec.Code)
	}
	// Re-enabling keeps working and does not invalidate anything else.
	if rec := patch("admin", b, `{"disabled":false}`); rec.Code != 200 {
		t.Errorf("enable: %d", rec.Code)
	}
	text := auditText(t)
	for _, want := range []string{"admin|-|user_update|ok|user=viewer fields=role\n", "admin|-|user_update|ok|user=admin2 fields=disabled\n"} {
		if !strings.Contains(text, want) {
			t.Errorf("audit lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "user=admin fields") {
		t.Errorf("a refused or no-op self change was audited as done:\n%s", text)
	}
}

func TestLastAdminStore(t *testing.T) {
	d, _, _ := newDeps(t) // "tester" is the only user and an admin
	st := d.Auth.Store()
	viewer, dis := auth.RoleViewer, true
	for _, c := range []struct {
		role *auth.Role
		dis  *bool
	}{{&viewer, nil}, {nil, &dis}, {&viewer, &dis}} {
		if _, err := st.UpdateUser(context.Background(), testUserID, c.role, c.dis); err != auth.ErrLastAdmin {
			t.Errorf("sole admin change: %v", err)
		}
	}
	if _, err := st.UpdateUser(context.Background(), 4242, &viewer, nil); err != auth.ErrUserMissing {
		t.Errorf("missing user: %v", err)
	}
	u, _ := st.UserByID(context.Background(), testUserID)
	if u.Role != auth.RoleAdmin || u.Disabled {
		t.Errorf("sole admin was changed: %+v", u)
	}
}

func TestLastAdminConcurrency(t *testing.T) {
	for i := 0; i < 15; i++ {
		e := newSetEnv(t)
		// Only "admin" and "admin2" (and the fixture "tester", demoted first) may be admins.
		tester := e.userID(t, "tester")
		if _, err := e.d.Auth.Store().UpdateUser(context.Background(), tester, ptr(auth.RoleViewer), nil); err != nil {
			t.Fatal(err)
		}
		a, b := e.who["admin"], e.who["admin2"]
		var wg sync.WaitGroup
		codes := make([]int, 2)
		start := make(chan struct{})
		for n, c := range []struct {
			who    string
			target int64
		}{{"admin", b.id}, {"admin2", a.id}} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				codes[n] = e.send(c.who, "PATCH", fmt.Sprintf("/api/users/%d", c.target), `{"role":"viewer"}`, true).Code
			}()
		}
		close(start)
		wg.Wait()
		if codes[0] == 200 && codes[1] == 200 {
			t.Fatalf("round %d: both demotions succeeded", i)
		}
		us, _ := e.d.Auth.Store().ListUsers(context.Background())
		active := 0
		for _, u := range us {
			if u.Role == auth.RoleAdmin && !u.Disabled {
				active++
			}
		}
		if active != 1 {
			t.Fatalf("round %d: %d active admins after %v", i, active, codes)
		}
	}
}

func ptr[T any](v T) *T { return &v }

func TestInitialPasswordAppearsOnlyInItsResponse(t *testing.T) {
	e := newSetEnv(t)
	srv := httptest.NewServer(e.h)
	t.Cleanup(srv.Close)
	adm := sseConnect(t, srv, e.who["admin"].ck)
	adm.until(t, "event: hello")

	var secrets []string
	m := e.ok(t, "admin", "POST", "/api/users", `{"username":"dana","role":"viewer"}`, 201)
	secrets = append(secrets, m["initial_password"].(string))
	e.ok(t, "admin", "POST", "/api/users", `{"username":"erin","role":"viewer","password":"my-chosen-secret-pw"}`, 201)
	secrets = append(secrets, "my-chosen-secret-pw")
	id := e.userID(t, "dana")
	m = e.ok(t, "admin", "POST", fmt.Sprintf("/api/users/%d/reset-password", id), ``, 200)
	secrets = append(secrets, m["initial_password"].(string))
	// A rejected password must not be echoed either.
	rec := e.send("admin", "POST", "/api/users", `{"username":"frank","role":"viewer","password":"rejected-same-same"}`, true)
	_ = rec
	e.send("admin", "POST", "/api/users", `{"username":"frank","role":"viewer","password":"tooshort"}`, true)
	secrets = append(secrets, "tooshort")
	e.ok(t, "admin", "PATCH", fmt.Sprintf("/api/users/%d", id), `{"disabled":true}`, 200)
	rec = e.send("admin", "GET", "/api/users", ``, false)
	secrets = append(secrets, "NEVER-MATCHES")
	for _, s := range secrets {
		if strings.Contains(rec.Body.String(), s) {
			t.Errorf("GET /api/users leaks %q", s)
		}
	}
	if rec := e.send("admin", "GET", "/api/activity", ``, false); rec.Code == 200 {
		for _, s := range secrets {
			if strings.Contains(rec.Body.String(), s) {
				t.Errorf("activity leaks %q", s)
			}
		}
	}

	e.d.Hub.Publish(events.TypeBrokerState, brokerView{})
	var stream strings.Builder
	done := make(chan struct{})
	go func() {
		defer close(done)
		for adm.sc.Scan() {
			stream.WriteString(adm.sc.Text() + "\n")
			if adm.sc.Text() == "event: broker.state" {
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("no canary")
	}
	for _, s := range secrets {
		for name, hay := range map[string]string{"audit": auditText(t), "logs": e.logs.String(), "sse": stream.String()} {
			if strings.Contains(hay, s) {
				t.Errorf("%s contains a password (%d chars)", name, len(s))
			}
		}
	}
	text := auditText(t)
	for _, want := range []string{"admin|-|user_create|ok|user=dana\n", "admin|-|user_create|ok|user=erin\n", "admin|-|user_reset_password|ok|user=dana\n", "admin|-|user_update|ok|user=dana fields=disabled\n"} {
		if !strings.Contains(text, want) {
			t.Errorf("audit lacks %q:\n%s", want, text)
		}
	}
}

func TestSettingsWriteRateLimit(t *testing.T) {
	e := newSetEnv(t)
	e.fs.devs = []devices.Device{{Key: "dradis/a", Prefix: "dradis", Device: "a"}}
	for i := 0; i < settingsWritesPerMin; i++ {
		if rec := e.send("admin", "PATCH", "/api/devices/dradis/a", `{"hidden":false}`, true); rec.Code != 200 {
			t.Fatalf("write %d: %d", i, rec.Code)
		}
	}
	rec := e.send("admin", "PATCH", "/api/devices/dradis/a", `{"hidden":false}`, true)
	if rec.Code != 429 || rec.Header().Get("Retry-After") == "" || !strings.Contains(rec.Body.String(), `"code":"rate_limited"`) {
		t.Fatalf("over limit: %d %v %s", rec.Code, rec.Header(), rec.Body)
	}
	// The limit is per admin, shared by all settings writes, and does not cover reads.
	if rec := e.send("admin", "POST", "/api/users", `{"username":"zed","role":"viewer"}`, true); rec.Code != 429 {
		t.Errorf("other route: %d", rec.Code)
	}
	if rec := e.send("admin2", "PATCH", "/api/devices/dradis/a", `{"hidden":false}`, true); rec.Code != 200 {
		t.Errorf("other admin: %d", rec.Code)
	}
	if rec := e.send("admin", "GET", "/api/users", ``, false); rec.Code != 200 {
		t.Errorf("read: %d", rec.Code)
	}
}

func TestWriteLimiterWindow(t *testing.T) {
	now := time.Unix(1000, 0)
	l := newWriteLimiter(func() time.Time { return now })
	for i := 0; i < settingsWritesPerMin; i++ {
		if ok, _ := l.allow(1); !ok {
			t.Fatal("limited early")
		}
	}
	if ok, retry := l.allow(1); ok || retry < 1 || retry > 61 {
		t.Fatalf("ok=%v retry=%d", ok, retry)
	}
	now = now.Add(61 * time.Second)
	if ok, _ := l.allow(1); !ok {
		t.Fatal("still limited after the window")
	}
}

func TestSettingsMethodNotAllowed(t *testing.T) {
	e := newSetEnv(t)
	cases := []struct{ method, path, allow string }{
		{"DELETE", "/api/users", "GET, POST"},
		{"PUT", "/api/users", "GET, POST"},
		{"GET", "/api/users/1", "PATCH"},
		{"DELETE", "/api/users/1", "PATCH"},
		{"GET", "/api/users/1/reset-password", "POST"},
		{"PUT", "/api/devices/dradis/a", "DELETE, GET, PATCH"},
		{"POST", "/api/devices/dradis/a", "DELETE, GET, PATCH"},
	}
	for _, c := range cases {
		if rec := e.send("", c.method, c.path, "", false); rec.Code != 401 {
			t.Errorf("anonymous %s %s: %d", c.method, c.path, rec.Code)
		}
		rec := e.send("admin", c.method, c.path, "", true)
		if rec.Code != 405 || rec.Header().Get("Allow") != c.allow {
			t.Errorf("%s %s: %d allow=%q want %q", c.method, c.path, rec.Code, rec.Header().Get("Allow"), c.allow)
		}
	}
}

func TestOneTimePasswordResponsesAreNoStore(t *testing.T) {
	e := newSetEnv(t)
	check := func(what string, rec *httptest.ResponseRecorder, code int) {
		t.Helper()
		if rec.Code != code {
			t.Fatalf("%s: %d %s", what, rec.Code, rec.Body)
		}
		if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Pragma") != "no-cache" {
			t.Errorf("%s: Cache-Control=%q Pragma=%q", what, rec.Header().Get("Cache-Control"), rec.Header().Get("Pragma"))
		}
	}
	check("create", e.send("admin", "POST", "/api/users", `{"username":"dana","role":"viewer"}`, true), 201)
	check("reset", e.send("admin", "POST", fmt.Sprintf("/api/users/%d/reset-password", e.userID(t, "dana")), ``, true), 200)
}

func TestAdminHashingBoundOnSettingsRoutes(t *testing.T) {
	e := newSetEnv(t)
	peak, capacity, saturate, restore := auth.HashLimitsForTests(15*time.Millisecond, 5*time.Second)
	defer restore()
	var wg sync.WaitGroup
	codes := make([]int, 25) // the settings write limit is 30 a minute
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = e.send("admin", "POST", "/api/users", fmt.Sprintf(`{"username":"bulk%02d","role":"viewer"}`, i), true).Code
		}()
	}
	wg.Wait()
	for i, c := range codes {
		if c != 201 {
			t.Errorf("request %d: %d", i, c)
		}
	}
	if p := peak(); p > capacity || p < 2 {
		t.Fatalf("peak concurrent argon2 = %d, capacity %d", p, capacity)
	}

	// Exhausted budget: 503 busy, nothing created, audit says rejected:busy, no "failed".
	restore()
	_, _, _, restore2 := auth.HashLimitsForTests(0, 30*time.Millisecond)
	defer restore2()
	release := saturate()
	defer release()
	rec := e.send("admin", "POST", "/api/users", `{"username":"late","role":"viewer"}`, true)
	if rec.Code != 503 || strings.TrimSpace(rec.Body.String()) != `{"error":"busy","code":"busy"}` {
		t.Fatalf("create busy: %d %s", rec.Code, rec.Body)
	}
	rec = e.send("admin", "POST", fmt.Sprintf("/api/users/%d/reset-password", e.userID(t, "bulk00")), ``, true)
	if rec.Code != 503 || strings.TrimSpace(rec.Body.String()) != `{"error":"busy","code":"busy"}` {
		t.Fatalf("reset busy: %d %s", rec.Code, rec.Body)
	}
	release()
	if _, _, err := e.d.Auth.Store().UserByName(context.Background(), "late"); err == nil {
		t.Error("user was created although the request answered busy")
	}
	text := auditText(t)
	for _, want := range []string{"admin|-|user_create|rejected:busy|user=late\n", "user_reset_password|rejected:busy|user=bulk00\n"} {
		if !strings.Contains(text, want) {
			t.Errorf("audit lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "|failed|") {
		t.Errorf("busy was audited as failed:\n%s", text)
	}
}

func TestPatchUserSessionDeleteFailureIs500AndRollsBack(t *testing.T) {
	e := newSetEnv(t)
	target := e.userID(t, "admin2")
	if _, err := testDB.Exec(`CREATE TRIGGER no_session_delete BEFORE DELETE ON sessions
		BEGIN SELECT RAISE(ABORT, 'boom'); END`); err != nil {
		t.Fatal(err)
	}
	rec := e.send("admin", "PATCH", fmt.Sprintf("/api/users/%d", target), `{"role":"viewer"}`, true)
	if rec.Code != 500 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	u, err := e.d.Auth.Store().UserByID(context.Background(), target)
	if err != nil || u.Role != auth.RoleAdmin {
		t.Fatalf("role change survived the failure: %+v %v", u, err)
	}
	if text := auditText(t); !strings.Contains(text, "admin|-|user_update|failed|user=admin2 fields=role\n") {
		t.Errorf("audit:\n%s", text)
	}
	if _, err := testDB.Exec(`DROP TRIGGER no_session_delete`); err != nil {
		t.Fatal(err)
	}
}

type bogusPayload struct{ Secret string }

func TestViewerSSEFailsClosedOnUnknownDevicePayload(t *testing.T) {
	var buf bytes.Buffer
	s := &server{Deps: Deps{Log: slog.New(slog.NewTextHandler(&buf, nil))}}
	for _, typ := range []string{events.TypeDeviceUpdated, events.TypeDeviceRemoved} {
		for range 3 {
			if _, drop := s.viewerView(events.Event{ID: 1, Type: typ, Data: bogusPayload{"x"}}); !drop {
				t.Errorf("%s with an unknown payload reached a viewer", typ)
			}
		}
	}
	if _, drop := s.viewerView(events.Event{Type: events.TypeBrokerState, Data: bogusPayload{}}); drop {
		t.Error("non-device events must pass")
	}
	out := buf.String()
	if n := strings.Count(out, "level=WARN"); n != 1 {
		t.Errorf("want one WARN for the type, got %d:\n%s", n, out)
	}
	if !strings.Contains(out, "api.bogusPayload") || strings.Contains(out, "Secret") {
		t.Errorf("log must name the type only:\n%s", out)
	}
}

func TestConcurrentDeviceEditsPublishInStoreOrder(t *testing.T) {
	e := newSetEnv(t)
	e.fs.devs = []devices.Device{{Key: "dradis/a", Prefix: "dradis", Device: "a", FullName: "A"}}
	ch, stop := e.d.Hub.Subscribe()
	defer stop()
	for round := range 12 {
		var wg sync.WaitGroup
		for _, c := range []string{"#111111", "#222222"} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				e.send("admin", "PATCH", "/api/devices/dradis/a", fmt.Sprintf(`{"color":%q}`, c), true)
			}()
		}
		wg.Wait()
		var last devices.Device
		for range 2 {
			select {
			case ev := <-ch:
				last = ev.Data.(deviceUpdate).Device
			case <-time.After(3 * time.Second):
				t.Fatal("missing device.updated")
			}
		}
		e.fs.mu.Lock()
		final := e.fs.devs[0].Color
		e.fs.mu.Unlock()
		if last.Color != final {
			t.Fatalf("round %d: last event %q but stored %q", round, last.Color, final)
		}
		e.fs.mu.Lock()
		e.fs.devs[0].Color = ""
		e.fs.mu.Unlock()
	}
}
