package auth

import (
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

// Limiter tracks login failures per key and imposes an exponentially growing lockout:
// after the key's threshold of failures it is locked for Base, doubling with each further
// failure up to Max.
//
// Keys come in two policies. A "user:<name>" (or any non-"ip:") key locks after Threshold
// failures and is forgotten after an hour of quiet. An "ip:<addr>" key is deliberately far
// more tolerant (IPThreshold, IPWindow): its only job is to slow a single source spraying
// many accounts, so one noisy client (or one reverse proxy that all clients share) cannot
// lock every user out of logging in.
//
// Accepted trade-off: the per-username lockout is global, so anybody who can reach the
// login endpoint can lock a known username out for up to Max by failing on purpose. That
// is a denial of service on one account, not a bypass; the lockout expires on its own and
// the operator can reset it by restarting the service.
type Limiter struct {
	Threshold   int           // failures that lock a username (or other non-IP) key
	IPThreshold int           // failures within IPWindow that lock a source address
	Base, Max   time.Duration // first lockout and its cap
	IPWindow    time.Duration // quiet time after which an IP key's failure count decays

	mu         sync.Mutex
	now        func() time.Time
	m          map[string]*entry
	maxEntries int // 0 means the package default
}

type entry struct {
	fails       int
	pending     int // attempts that passed Begin and have not settled yet
	lockedUntil time.Time
	lastFail    time.Time
}

const (
	forgetAfter = time.Hour
	maxEntries  = 100_000
	maxKeyLen   = 64
	ipPrefix    = "ip:"
)

// NewLimiter returns a limiter with the default policy: usernames lock after 5 failures,
// source addresses after 30 failures in 15 minutes; lockouts start at 30 s and cap at 15 min.
func NewLimiter(now func() time.Time) *Limiter {
	if now == nil {
		now = time.Now
	}
	return &Limiter{Threshold: 5, IPThreshold: 30, Base: 30 * time.Second, Max: 15 * time.Minute,
		IPWindow: 15 * time.Minute, now: now, m: map[string]*entry{}}
}

func clip(k string) string {
	if len(k) > maxKeyLen+8 {
		return k[:maxKeyLen+8]
	}
	return k
}

func (l *Limiter) capacity() int {
	if l.maxEntries > 0 {
		return l.maxEntries
	}
	return maxEntries
}

// policy returns the lock threshold and the quiet period after which failures are forgotten.
func (l *Limiter) policy(k string) (int, time.Duration) {
	if strings.HasPrefix(k, ipPrefix) {
		return l.IPThreshold, l.IPWindow
	}
	return l.Threshold, forgetAfter
}

func (e *entry) locked(now time.Time) bool { return e.lockedUntil.After(now) }

// idle reports whether the entry carries no information worth keeping.
func (e *entry) idle(now time.Time, forget time.Duration) bool {
	return e.pending == 0 && !e.locked(now) && now.Sub(e.lastFail) > forget
}

// Check returns how long the caller must wait, or 0 if no key is locked.
func (l *Limiter) Check(keys ...string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	var wait time.Duration
	for _, k := range keys {
		if e := l.m[clip(k)]; e != nil && e.locked(now) {
			wait = max(wait, e.lockedUntil.Sub(now))
		}
	}
	return wait
}

// Attempt is a login attempt admitted by Begin. Exactly one of Fail, Success or Cancel
// settles it; further calls do nothing.
type Attempt struct {
	l    *Limiter
	keys []string
	done bool
}

// Begin admits an attempt on every key, or reports how long the caller must wait. The
// attempt is counted as pending until settled, so N parallel requests cannot all pass a
// threshold that only counts completed failures: at most (threshold - failures) attempts
// per key are in flight at once.
func (l *Limiter) Begin(keys ...string) (*Attempt, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	ks := make([]string, len(keys))
	var wait time.Duration
	for i, k := range keys {
		k = clip(k)
		ks[i] = k
		e := l.m[k]
		if e == nil {
			continue
		}
		thr, forget := l.policy(k)
		if e.idle(now, forget) {
			e.fails = 0
		}
		switch {
		case e.locked(now):
			wait = max(wait, e.lockedUntil.Sub(now))
		case e.pending >= max(thr-e.fails, 1):
			wait = max(wait, l.Base)
		}
	}
	if wait > 0 {
		return nil, wait
	}
	for _, k := range ks {
		l.entryLocked(k, now).pending++
	}
	return &Attempt{l: l, keys: ks}, 0
}

// entryLocked returns the entry for k, creating it within the entry cap.
func (l *Limiter) entryLocked(k string, now time.Time) *entry {
	if e := l.m[k]; e != nil {
		return e
	}
	l.makeRoomLocked(now)
	e := &entry{}
	l.m[k] = e
	return e
}

func (a *Attempt) settle(fn func(k string, e *entry, now time.Time)) {
	if a == nil || a.done {
		return
	}
	a.done = true
	l := a.l
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for _, k := range a.keys {
		e := l.m[k]
		if e == nil {
			e = l.entryLocked(k, now) // evicted while pending
		}
		e.pending = max(e.pending-1, 0)
		fn(k, e, now)
	}
}

// Fail records a failed attempt on every key.
func (a *Attempt) Fail() {
	a.settle(func(k string, e *entry, now time.Time) { a.l.failLocked(k, e, now) })
}

// Success refunds the attempt and forgets the failures of resetKeys. Keys not listed
// keep their count (a success on one account must not erase the source's failures).
func (a *Attempt) Success(resetKeys ...string) {
	a.settle(func(k string, e *entry, _ time.Time) {
		if slices.Contains(resetKeys, k) {
			e.fails, e.lockedUntil = 0, time.Time{}
		}
	})
}

// Cancel refunds the attempt without a verdict (request cancelled, server busy, error).
func (a *Attempt) Cancel() { a.settle(func(string, *entry, time.Time) {}) }

func (l *Limiter) failLocked(k string, e *entry, now time.Time) {
	thr, forget := l.policy(k)
	if now.Sub(e.lastFail) > forget && !e.locked(now) {
		e.fails = 0
	}
	e.fails++
	e.lastFail = now
	if e.fails >= thr {
		shift := min(e.fails-thr, 20)
		e.lockedUntil = now.Add(min(l.Base<<shift, l.Max))
	}
}

// Fail records a failed attempt on every key.
func (l *Limiter) Fail(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for _, k := range keys {
		k = clip(k)
		l.failLocked(k, l.entryLocked(k, now), now)
	}
}

// Reset forgets the keys.
func (l *Limiter) Reset(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, k := range keys {
		delete(l.m, clip(k))
	}
}

// Prune drops stale entries.
func (l *Limiter) Prune() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pruneLocked(l.now())
}

