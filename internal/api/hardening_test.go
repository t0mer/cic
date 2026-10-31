package api

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/t0mer/cic/internal/audit"
	"github.com/t0mer/cic/internal/commands"
	"github.com/t0mer/cic/internal/events"
)

// failingLog is an ActivityLog whose Begin and/or Finish fail.
type failingLog struct {
	*audit.Store
	failBegin, failFinish bool
}

func (f failingLog) Begin(ctx context.Context, e audit.Entry) (int64, error) {
	if f.failBegin {
		return 0, errors.New("disk full")
	}
	return f.Store.Begin(ctx, e)
}

func (f failingLog) Finish(ctx context.Context, id int64, status, cid string) error {
	if f.failFinish {
		return errors.New("db gone")
	}
	return f.Store.Finish(ctx, id, status, cid)
}

func TestAuditIntentFailureRefusesAndNeverPublishes(t *testing.T) {
	e := newCmdEnvWith(t, func(d *Deps, e *cmdEnv) { d.Audit = failingLog{Store: e.aud, failBegin: true} })
	for _, rt := range cmdRoutes {
		rec := e.send("POST", rt.path, rt.body, "admin", true)
		if rec.Code != 503 || !strings.Contains(rec.Body.String(), `"code":"unavailable"`) {
			t.Errorf("%s: %d %s", rt.name, rec.Code, rec.Body)
		}
	}
	if e.fc.n() != 0 {
		t.Fatalf("commander reached %d times although the intent row could not be written", e.fc.n())
	}
}

func TestAuditFinalUpdateFailureIsLoggedAtErrorWithoutSensitiveData(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	e := newCmdEnvWith(t, func(d *Deps, e *cmdEnv) {
		d.Audit = failingLog{Store: e.aud, failFinish: true}
		d.Log = log
	})
	body := `{"phone":"+972501234567","text":"Secret message body that is long"}`
	rec := e.send("POST", "/api/devices/dradis/a/sms", body, "admin", true)
	if rec.Code != 202 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	out := buf.String()
	if !strings.Contains(out, "level=ERROR") || !strings.Contains(out, "audit final update failed") {
		t.Fatalf("no ERROR log: %s", out)
	}
	if strings.Contains(out, "972501234567") || strings.Contains(out, "Secret") {
		t.Fatalf("sensitive data logged: %s", out)
	}
}

func TestOutcomeUnknownResponseAndAuditRow(t *testing.T) {
	e := newCmdEnv(t)
	e.fc.err = &OutcomeUnknownError{ID: "abc123", Err: fmt.Errorf("%w: publishing sendsms: timeout", commands.ErrOutcomeUnknown)}
	rec := e.send("POST", "/api/devices/dradis/a/sms", `{"phone":"+972501234567","text":"hi there"}`, "admin", true)
	if rec.Code != 504 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Retry-After") != "" {
		t.Errorf("headers %v", rec.Header())
	}
	if !strings.Contains(rec.Body.String(), `"retry":false`) || !strings.Contains(rec.Body.String(), `"code":"outcome_unknown"`) {
		t.Errorf("body %s", rec.Body)
	}
	// Other errors carry no retry hint.
	e.fc.err = commands.ErrNotConnected
	if rec := e.send("POST", "/api/devices/dradis/a/ping", "", "admin", true); strings.Contains(rec.Body.String(), "retry") {
		t.Errorf("retry field on a retryable error: %s", rec.Body)
	}

	var status, cid string
	if err := testDB.QueryRow(`SELECT status, command_id FROM audit_log WHERE action = 'sms'`).Scan(&status, &cid); err != nil {
		t.Fatal(err)
	}
	if status != "unknown" || cid != "abc123" {
		t.Fatalf("row %q %q", status, cid)
	}
	// The later phone-side result (or Sweep) updates the same row.
	if err := e.aud.SetOutcome(context.Background(), "abc123", "no_confirmation"); err != nil {
		t.Fatal(err)
	}
	_ = testDB.QueryRow(`SELECT status FROM audit_log WHERE action = 'sms'`).Scan(&status)
	if status != "no_confirmation" {
		t.Fatalf("status %q", status)
	}
}

func TestAuditDetailInvalidPhoneIsNotEchoed(t *testing.T) {
	e := newCmdEnv(t)
	e.send("POST", "/api/devices/dradis/a/sms", `{"phone":"call me maybe 0501234567","text":"hi"}`, "admin", true)
	page, _ := e.aud.List(context.Background(), 5, 0)
	if len(page.Items) != 1 || page.Items[0].Detail != `phone=invalid len=2 "hi"` {
		t.Fatalf("%+v", page.Items)
	}
}

