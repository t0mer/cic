package commands

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/t0mer/cic/internal/devices"
	"github.com/t0mer/cic/internal/mqtt"
	"github.com/t0mer/cic/internal/store"
)

// Errors returned by Service. Use errors.Is; the HTTP layer maps them as
// listed in the package documentation (400 ErrInvalid, 404 ErrUnknownDevice,
// 403 ErrSMSDisabled, 429 ErrRateLimited/ErrRecipientLimited/ErrSMSCap,
// 503 ErrNotConnected/ErrUnavailable, 504 ErrOutcomeUnknown).
var (
	ErrInvalid       = errors.New("invalid command")
	ErrUnknownDevice = errors.New("unknown device")
	ErrRateLimited   = errors.New("rate limit exceeded")
	ErrSMSCap        = errors.New("daily sms cap reached")
	ErrSMSDisabled   = errors.New("sms sending is disabled")
	// ErrRecipientLimited means too many SMS went to the same number this hour.
	ErrRecipientLimited = errors.New("too many sms to this recipient")
	// ErrNotConnected means the broker link was down; nothing was sent.
	ErrNotConnected = errors.New("mqtt broker not connected")
	// ErrUnavailable means the device or usage store failed; nothing was sent.
	ErrUnavailable = errors.New("service temporarily unavailable")
	// ErrOutcomeUnknown means the publish may have reached the broker (timeout,
	// cancellation or connection loss while waiting). The command, and for SMS
	// its budget reservation, are kept: callers must not blindly retry.
	ErrOutcomeUnknown = errors.New("command outcome unknown")
)

// Action names reported in Result.
const (
	ActionPing        = "ping"
	ActionStopPing    = "ping_stop"
	ActionNotify      = "notify"
	ActionSendSMS     = "sendsms"
	ActionGetStatus   = "getstatus"
	ActionGetLocation = "getlocation"
)

const (
	maxPingSeconds = 600
	maxTitleRunes  = 100
	maxNotifyRunes = 1000
	maxSMSRunes    = 1600
	defaultTitle   = "CIC"

	pingPerMinute     = 10 // ping and stop-ping share one bucket
	notifyPerMinute   = 10
	smsPerMinute      = 5
	statusPerMinute   = 20
	locationPerMinute = 20

	maxSMSPerRecipient = 3 // per normalized number, per rolling hour
	recipientWindow    = time.Hour
)

// rate names a rate-limit bucket (shared by actions that use the same name)
// and its per-minute maximum.
type rate struct {
	bucket string
	max    int
}

var phoneRe = regexp.MustCompile(`^\+[1-9]\d{6,14}$`)

// Publisher publishes a command payload to a topic.
type Publisher interface {
	Publish(ctx context.Context, topic string, payload []byte) error
}

// DeviceLookup finds registered devices by key.
type DeviceLookup interface {
	Get(ctx context.Context, key string) (devices.Device, error)
}

// Config configures a Service.
type Config struct {
	Prefixes    []string
	PingSeconds int // default ping duration
	SMSDailyCap int // billed SMS segments per UTC day; 0 disables SMS
	Now         func() time.Time
	Log         *slog.Logger
	Usage       UsageStore  // required for SMS when SMSDailyCap > 0
	Correlator  *Correlator // optional
}

// Result describes an accepted command.
type Result struct {
	ID        string    `json:"id"`
	Action    string    `json:"action"`
	DeviceKey string    `json:"device_key"`
	SentAt    time.Time `json:"sent_at"`
}

// Service validates, rate limits and publishes device commands.
//
// The per-user rate-limit key is the user argument of each method, which the
// HTTP layer must set from the authenticated user id (never from request
// input). If Config.Correlator is set, the wiring MUST call
// Correlator.Sweep(time.Now()) about every 5 seconds, or SMS without a phone
// result are never reported as no_confirmation.
type Service struct {
	cfg     Config
	pub     Publisher
	devs    DeviceLookup
	builder *mqtt.Builder
	limits  *limiter
	recips  *limiter // SMS per recipient, rolling hour

	releaseFailures atomic.Int64
}

// ReleaseFailures counts SMS budget releases that failed (the reserved
// segments stay consumed until the day rolls over).
func (s *Service) ReleaseFailures() int64 { return s.releaseFailures.Load() }

// NewService returns a Service.
func NewService(pub Publisher, devs DeviceLookup, cfg Config) *Service {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	return &Service{cfg: cfg, pub: pub, devs: devs, builder: mqtt.NewBuilder(cfg.Prefixes), limits: newLimiter(time.Minute), recips: newLimiter(recipientWindow)}
}

