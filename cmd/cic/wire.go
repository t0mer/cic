package main

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/t0mer/cic/internal/api"
	"github.com/t0mer/cic/internal/audit"
	"github.com/t0mer/cic/internal/commands"
	"github.com/t0mer/cic/internal/events"
	"github.com/t0mer/cic/internal/mqtt"
)

const (
	sweepEvery          = 5 * time.Second
	auditPurgeEvery     = time.Hour
	locationsPurgeEvery = time.Hour
	outcomeWriteLimit   = 5 * time.Second
)

// fanoutSink delivers every MQTT event to each sink in order. A panic in one sink is
// recovered and logged (never with the event) so it cannot skip the sinks after it.
type fanoutSink struct {
	sinks []mqtt.Sink
	log   *slog.Logger
}

// Handle implements mqtt.Sink.
func (f fanoutSink) Handle(ev mqtt.Event) {
	for _, s := range f.sinks {
		f.handle(s, ev)
	}
}

func (f fanoutSink) handle(s mqtt.Sink, ev mqtt.Event) {
	defer func() {
		if r := recover(); r != nil {
			log := f.log
			if log == nil {
				log = slog.Default()
			}
			// Only the types: a panic value or the event could carry payload data.
			log.Error("mqtt sink panicked, event skipped for this sink", "sink", fmt.Sprintf("%T", s), "panic_type", fmt.Sprintf("%T", r))
		}
	}()
	s.Handle(ev)
}

// observerSink feeds sendsms/result events to the SMS correlator.
type observerSink struct{ c *commands.Correlator }

// Handle implements mqtt.Sink.
func (o observerSink) Handle(ev mqtt.Event) { o.c.Observe(ev) }

// noBroker is the Publisher used when no broker is configured: nothing is ever sent.
type noBroker struct{}

// Publish implements commands.Publisher.
func (noBroker) Publish(context.Context, string, []byte) error { return mqtt.ErrNotConnected }

// outcomeQueue is the number of SMS outcomes that may wait for delivery.
const outcomeQueue = 256

// outcomeRelay applies SMS outcomes (audit update + `command.result` event) on its own
// goroutine, so the MQTT worker and the correlator never wait for SQLite or hold a lock
// while it runs. The queue is bounded: on overflow the outcome is logged and counted,
// never blocked on.
type outcomeRelay struct {
	hub   *events.Hub
	store *audit.Store
	log   *slog.Logger

	ch      chan commands.Outcome
	done    chan struct{}
	stopped chan struct{}
	once    sync.Once
	dropped atomic.Int64
}

func newOutcomeRelay(hub *events.Hub, store *audit.Store, log *slog.Logger) *outcomeRelay {
	o := &outcomeRelay{hub: hub, store: store, log: log, ch: make(chan commands.Outcome, outcomeQueue),
		done: make(chan struct{}), stopped: make(chan struct{})}
	go o.run()
	return o
}

// Deliver queues o without ever blocking. It is the Correlator's deliver callback.
func (o *outcomeRelay) Deliver(out commands.Outcome) {
	select {
	case <-o.done:
		return
	default:
	}
	select {
	case o.ch <- out:
	default:
		o.dropped.Add(1)
		o.log.Error("sms outcome queue full, outcome dropped", "id", out.CommandID, "status", out.Status)
	}
}

// Dropped counts outcomes lost to a full queue.
func (o *outcomeRelay) Dropped() int64 { return o.dropped.Load() }

// Close stops the relay after delivering what is already queued. It is idempotent.
func (o *outcomeRelay) Close() {
	o.once.Do(func() { close(o.done) })
	<-o.stopped
}

func (o *outcomeRelay) run() {
	defer close(o.stopped)
	for {
		select {
		case out := <-o.ch:
			o.apply(out)
		case <-o.done:
			for {
				select {
				case out := <-o.ch:
					o.apply(out)
				default:
					return
				}
			}
		}
	}
}

func (o *outcomeRelay) apply(out commands.Outcome) {
	ctx, cancel := context.WithTimeout(context.Background(), outcomeWriteLimit)
	defer cancel()
	if err := o.store.SetOutcome(ctx, out.CommandID, out.Status); err != nil {
		o.log.Error("audit outcome update failed", "id", out.CommandID, "status", out.Status, "err", err)
	}
	o.hub.Publish(events.TypeCommandResult, api.NewCommandResultEvent(out))
}

// newCorrelator returns a Correlator whose outcomes go through relay.
func newCorrelator(relay *outcomeRelay, now func() time.Time) *commands.Correlator {
	return commands.NewCorrelator(relay.Deliver, now)
}

// runSweeper calls c.Sweep(now()) every interval until ctx is done.
func runSweeper(ctx context.Context, c *commands.Correlator, every time.Duration, now func() time.Time) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.Sweep(now())
		}
	}
}
