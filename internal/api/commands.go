package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/go-chi/chi/v5"

	"github.com/t0mer/cic/internal/audit"
	"github.com/t0mer/cic/internal/auth"
	"github.com/t0mer/cic/internal/commands"
	"github.com/t0mer/cic/internal/mqtt"
)

// Commander is the command layer as seen by the API; *commands.Service implements it.
type Commander interface {
	Ping(ctx context.Context, user, key string, seconds int) (commands.Result, error)
	StopPing(ctx context.Context, user, key string) (commands.Result, error)
	Notify(ctx context.Context, user, key, title, text string) (commands.Result, error)
	SendSMS(ctx context.Context, user, key, phone, text string) (commands.Result, error)
	GetStatus(ctx context.Context, user, key string) (commands.Result, error)
	GetLocation(ctx context.Context, user, key string) (commands.Result, error)
}

// ActivityLog is the audit log as seen by the API; *audit.Store implements it.
//
// Every command is audited in two steps: Begin writes a pending intent row BEFORE the
// command is published (if it fails nothing is published), Finish sets the final status.
type ActivityLog interface {
	Begin(ctx context.Context, e audit.Entry) (rowID int64, err error)
	Finish(ctx context.Context, rowID int64, status, commandID string) error
	List(ctx context.Context, limit int, before int64) (audit.Page, error)
}

// Error codes returned in the `code` field of command errors.
const (
	codeInvalid        = "invalid"
	codeUnknownDevice  = "unknown_device"
	codeRateLimited    = "rate_limited"
	codeSMSCap         = "sms_cap"
	codeRecipientLimit = "recipient_limited"
	codeSMSDisabled    = "sms_disabled"
	codeNotConnected   = "not_connected"
	codeOutcomeUnknown = "outcome_unknown"
	codeUnavailable    = "unavailable"
)

const (
	actionSMS       = "sms" // API name of commands.ActionSendSMS
	defaultActivity = 50
	maxActivity     = 200
	auditTimeout    = 5 * time.Second
)

// CommandResultEvent is the data of the `command.result` SSE event. It never
// carries the raw phone number or the message text.
type CommandResultEvent struct {
	ID          string `json:"id"`
	Action      string `json:"action"`
	Device      string `json:"device"`
	Status      string `json:"status"`
	Error       string `json:"error,omitempty"`
	PhoneMasked string `json:"phone_masked,omitempty"`
}

const maxEventError = 120

// NewCommandResultEvent builds the SSE payload for an SMS outcome. The phone-side error
// text is cut to 120 runes and stripped of anything number-like, whatever the correlator
// already did to it.
func NewCommandResultEvent(o commands.Outcome) CommandResultEvent {
	return CommandResultEvent{ID: o.CommandID, Action: actionSMS, Device: o.DeviceKey, Status: o.Status,
		Error: eventError(o.Error, o.Phone), PhoneMasked: commands.MaskPhone(o.Phone)}
}

func isNumSep(r rune) bool {
	switch r {
	case ' ', '-', '.', '(', ')', '+':
		return true
	}
	return false
}

// eventError replaces every number-like run (6 or more digits, possibly separated by
// spaces, dashes, dots, brackets or plus signs) and every literal occurrence of phone with
// "***", drops control characters and cuts the result to maxEventError runes.
func eventError(msg, phone string) string {
	msg = strings.ToValidUTF8(msg, "")
	if phone != "" {
		msg = strings.ReplaceAll(msg, phone, "***")
	}
	rs := make([]rune, 0, len(msg))
	for _, r := range msg {
		if !unicode.In(r, unicode.Cc, unicode.Cf) {
			rs = append(rs, r)
		}
	}
	var b strings.Builder
	for i := 0; i < len(rs); {
		if !unicode.IsDigit(rs[i]) {
			b.WriteRune(rs[i])
			i++
			continue
		}
		j, digits, lastDigit := i, 0, i
		for j < len(rs) && (unicode.IsDigit(rs[j]) || (isNumSep(rs[j]) && j+1 < len(rs))) {
			if unicode.IsDigit(rs[j]) {
				digits++
				lastDigit = j
			}
			j++
		}
		if digits >= 6 {
			b.WriteString("***")
			i = lastDigit + 1
		} else {
			b.WriteRune(rs[i])
			i++
		}
	}
	out := strings.TrimSpace(b.String())
	if r := []rune(out); len(r) > maxEventError {
		out = string(r[:maxEventError])
	}
	return out
}