func TestMethodNotAllowedAllowHeaderAndAuthOrder(t *testing.T) {
	e := newCmdEnv(t)
	cases := []struct{ method, path, allow string }{
		{"GET", "/api/devices/dradis/a/ping", "POST"},
		{"GET", "/api/devices/dradis/a/ping/stop", "POST"},
		{"GET", "/api/devices/dradis/a/notify", "POST"},
		{"GET", "/api/devices/dradis/a/sms", "POST"},
		{"GET", "/api/devices/dradis/a/refresh-status", "POST"},
		{"DELETE", "/api/devices/dradis/a/refresh-location", "POST"},
		{"POST", "/api/devices", "GET"},
		{"POST", "/api/devices/dradis/a", "GET"},
		{"POST", "/api/activity", "GET"},
		{"POST", "/api/events", "GET"},
	}
	for _, c := range cases {
		if rec := e.send(c.method, c.path, "", "", false); rec.Code != 401 {
			t.Errorf("anonymous %s %s: %d, want 401 before 405", c.method, c.path, rec.Code)
		}
		rec := e.send(c.method, c.path, "", "admin", true)
		if rec.Code != 405 || rec.Header().Get("Allow") != c.allow {
			t.Errorf("%s %s: %d allow=%q want %q", c.method, c.path, rec.Code, rec.Header().Get("Allow"), c.allow)
		}
		if rec := e.send(c.method, c.path, "", "admin", false); rec.Code != 405 {
			t.Errorf("%s %s without csrf: %d, want 405", c.method, c.path, rec.Code)
		}
	}
	if rec := e.send("GET", "/api/no/such/thing", "", "", false); rec.Code != 404 {
		t.Errorf("unknown path anonymous: %d", rec.Code)
	}
	if rec := e.send("GET", "/api/no/such/thing", "", "admin", false); rec.Code != 404 {
		t.Errorf("unknown path: %d", rec.Code)
	}
}

func TestEventErrorSanitizing(t *testing.T) {
	phone := "+972501234567"
	for _, in := range []string{
		"radio off, called 972501234567",
		"failed for +972501234567",
		"sent to 050-123-4567 failed",
		"tried (050) 123 4567",
		"digits 1234567890123 everywhere",
	} {
		out := eventError(in, phone)
		if regexp.MustCompile(`[0-9]{4}`).MatchString(out) || strings.Contains(out, "972501234567") || !strings.Contains(out, "***") {
			t.Errorf("%q -> %q", in, out)
		}
	}
	if got := eventError("error code 42 at step 7", phone); got != "error code 42 at step 7" {
		t.Errorf("short numbers must survive: %q", got)
	}
	if got := eventError(strings.Repeat("é", 500), ""); len([]rune(got)) != 120 {
		t.Errorf("cap: %d runes", len([]rune(got)))
	}
	ev := NewCommandResultEvent(commands.Outcome{CommandID: "c", DeviceKey: "d/a", Phone: phone, Status: "failed", Error: strings.Repeat("x", 300) + " 972501234567"})
	if len([]rune(ev.Error)) > 120 || strings.Contains(ev.PhoneMasked, "50123") {
		t.Errorf("%+v", ev)
	}
}

func sseConnect(t *testing.T, srv *httptest.Server, ck *http.Cookie) *sseClient {
	t.Helper()
	req, _ := http.NewRequest("GET", srv.URL+"/api/events", nil)
	req.AddCookie(ck)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	if resp.StatusCode != 200 {
		t.Fatalf("sse status %d", resp.StatusCode)
	}
	return &sseClient{resp, bufio.NewScanner(resp.Body)}
}

func TestCommandResultEventsAdminOnly(t *testing.T) {
	e := newCmdEnv(t)
	srv := httptest.NewServer(e.h)
	t.Cleanup(srv.Close)
	admin, viewer := sseConnect(t, srv, e.adminCk), sseConnect(t, srv, e.viewerCk)
	admin.until(t, "event: hello")
	viewer.until(t, "event: hello")

	e.hub.Publish(events.TypeCommandResult, NewCommandResultEvent(commands.Outcome{CommandID: "c1", DeviceKey: "dradis/a",
		Phone: "+972501234567", Status: "failed", Error: "radio off, called 0501234567 or +972 50 123 4567"}))
	e.hub.Publish(events.TypeBrokerState, brokerView{}) // canary: sent after the result

	// The viewer sees the canary and nothing of the result before it.
	var seen []string
	done := make(chan struct{})
	go func() {
		defer close(done)
		for viewer.sc.Scan() {
			seen = append(seen, viewer.sc.Text())
			if viewer.sc.Text() == "event: broker.state" {
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("viewer never saw the canary event")
	}
	if all := strings.Join(seen, "\n"); strings.Contains(all, "command.result") || strings.Contains(all, "dradis/a") {
		t.Fatalf("viewer received command.result: %s", all)
	}

	admin.until(t, "event: command.result")
	if !admin.sc.Scan() {
		t.Fatal("no data line")
	}
	data := admin.sc.Text()
	if !strings.Contains(data, `"phone_masked":"+*********567"`) || regexp.MustCompile(`[0-9]{4}`).MatchString(data) {
		t.Fatalf("admin event leaks digits or lacks the masked phone: %s", data)
	}
}

func TestConcurrentCommandsRaceFree(t *testing.T) {
	e := newCmdEnv(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.send("POST", "/api/devices/dradis/a/refresh-status", "", "admin", true)
		}()
	}
	wg.Wait()
	if p, _ := e.aud.List(context.Background(), 50, 0); len(p.Items) != 8 {
		t.Fatalf("rows %d", len(p.Items))
	}
}
