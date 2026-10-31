package mqtt

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
	"github.com/eclipse/paho.mqtt.golang/packets"

	"github.com/t0mer/cic/internal/config"
	"github.com/t0mer/cic/internal/ratelog"
	"github.com/t0mer/cic/internal/store"
)

const (
	clientIDMetaKey = "mqtt_client_id"
	eventQueueSize  = 256
	quiesceMillis   = 250

	reconnectMin     = time.Second
	reconnectMax     = 20 * time.Second
	authRetryEvery   = 60 * time.Second
	subscribeBackoff = 5 * time.Second
	subscribeMax     = 60 * time.Second
	subscribeTimeout = 15 * time.Second
	errNotAuthorized = "not authorized — check credentials"
)

// Event is a parsed telemetry message. Payload is one of Status, Version,
// Battery, Wifi, Location, DeviceInfo or SMSResult.
type Event struct {
	Prefix     string
	Device     string
	Leaf       string
	Retained   bool
	ReceivedAt time.Time
	Payload    any
}

// Sink receives parsed events. Handle is called from a single worker goroutine.
type Sink interface {
	Handle(Event)
}

// State is a snapshot of the broker connection.
type State struct {
	Connected bool      `json:"connected"`
	LastError string    `json:"last_error"`
	Since     time.Time `json:"since"`
}

// Client is a single-broker MQTT subscriber.
type Client struct {
	cfg      config.MQTTConfig
	log      *slog.Logger
	sink     Sink
	clientID string
	filters  []string
	pc       paho.Client
	queue    *eventQueue
	track    *connTracker
	lostCh   chan struct{}
	dropWarn ratelog.Limiter

	mu        sync.Mutex
	runCtx    context.Context
	subCancel context.CancelFunc
}

// New builds a Client, generating and persisting its client ID on first use.
func New(ctx context.Context, cfg config.MQTTConfig, db *sql.DB, sink Sink, log *slog.Logger) (*Client, error) {
	id, err := clientID(ctx, db)
	if err != nil {
		return nil, err
	}
	c := &Client{
		cfg:      cfg,
		log:      log,
		sink:     sink,
		clientID: id,
		filters:  SubscribeFilters(cfg.Prefixes),
		queue:    newEventQueue(eventQueueSize),
		track:    newConnTracker(),
		lostCh:   make(chan struct{}, 1),
		dropWarn: ratelog.Limiter{Interval: 10 * time.Second},
		runCtx:   context.Background(),
	}
	opts, err := c.options()
	if err != nil {
		return nil, err
	}
	c.pc = paho.NewClient(opts)
	return c, nil
}

func clientID(ctx context.Context, db *sql.DB) (string, error) {
	id, err := store.GetMeta(ctx, db, clientIDMetaKey)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return "", err
	}
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating client id: %w", err)
	}
	id = "cic-" + hex.EncodeToString(b)
	if err := store.SetMeta(ctx, db, clientIDMetaKey, id); err != nil {
		return "", err
	}
	return id, nil
}

func (c *Client) options() (*paho.ClientOptions, error) {
	b := c.cfg.Broker
	scheme := "tcp"
	if b.TLS {
		scheme = "ssl"
	}
	opts := paho.NewClientOptions().
		AddBroker(scheme + "://" + net.JoinHostPort(b.Host, strconv.Itoa(b.Port))).
		SetClientID(c.clientID).
		SetProtocolVersion(4).
		SetCleanSession(true).
		SetAutoReconnect(false).
		SetConnectRetry(false).
		SetKeepAlive(45 * time.Second).
		SetOrderMatters(true).
		SetOnConnectHandler(c.onConnect).
		SetConnectionLostHandler(c.onConnectionLost)
	if b.Username != "" {
		opts.SetUsername(b.Username).SetPassword(b.Password)
	}
	if b.TLS {
		tc, err := c.tlsConfig()
		if err != nil {
			return nil, err
		}
		opts.SetTLSConfig(tc)
	}
	return opts, nil
}

func (c *Client) tlsConfig() (*tls.Config, error) {
	b := c.cfg.Broker
	tc := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: b.Host}
	if b.CAFile != "" {
		pem, err := os.ReadFile(b.CAFile)
		if err != nil {
			return nil, fmt.Errorf("reading mqtt ca_file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("mqtt ca_file contains no valid certificates")
		}
		tc.RootCAs = pool
	}
	if b.InsecureSkipVerify {
		tc.InsecureSkipVerify = true //nolint:gosec // explicitly requested by configuration
		c.log.Warn("mqtt tls certificate verification is disabled")
	}
	return tc, nil
}

