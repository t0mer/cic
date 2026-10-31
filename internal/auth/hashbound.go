package auth

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// hashSlots is how many argon2 computations (64 MiB each) may run at once, process-wide.
// Logins, admin user creation and password resets all draw from this one budget.
const hashSlots = 4

// hashQueueWait is how long a request may wait for an argon2 slot before giving up with ErrBusy.
const hashQueueWait = 5 * time.Second

// ErrBusy is returned when no argon2 slot freed up in time. No verdict was reached, so
// callers must not record a failed attempt.
var ErrBusy = errors.New("auth: too many password operations in progress")

// hashPool is the shared budget; Service.hashSem points at it unless a test swaps it.
var hashPool = make(chan struct{}, hashSlots)

// hashPoolWait is the wait budget of the store's hashing (Service has its own hashWait).
var hashPoolWait = hashQueueWait

var (
	hashInFlight atomic.Int32
	hashPeak     atomic.Int32
	hashHold     atomic.Int64 // test-only: nanoseconds each slot holder sleeps
)

// acquireSlot takes a slot from sem, waiting at most wait. It returns ErrBusy on timeout
// and ctx.Err() when the request ends first.
func acquireSlot(ctx context.Context, sem chan struct{}, wait time.Duration) (release func(), err error) {
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case sem <- struct{}{}:
	case <-t.C:
		return nil, ErrBusy
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	n := hashInFlight.Add(1)
	for p := hashPeak.Load(); n > p && !hashPeak.CompareAndSwap(p, n); p = hashPeak.Load() {
	}
	if d := hashHold.Load(); d > 0 {
		time.Sleep(time.Duration(d))
	}
	return func() { hashInFlight.Add(-1); <-sem }, nil
}

// HashPasswordContext is HashPassword on the shared bounded budget: it waits up to five
// seconds for a slot and then fails with ErrBusy. The CLI keeps using HashPassword.
func HashPasswordContext(ctx context.Context, pw string) (string, error) {
	release, err := acquireSlot(ctx, hashPool, hashPoolWait)
	if err != nil {
		return "", err
	}
	defer release()
	return HashPassword(pw)
}

// HashLimitsForTests exposes the shared bound to tests in other packages: peak returns the
// highest number of simultaneous argon2 slot holders since the call, capacity the bound;
// hold makes every slot holder linger that long so contention is real; saturate takes every
// slot and shortens the wait to wait until release is called. Never call it outside tests.
func HashLimitsForTests(hold, wait time.Duration) (peak func() int, capacity int, saturate func() (release func()), restore func()) {
	hashPeak.Store(0)
	hashHold.Store(int64(hold))
	oldWait := hashPoolWait
	hashPoolWait = wait
	saturate = func() func() {
		for range hashSlots {
			hashPool <- struct{}{}
		}
		var once sync.Once
		return func() { // idempotent, so tests can both call and defer it
			once.Do(func() {
				for range hashSlots {
					<-hashPool
				}
			})
		}
	}
	return func() int { return int(hashPeak.Load()) }, hashSlots, saturate,
		func() { hashHold.Store(0); hashPoolWait = oldWait }
}