func (l *Limiter) pruneLocked(now time.Time) {
	for k, e := range l.m {
		if _, forget := l.policy(k); e.idle(now, forget) {
			delete(l.m, k)
		}
	}
}

// makeRoomLocked guarantees space for one more entry: it prunes idle entries and, if the
// table is still full, evicts the least valuable tenth (unlocked and not in flight first,
// oldest first), so the table never exceeds its cap however many sources spray it.
func (l *Limiter) makeRoomLocked(now time.Time) {
	limit := l.capacity()
	if len(l.m) < limit {
		return
	}
	l.pruneLocked(now)
	if len(l.m) < limit {
		return
	}
	type kv struct {
		k        string
		protect  bool
		lastFail time.Time
	}
	all := make([]kv, 0, len(l.m))
	for k, e := range l.m {
		all = append(all, kv{k, e.locked(now) || e.pending > 0, e.lastFail})
	}
	slices.SortFunc(all, func(a, b kv) int {
		if a.protect != b.protect {
			if a.protect {
				return 1
			}
			return -1
		}
		return a.lastFail.Compare(b.lastFail)
	})
	drop := len(all) - limit*9/10
	for _, x := range all[:max(drop, 1)] {
		delete(l.m, x.k)
	}
}

// limiterIP maps a client address to its limiter identity: IPv4 addresses as is, IPv6
// addresses by their /64 (a single subscriber owns a whole /64, so per-address keys
// would be free to rotate), with any zone suffix removed.
func limiterIP(ip string) string {
	if i := strings.IndexByte(ip, '%'); i >= 0 {
		ip = ip[:i]
	}
	a := net.ParseIP(ip)
	if a == nil {
		return ip
	}
	if v4 := a.To4(); v4 != nil {
		return v4.String()
	}
	return a.Mask(net.CIDRMask(64, 128)).String() + "/64"
}

// ViaTrustedProxyHTTPS reports whether the direct peer is a trusted proxy that says the
// original request used https (X-Forwarded-Proto).
func ViaTrustedProxyHTTPS(r *http.Request, trusted []*net.IPNet) bool {
	if len(trusted) == 0 {
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer := net.ParseIP(host)
	if peer == nil || !inNets(peer, trusted) {
		return false
	}
	vals := r.Header.Values("X-Forwarded-Proto")
	if len(vals) == 0 {
		return false
	}
	parts := strings.Split(vals[len(vals)-1], ",")
	return strings.EqualFold(strings.TrimSpace(parts[len(parts)-1]), "https")
}

// ParseProxies parses trusted-proxy entries (CIDRs or bare IPs).
func ParseProxies(entries []string) ([]*net.IPNet, error) {
	var out []*net.IPNet
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if !strings.Contains(e, "/") {
			ip := net.ParseIP(e)
			if ip == nil {
				return nil, &net.ParseError{Type: "IP address", Text: e}
			}
			bits := 128
			if ip.To4() != nil {
				bits = 32
			}
			out = append(out, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
			continue
		}
		_, n, err := net.ParseCIDR(e)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

func inNets(ip net.IP, nets []*net.IPNet) bool {
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// ClientIP returns the request's source address. X-Forwarded-For is honoured only when the
// direct peer (r.RemoteAddr) is in trusted; then the right-most entry that is not itself a
// trusted proxy is used. Unparseable data falls back to the direct peer.
func ClientIP(r *http.Request, trusted []*net.IPNet) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer := net.ParseIP(host)
	if peer == nil || len(trusted) == 0 || !inNets(peer, trusted) {
		return host
	}
	var hops []string
	for _, v := range r.Header.Values("X-Forwarded-For") {
		hops = append(hops, strings.Split(v, ",")...)
	}
	for i := len(hops) - 1; i >= 0; i-- {
		ip := net.ParseIP(strings.TrimSpace(hops[i]))
		if ip == nil {
			return host
		}
		if !inNets(ip, trusted) {
			return ip.String()
		}
	}
	return host
}
