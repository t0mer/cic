package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

// Pending second-factor challenges. A challenge is created after a correct password for a
// user with two-factor login on; it is not a session and grants nothing but the right to
// try a second factor for that user.
const (
	mfaTTL        = 5 * time.Minute
	mfaMaxTries   = 5
	mfaMaxPending = 10_000
	mfaMaxPerUser = 5
	mfaTokenBytes = 32
)

// errMFAFull is returned by issue when the global cap is reached by live challenges.
var errMFAFull = errors.New("too many pending second-factor logins")

type challenge struct {
	userID  int64
	expires time.Time
	created time.Time
	tries   int
}

// mfaStore holds challenges in memory, keyed by the SHA-256 of the token (the token itself
// is never kept). They do not survive a restart, which only means signing in again.
type mfaStore struct {
	mu  sync.Mutex
	now func() time.Time
	m   map[string]*challenge
}

func newMFAStore(now func() time.Time) *mfaStore {
	return &mfaStore{now: now, m: map[string]*challenge{}}
}

func mfaKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// issue creates a challenge for userID and returns its token. A user keeps at most
// mfaMaxPerUser live challenges: a newer one drops that user's oldest, so one account can
// never push out another's. At the global cap (after pruning expired ones) it refuses with
// errMFAFull rather than evicting anybody.
func (m *mfaStore) issue(userID int64) (string, error) {
	b := make([]byte, mfaTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.m) >= mfaMaxPending {
		m.pruneLocked(now)
	}
	n := 0
	var oldK string
	var oldT time.Time
	for k, c := range m.m {
		if c.userID != userID {
			continue
		}
		n++
		if oldK == "" || c.created.Before(oldT) {
			oldK, oldT = k, c.created
		}
	}
	if n >= mfaMaxPerUser {
		delete(m.m, oldK)
	} else if len(m.m) >= mfaMaxPending {
		return "", errMFAFull
	}
	m.m[mfaKey(token)] = &challenge{userID: userID, created: now, expires: now.Add(mfaTTL)}
	return token, nil
}

// peek returns the user of a live challenge.
func (m *mfaStore) peek(token string) (int64, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.liveLocked(mfaKey(token))
	if c == nil {
		return 0, false
	}
	return c.userID, true
}

func (m *mfaStore) liveLocked(k string) *challenge {
	c := m.m[k]
	if c == nil {
		return nil
	}
	if !m.now().Before(c.expires) {
		delete(m.m, k)
		return nil
	}
	return c
}

// try reserves one guess. It reports false (and deletes the challenge) when the token is
// unknown, expired or out of tries.
func (m *mfaStore) try(token string) bool {
	k := mfaKey(token)
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.liveLocked(k)
	if c == nil {
		return false
	}
	if c.tries >= mfaMaxTries {
		delete(m.m, k)
		return false
	}
	c.tries++
	return true
}

// refund gives back a guess that never reached a verdict (server error).
func (m *mfaStore) refund(token string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c := m.m[mfaKey(token)]; c != nil && c.tries > 0 {
		c.tries--
	}
}

// exhausted deletes the challenge once its tries are used up.
func (m *mfaStore) dropIfSpent(token string) {
	k := mfaKey(token)
	m.mu.Lock()
	defer m.mu.Unlock()
	if c := m.m[k]; c != nil && c.tries >= mfaMaxTries {
		delete(m.m, k)
	}
}

// take consumes the challenge; it reports false when it was already gone (single use).
func (m *mfaStore) take(token string) bool {
	k := mfaKey(token)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.liveLocked(k) == nil {
		return false
	}
	delete(m.m, k)
	return true
}

func (m *mfaStore) drop(token string) {
	m.mu.Lock()
	delete(m.m, mfaKey(token))
	m.mu.Unlock()
}

func (m *mfaStore) pruneLocked(now time.Time) {
	for k, c := range m.m {
		if !now.Before(c.expires) {
			delete(m.m, k)
		}
	}
}

func (m *mfaStore) prune() {
	m.mu.Lock()
	m.pruneLocked(m.now())
	m.mu.Unlock()
}