// OutcomeUnknownError is returned by a Commander when the broker did not confirm a
// command that may nevertheless have been delivered. ID is the command id when known;
// it lets the audit row be linked to the later SMS outcome.
type OutcomeUnknownError struct {
	ID  string
	Err error // wraps commands.ErrOutcomeUnknown
}

func (e *OutcomeUnknownError) Error() string { return e.Err.Error() }
func (e *OutcomeUnknownError) Unwrap() error { return e.Err }

type commandAccepted struct {
	ID     string `json:"id"`
	Action string `json:"action"`
	Device string `json:"device"`
	SentAt string `json:"sent_at"`
}

type commandError struct {
	Error string `json:"error"`
	Code  string `json:"code"`
	// Retry is present (false) only for outcome_unknown: clients and proxies must NOT
	// retry automatically, the command may already have been delivered.
	Retry *bool `json:"retry,omitempty"`
}

func writeCmdError(w http.ResponseWriter, status int, code, msg string) {
	ce := commandError{Error: msg, Code: code}
	if code == codeOutcomeUnknown {
		no := false
		ce.Retry = &no
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, status, ce)
}

// classify maps a command-layer error to HTTP status, code, message and Retry-After
// seconds (0 = none). ok is false for a cancelled request, which gets no response.
func classify(err error, now time.Time) (status int, code, msg string, retry int, ok bool) {
	switch {
	case errors.Is(err, commands.ErrOutcomeUnknown):
		return http.StatusGatewayTimeout, codeOutcomeUnknown,
			"The broker did not confirm the command. It MAY still be delivered; do not retry blindly.", 0, true
	case errors.Is(err, commands.ErrInvalid):
		return http.StatusBadRequest, codeInvalid, err.Error(), 0, true
	case errors.Is(err, commands.ErrUnknownDevice):
		return http.StatusNotFound, codeUnknownDevice, "unknown device", 0, true
	case errors.Is(err, commands.ErrSMSDisabled):
		return http.StatusForbidden, codeSMSDisabled, "sms sending is disabled", 0, true
	case errors.Is(err, commands.ErrRateLimited):
		return http.StatusTooManyRequests, codeRateLimited, "rate limit exceeded, try again in a minute", 60, true
	case errors.Is(err, commands.ErrRecipientLimited):
		return http.StatusTooManyRequests, codeRecipientLimit, "too many sms to this recipient, try again later", 3600, true
	case errors.Is(err, commands.ErrSMSCap):
		next := time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day()+1, 0, 0, 0, 0, time.UTC)
		return http.StatusTooManyRequests, codeSMSCap, "daily sms cap reached", max(1, int(next.Sub(now).Seconds())), true
	case errors.Is(err, commands.ErrNotConnected), errors.Is(err, mqtt.ErrNotConnected):
		return http.StatusServiceUnavailable, codeNotConnected, "mqtt broker not connected; nothing was sent", 0, true
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return 0, "", "", 0, false
	}
	return http.StatusServiceUnavailable, codeUnavailable, "service temporarily unavailable; nothing was sent", 0, true
}

// decodeBody strictly decodes one JSON object into v. An empty body is allowed
// only when optional is true.
func decodeBody(w http.ResponseWriter, r *http.Request, v any, optional bool) bool {
	raw, err := io.ReadAll(r.Body)
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		writeCmdError(w, http.StatusRequestEntityTooLarge, codeInvalid, "request body too large")
		return false
	}
	if err != nil {
		writeCmdError(w, http.StatusBadRequest, codeInvalid, "could not read request body")
		return false
	}
	if len(bytes.TrimSpace(raw)) == 0 && optional {
		return true
	}
	if mt, _, perr := mime.ParseMediaType(r.Header.Get("Content-Type")); perr != nil || mt != "application/json" {
		writeCmdError(w, http.StatusUnsupportedMediaType, codeInvalid, "Content-Type must be application/json")
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeCmdError(w, http.StatusBadRequest, codeInvalid, "request body must be a valid JSON object with known fields")
		return false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		writeCmdError(w, http.StatusBadRequest, codeInvalid, "request body must be a single JSON object")
		return false
	}
	return true
}

// cmdSpec describes one command route.
type cmdSpec struct {
	action string
	// prep decodes and validates the request (writing the 400 itself on failure) and
	// returns the audit detail and the call that runs the command. ok=false means the
	// request was already answered; nothing is audited then.
	prep func(s *server, w http.ResponseWriter, r *http.Request) (detail string, call func(ctx context.Context, user, key string) (commands.Result, error), ok bool)
}