func (c *Client) onConnect(pc paho.Client) {
	gen := c.track.begin()
	c.mu.Lock()
	if c.subCancel != nil {
		c.subCancel()
	}
	ctx, cancel := context.WithCancel(c.runCtx)
	c.subCancel = cancel
	c.mu.Unlock()

	sub := func(filter string) error {
		tok := pc.Subscribe(filter, 1, c.onMessage)
		if !tok.WaitTimeout(subscribeTimeout) {
			return errors.New("subscribe timed out")
		}
		return tok.Error()
	}
	fail := func(filter string, err error) {
		c.log.Warn("mqtt subscribe failed", "filter", filter)
		c.track.fail(gen, "subscribe failed: "+err.Error())
	}
	if subscribeAll(ctx, c.filters, sub, fail, sleepCtx) != nil {
		return
	}
	if c.track.connected(gen) {
		b := c.cfg.Broker
		c.log.Info("mqtt connected", "client_id", c.clientID, "broker", net.JoinHostPort(b.Host, strconv.Itoa(b.Port)), "filters", len(c.filters))
	}
}

func (c *Client) onConnectionLost(_ paho.Client, err error) {
	c.mu.Lock()
	if c.subCancel != nil {
		c.subCancel()
	}
	c.mu.Unlock()
	c.track.lost(err.Error())
	c.log.Warn("mqtt connection lost", "err", err)
	select {
	case c.lostCh <- struct{}{}:
	default:
	}
}

