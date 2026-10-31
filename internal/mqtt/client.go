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