// command serves one command route. Audit is fail-closed: the pending intent row is
// written before the command is published and, if that fails, nothing is published.
//
// outcome_unknown (504) means the command MAY have been delivered. It is not
// idempotent: clients and proxies must never retry it automatically.
func (s *server) command(spec cmdSpec) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		prefix, dev := chi.URLParam(r, "prefix"), chi.URLParam(r, "device")
		if !mqtt.ValidSegment(prefix) || !mqtt.ValidSegment(dev) {
			writeCmdError(w, http.StatusBadRequest, codeInvalid, "invalid prefix or device")
			return
		}
		u, _ := auth.UserFrom(r.Context())
		key := prefix + "/" + dev
		detail, call, ok := spec.prep(s, w, r)
		if !ok {
			return
		}
		rowID, ok := s.begin(audit.Entry{User: u.Username, Device: key, Action: spec.action, Detail: detail})
		if !ok {
			writeCmdError(w, http.StatusServiceUnavailable, codeUnavailable, "service temporarily unavailable; nothing was sent")
			return
		}
		res, err := call(r.Context(), strconv.FormatInt(u.ID, 10), key)
		if err == nil {
			s.finish(rowID, spec.action, audit.StatusSent, res.ID)
			writeJSON(w, http.StatusAccepted, commandAccepted{ID: res.ID, Action: spec.action, Device: res.DeviceKey,
				SentAt: res.SentAt.UTC().Format(time.RFC3339)})
			return
		}
		status, code, msg, retry, respond := classify(err, time.Now())
		if !respond {
			// The client went away before anything was sent.
			s.finish(rowID, spec.action, "rejected:cancelled", "")
			return
		}
		if code == codeOutcomeUnknown {
			id := res.ID
			var ou *OutcomeUnknownError
			if errors.As(err, &ou) && ou.ID != "" {
				id = ou.ID
			}
			s.finish(rowID, spec.action, audit.StatusUnknown, id)
		} else {
			s.finish(rowID, spec.action, "rejected:"+code, "")
		}
		if r.Context().Err() != nil {
			return // the client went away; nobody is listening
		}
		if retry > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(retry))
		}
		writeCmdError(w, status, code, msg)
	}
}

// begin writes the intent row, surviving a cancelled request. ok=false means the caller
// must not publish. With no audit store configured there is nothing to write.
func (s *server) begin(e audit.Entry) (rowID int64, ok bool) {
	if s.Audit == nil {
		return 0, true
	}
	ctx, cancel := context.WithTimeout(context.Background(), auditTimeout)
	defer cancel()
	id, err := s.Audit.Begin(ctx, e)
	if err != nil {
		s.log().Error("audit intent write failed, command refused", "action", e.Action, "device", e.Device, "err", err)
		return 0, false
	}
	return id, true
}

// finish sets the final status of the intent row. A failure is logged without any
// sensitive data and counted by the audit store; the row stays pending (recovered to
// unknown at the next start).
func (s *server) finish(rowID int64, action, status, commandID string) {
	if s.Audit == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), auditTimeout)
	defer cancel()
	if err := s.Audit.Finish(ctx, rowID, status, commandID); err != nil {
		s.log().Error("audit final update failed", "action", action, "status", status, "row", rowID, "err", err)
	}
}

func (s *server) commander() Commander {
	if s.Commands == nil {
		return unavailableCommander{}
	}
	return s.Commands
}

// unavailableCommander answers every command with ErrNotConnected (no broker configured).
type unavailableCommander struct{}

func (unavailableCommander) Ping(context.Context, string, string, int) (commands.Result, error) {
	return commands.Result{}, commands.ErrNotConnected
}
func (unavailableCommander) StopPing(context.Context, string, string) (commands.Result, error) {
	return commands.Result{}, commands.ErrNotConnected
}
func (unavailableCommander) Notify(context.Context, string, string, string, string) (commands.Result, error) {
	return commands.Result{}, commands.ErrNotConnected
}
func (unavailableCommander) SendSMS(context.Context, string, string, string, string) (commands.Result, error) {
	return commands.Result{}, commands.ErrNotConnected
}
func (unavailableCommander) GetStatus(context.Context, string, string) (commands.Result, error) {
	return commands.Result{}, commands.ErrNotConnected
}
func (unavailableCommander) GetLocation(context.Context, string, string) (commands.Result, error) {
	return commands.Result{}, commands.ErrNotConnected
}

// simple builds a prep for a command that takes no body.
func simple(detail string, f func(c Commander, ctx context.Context, user, key string) (commands.Result, error)) func(*server, http.ResponseWriter, *http.Request) (string, func(context.Context, string, string) (commands.Result, error), bool) {
	return func(s *server, _ http.ResponseWriter, _ *http.Request) (string, func(context.Context, string, string) (commands.Result, error), bool) {
		return detail, func(ctx context.Context, user, key string) (commands.Result, error) {
			return f(s.commander(), ctx, user, key)
		}, true
	}
}