// subscribeAll subscribes each filter separately and retries the failed ones
// with doubling backoff until all succeed or ctx is done.
func subscribeAll(ctx context.Context, filters []string, sub func(string) error,
	fail func(string, error), sleep func(context.Context, time.Duration) bool) error {
	pending := filters
	delay := subscribeBackoff
	for {
		var failed []string
		for _, f := range pending {
			if err := sub(f); err != nil {
				fail(f, err)
				failed = append(failed, f)
			}
		}
		if len(failed) == 0 {
			return nil
		}
		pending = failed
		if !sleep(ctx, delay) {
			return ctx.Err()
		}
		delay = min(delay*2, subscribeMax)
	}
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// retryDelay returns how long to wait after a failed connect and the next
// ordinary backoff step. Authentication rejections retry slowly.
func retryDelay(err error, cur time.Duration) (wait, next time.Duration, authFailed bool) {
	if errors.Is(err, packets.ErrorRefusedBadUsernameOrPassword) || errors.Is(err, packets.ErrorRefusedNotAuthorised) {
		return authRetryEvery, reconnectMin, true
	}
	return cur, min(cur*2, reconnectMax), false
}

func (c *Client) onMessage(_ paho.Client, m paho.Message) {
	prefix, device, leaf, ok := ParseTopic(m.Topic(), c.cfg.Prefixes)
	if !ok {
		return
	}
	if m.Retained() && leaf != LeafStatus && leaf != LeafVersion {
		return
	}
	payload, err := parseLeaf(leaf, m.Payload())
	if err != nil {
		c.log.Debug("mqtt payload dropped", "device", device, "leaf", leaf, "err", err)
		return
	}
	c.enqueue(Event{
		Prefix:     prefix,
		Device:     device,
		Leaf:       leaf,
		Retained:   m.Retained(),
		ReceivedAt: time.Now(),
		Payload:    payload,
	})
}

func (c *Client) enqueue(ev Event) {
	if !c.queue.push(ev) {
		return
	}
	if total, ok := c.dropWarn.Hit(); ok {
		c.log.Warn("mqtt event queue full, dropped telemetry", "dropped_total", total)
	}
}

func (c *Client) worker(ctx context.Context) {
	for {
		ev, ok := c.queue.next(ctx)
		if !ok {
			return
		}
		c.safeHandle(ev)
	}
}

func (c *Client) safeHandle(ev Event) {
	defer func() {
		if r := recover(); r != nil {
			c.log.Error("mqtt sink panicked", "device", ev.Device, "leaf", ev.Leaf, "panic", r)
		}
	}()
	c.sink.Handle(ev)
}

// Run connects to the broker, retrying until it succeeds, and blocks until ctx
// is cancelled, then disconnects.
func (c *Client) Run(ctx context.Context) {
	c.mu.Lock()
	c.runCtx = ctx
	c.mu.Unlock()
	workerDone := make(chan struct{})
	workerCtx, stopWorker := context.WithCancel(context.WithoutCancel(ctx))
	go func() {
		defer close(workerDone)
		c.worker(workerCtx)
	}()
	c.connectLoop(ctx)
	c.pc.Disconnect(quiesceMillis)
	stopWorker()
	<-workerDone
}

// connectLoop keeps the connection up: it reconnects after a loss and retries
// failed attempts with backoff.
func (c *Client) connectLoop(ctx context.Context) {
	delay := reconnectMin
	for {
		select {
		case <-c.lostCh:
		default:
		}
		tok := c.pc.Connect()
		select {
		case <-ctx.Done():
			return
		case <-tok.Done():
		}
		if err := tok.Error(); err != nil {
			wait, next, authFailed := retryDelay(err, delay)
			delay = next
			msg := "connect failed: " + err.Error()
			if authFailed {
				msg = errNotAuthorized
			}
			c.track.fail(0, msg)
			c.log.Warn("mqtt connect failed", "err", err, "retry_in", wait)
			if !sleepCtx(ctx, wait) {
				return
			}
			continue
		}
		delay = reconnectMin
		select {
		case <-ctx.Done():
			return
		case <-c.lostCh:
		}
		if !sleepCtx(ctx, delay) {
			return
		}
	}
}

// Connected reports whether the client is connected and subscribed.
func (c *Client) Connected() bool {
	return c.State().Connected
}

// State returns the current connection state.
func (c *Client) State() State {
	return c.track.snapshot()
}

// ClientID returns the persisted MQTT client ID.
func (c *Client) ClientID() string {
	return c.clientID
}

// Errors returned by Publish. ErrNotConnected, ErrInvalidTopic and ErrNotSent
// prove that nothing was written to the broker; ErrConnectionLost, a timeout or
// a cancellation while waiting for the acknowledgement do not.
var (
	// ErrNotConnected is returned by Publish when the broker link is down. It
	// also wraps ErrConnectionLost when the link dropped mid-publish.
	ErrNotConnected = errors.New("mqtt not connected")
	// ErrConnectionLost means the link dropped before the broker acknowledged
	// the publish: the message may or may not have been delivered.
	ErrConnectionLost = errors.New("mqtt connection lost before publish completed")
	// ErrInvalidTopic means the topic is not a valid command topic; nothing was sent.
	ErrInvalidTopic = errors.New("invalid command topic")
	// ErrNotSent means the context ended before anything was written.
	ErrNotSent = errors.New("publish not attempted")
)

// NotSent reports whether err proves that the message was never written.
func NotSent(err error) bool {
	if errors.Is(err, ErrConnectionLost) {
		return false
	}
	return errors.Is(err, ErrNotConnected) || errors.Is(err, ErrInvalidTopic) || errors.Is(err, ErrNotSent)
}

const publishTimeout = 10 * time.Second

// mapTokenError converts a paho token error. "Not connected" proves nothing
// was sent; a connection lost mid-publish does not (it still wraps
// ErrNotConnected so callers see a broker outage, but NotSent is false).
func mapTokenError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, paho.ErrNotConnected):
		return ErrNotConnected
	case strings.Contains(strings.ToLower(err.Error()), "connection lost"):
		return fmt.Errorf("%w: %w", ErrNotConnected, ErrConnectionLost)
	}
	return fmt.Errorf("publishing command: %w", err)
}

// Publish sends payload to a command topic with QoS 1, never retained, and
// waits for the broker's acknowledgement. The topic must be a command topic
// under a configured prefix. Payloads are never logged. See NotSent for which
// errors prove the message was not written.
func (c *Client) Publish(ctx context.Context, topic string, payload []byte) error {
	parts := strings.Split(topic, "/")
	if len(parts) != 3 {
		return ErrInvalidTopic
	}
	if _, err := NewBuilder(c.cfg.Prefixes).Command(parts[0], parts[1], parts[2]); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidTopic, err)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: %w", ErrNotSent, err)
	}
	if !c.Connected() || c.pc == nil || !c.pc.IsConnectionOpen() {
		return ErrNotConnected
	}
	tok := c.pc.Publish(topic, 1, false, payload)
	ctx, cancel := context.WithTimeout(ctx, publishTimeout)
	defer cancel()
	return waitToken(ctx, tok)
}

// waitToken waits for tok or ctx, whichever ends first.
func waitToken(ctx context.Context, tok paho.Token) error {
	select {
	case <-tok.Done():
		return mapTokenError(tok.Error())
	case <-ctx.Done():
		return fmt.Errorf("waiting for publish acknowledgement: %w", ctx.Err())
	}
}