// Ping starts a ping for seconds (0 means the configured default).
func (s *Service) Ping(ctx context.Context, user, key string, seconds int) (Result, error) {
	if seconds == 0 {
		seconds = s.cfg.PingSeconds
	}
	if seconds < 1 || seconds > maxPingSeconds {
		return Result{}, fmt.Errorf("%w: seconds must be between 1 and %d", ErrInvalid, maxPingSeconds)
	}
	return s.send(ctx, user, key, ActionPing, mqtt.CmdPing, map[string]any{"seconds": seconds}, rate{"ping", pingPerMinute}, nil)
}

// StopPing stops a running ping.
func (s *Service) StopPing(ctx context.Context, user, key string) (Result, error) {
	return s.send(ctx, user, key, ActionStopPing, mqtt.CmdPing, map[string]any{"seconds": 0}, rate{"ping", pingPerMinute}, nil)
}

// Notify shows a notification on the device. An empty title becomes "CIC".
func (s *Service) Notify(ctx context.Context, user, key, title, text string) (Result, error) {
	if title == "" {
		title = defaultTitle
	}
	if err := checkText("title", title, maxTitleRunes, false); err != nil {
		return Result{}, err
	}
	if err := checkText("text", text, maxNotifyRunes, true); err != nil {
		return Result{}, err
	}
	return s.send(ctx, user, key, ActionNotify, mqtt.CmdNotify, map[string]any{"title": title, "text": text}, rate{"notify", notifyPerMinute}, nil)
}

// smsInfo carries the SMS-only parts of a send.
type smsInfo struct {
	segments int    // billed segments, reserved against the daily cap
	digits   string // normalized recipient for the per-recipient limit
}

// SendSMS asks the device to send an SMS. phone must be E.164. The daily cap
// counts billed segments, so a long message uses several units.
func (s *Service) SendSMS(ctx context.Context, user, key, phone, text string) (Result, error) {
	if s.cfg.SMSDailyCap <= 0 {
		return Result{}, ErrSMSDisabled
	}
	if !phoneRe.MatchString(phone) {
		return Result{}, fmt.Errorf("%w: phone must be in international format, e.g. +972501234567", ErrInvalid)
	}
	if err := checkText("text", text, maxSMSRunes, true); err != nil {
		return Result{}, err
	}
	_, _, segs := SMSSegments(text)
	return s.send(ctx, user, key, ActionSendSMS, mqtt.CmdSendSMS, map[string]any{"phone": phone, "text": text},
		rate{"sms", smsPerMinute}, &smsInfo{segments: segs, digits: phoneDigits(phone)})
}

// GetStatus asks the device to republish its status.
func (s *Service) GetStatus(ctx context.Context, user, key string) (Result, error) {
	return s.send(ctx, user, key, ActionGetStatus, mqtt.CmdGetStatus, map[string]any{}, rate{"getstatus", statusPerMinute}, nil)
}

// GetLocation asks the device to publish its location.
func (s *Service) GetLocation(ctx context.Context, user, key string) (Result, error) {
	return s.send(ctx, user, key, ActionGetLocation, mqtt.CmdGetLocation, map[string]any{}, rate{"getlocation", locationPerMinute}, nil)
}