// phoneRe mirrors the phone check of package commands; it only decides whether the
// phone may be shown (masked) in the audit detail.
var phoneRe = regexp.MustCompile(`^\+[1-9]\d{6,14}$`)

func (s *server) cmdRoutes(r chi.Router) {
	r.Post("/devices/{prefix}/{device}/ping", s.command(cmdSpec{"ping", func(s *server, w http.ResponseWriter, r *http.Request) (string, func(context.Context, string, string) (commands.Result, error), bool) {
		var body struct {
			Seconds int `json:"seconds"`
		}
		if !decodeBody(w, r, &body, true) {
			return "", nil, false
		}
		detail := "seconds=default"
		if body.Seconds != 0 {
			detail = fmt.Sprintf("seconds=%d", body.Seconds)
		}
		return detail, func(ctx context.Context, user, key string) (commands.Result, error) {
			return s.commander().Ping(ctx, user, key, body.Seconds)
		}, true
	}}))
	r.Post("/devices/{prefix}/{device}/ping/stop", s.command(cmdSpec{"ping_stop", simple("", func(c Commander, ctx context.Context, user, key string) (commands.Result, error) {
		return c.StopPing(ctx, user, key)
	})}))
	r.Post("/devices/{prefix}/{device}/notify", s.command(cmdSpec{"notify", func(s *server, w http.ResponseWriter, r *http.Request) (string, func(context.Context, string, string) (commands.Result, error), bool) {
		var body struct {
			Title string `json:"title"`
			Text  string `json:"text"`
		}
		if !decodeBody(w, r, &body, false) {
			return "", nil, false
		}
		// Audit detail: the length and the first 20 characters of the text (owner spec).
		n, first := commands.Preview(body.Text)
		return fmt.Sprintf("len=%d %q", n, first), func(ctx context.Context, user, key string) (commands.Result, error) {
			return s.commander().Notify(ctx, user, key, body.Title, body.Text)
		}, true
	}}))
	r.Post("/devices/{prefix}/{device}/sms", s.command(cmdSpec{actionSMS, func(s *server, w http.ResponseWriter, r *http.Request) (string, func(context.Context, string, string) (commands.Result, error), bool) {
		var body struct {
			Phone string `json:"phone"`
			Text  string `json:"text"`
		}
		if !decodeBody(w, r, &body, false) {
			return "", nil, false
		}
		// Audit detail: phone masked to its last 3 digits (or "invalid" for anything that
		// is not a phone number, so junk is never echoed), then the text length and its
		// first 20 characters (owner spec).
		phone := "invalid"
		if phoneRe.MatchString(body.Phone) {
			phone = commands.MaskPhone(body.Phone)
		}
		n, first := commands.Preview(body.Text)
		return fmt.Sprintf("phone=%s len=%d %q", phone, n, first), func(ctx context.Context, user, key string) (commands.Result, error) {
			return s.commander().SendSMS(ctx, user, key, body.Phone, body.Text)
		}, true
	}}))
	r.Post("/devices/{prefix}/{device}/refresh-status", s.command(cmdSpec{commands.ActionGetStatus, simple("", func(c Commander, ctx context.Context, user, key string) (commands.Result, error) {
		return c.GetStatus(ctx, user, key)
	})}))
	r.Post("/devices/{prefix}/{device}/refresh-location", s.command(cmdSpec{commands.ActionGetLocation, simple("", func(c Commander, ctx context.Context, user, key string) (commands.Result, error) {
		return c.GetLocation(ctx, user, key)
	})}))
	r.Get("/activity", s.activity)
}

func (s *server) activity(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := defaultActivity
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxActivity {
			writeCmdError(w, http.StatusBadRequest, codeInvalid, "limit must be between 1 and 200")
			return
		}
		limit = n
	}
	var before int64
	if v := q.Get("before"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 1 {
			writeCmdError(w, http.StatusBadRequest, codeInvalid, "before must be a positive id")
			return
		}
		before = n
	}
	if s.Audit == nil {
		writeJSON(w, http.StatusOK, audit.Page{Items: []audit.Item{}})
		return
	}
	page, err := s.Audit.List(r.Context(), limit, before)
	if err != nil {
		s.log().Error("listing activity", "err", err)
		writeCmdError(w, http.StatusServiceUnavailable, codeUnavailable, "service temporarily unavailable")
		return
	}
	writeJSON(w, http.StatusOK, page)
}