// send runs the shared pipeline: validation, device check, rate limit, SMS
// recipient limit and budget, correlator registration, publish. The rate-limit
// slot is taken only after validation passed and the device is known.
func (s *Service) send(ctx context.Context, user, key, action, leaf string, body map[string]any, rl rate, sms *smsInfo) (Result, error) {
	prefix, device, ok := mqtt.SplitKey(key, s.cfg.Prefixes)
	if !ok {
		return Result{}, fmt.Errorf("%w: malformed device key", ErrInvalid)
	}
	topic, err := s.builder.Command(prefix, device, leaf)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if err := ctx.Err(); err != nil {
		return Result{}, fmt.Errorf("%s not attempted: %w", action, err)
	}
	if _, err := s.devs.Get(ctx, key); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return Result{}, ErrUnknownDevice
		}
		s.cfg.Log.Warn("command failed", "action", action, "device", key, "error_kind", "device_lookup")
		return Result{}, fmt.Errorf("%w: looking up device: %v", ErrUnavailable, err)
	}
	now := s.cfg.Now()
	if rl.max > 0 && !s.limits.allow(rl.bucket+"\x00"+user, rl.max, now) {
		return Result{}, ErrRateLimited
	}
	id := newID()
	day := now.UTC().Format("2006-01-02")
	if sms != nil {
		if s.cfg.Usage == nil {
			s.cfg.Log.Warn("command failed", "action", action, "device", key, "error_kind", "usage_store_missing")
			return Result{}, fmt.Errorf("%w: sms usage store not configured", ErrUnavailable)
		}
		if !s.recips.allow(sms.digits, maxSMSPerRecipient, now) {
			return Result{}, ErrRecipientLimited
		}
		got, err := s.cfg.Usage.Reserve(ctx, day, sms.segments, s.cfg.SMSDailyCap)
		if err != nil || !got {
			s.recips.undo(sms.digits, now)
			if err != nil {
				s.cfg.Log.Warn("command failed", "action", action, "device", key, "error_kind", "reserve")
				return Result{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
			}
			return Result{}, ErrSMSCap
		}
		if s.cfg.Correlator != nil {
			s.cfg.Correlator.Record(id, key, body["phone"].(string))
		}
	}
	payload, err := json.Marshal(body)
	if err == nil {
		// A cancelled context proves nothing was written.
		if err = ctx.Err(); err == nil {
			err = s.pub.Publish(ctx, topic, payload)
		} else {
			err = fmt.Errorf("%w: %w", mqtt.ErrNotSent, err)
		}
	} else {
		err = fmt.Errorf("encoding command: %w: %w", err, mqtt.ErrNotSent)
	}
	if err != nil {
		if mqtt.NotSent(err) {
			if sms != nil {
				s.release(ctx, action, key, id, day, sms.segments)
				s.recips.undo(sms.digits, now)
				if s.cfg.Correlator != nil {
					s.cfg.Correlator.Forget(id)
				}
			}
		}
		return Result{}, s.publishError(action, key, err)
	}
	res := Result{ID: id, Action: action, DeviceKey: key, SentAt: now}
	s.cfg.Log.Info("command sent", "action", action, "device", key, "id", res.ID)
	return res, nil
}

// publishError maps a publish failure to the Service error contract. Failures
// that prove nothing was sent have already been rolled back by the caller; for
// every other failure the SMS reservation and correlator entry are kept.
func (s *Service) publishError(action, key string, err error) error {
	switch {
	case errors.Is(err, mqtt.ErrInvalidTopic):
		s.cfg.Log.Info("command failed", "action", action, "device", key, "error_kind", "invalid_topic")
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	case errors.Is(err, mqtt.ErrNotSent):
		s.cfg.Log.Info("command failed", "action", action, "device", key, "error_kind", "not_sent")
		return fmt.Errorf("publishing %s: %w", action, err)
	case mqtt.NotSent(err): // not connected
		s.cfg.Log.Info("command failed", "action", action, "device", key, "error_kind", "not_connected")
		return ErrNotConnected
	}
	s.cfg.Log.Warn("command outcome unknown", "action", action, "device", key, "error_kind", "publish_unconfirmed")
	return fmt.Errorf("%w: publishing %s: %w", ErrOutcomeUnknown, action, err)
}

// release returns segments to the daily budget after a publish that provably
// sent nothing. A failure is logged (no phone or text) and counted.
func (s *Service) release(ctx context.Context, action, key, id, day string, segments int) {
	if err := s.cfg.Usage.Release(context.WithoutCancel(ctx), day, segments); err != nil {
		s.releaseFailures.Add(1)
		s.cfg.Log.Warn("sms budget release failed", "action", action, "device", key, "id", id, "error_kind", "release_failed")
	}
}

func newID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand does not fail on supported platforms
	}
	return hex.EncodeToString(b)
}

// checkText requires a valid UTF-8 string of at most max runes that is not
// blank. Control (Cc) and format (Cf: bidi overrides and isolates, zero-width
// characters, BOM) characters are rejected; multiline additionally allows
// newline and tab.
func checkText(field, s string, max int, multiline bool) error {
	switch {
	case !utf8.ValidString(s):
		return fmt.Errorf("%w: %s is not valid UTF-8", ErrInvalid, field)
	case strings.TrimSpace(s) == "":
		return fmt.Errorf("%w: %s is required", ErrInvalid, field)
	case utf8.RuneCountInString(s) > max:
		return fmt.Errorf("%w: %s exceeds %d characters", ErrInvalid, field, max)
	}
	for _, r := range s {
		if multiline && (r == '\n' || r == '\t') {
			continue
		}
		if unicode.In(r, unicode.Cc, unicode.Cf) {
			return fmt.Errorf("%w: %s contains control or invisible formatting characters", ErrInvalid, field)
		}
	}
	return nil
}
