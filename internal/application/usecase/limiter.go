package usecase

import (
	"sync"
	"time"
)

// RegistrationLimiter is the RF3 rate-limit port: "un cliente nuevo se
// auto-registra en el canal por el que llegó (loop con rate-limit por
// identificador de canal)".
//
// The budget is per channel identifier (a phone or an e-mail address), never
// global and never per IP: the identity the gateway transports is the only
// handle the backend has on the sender, and a global bucket would let one noisy
// sender starve every other registration.
type RegistrationLimiter interface {
	// Allow reports whether a registration attempt for identifier may proceed.
	// A true answer consumes one unit of that identifier's budget; a false
	// answer consumes nothing.
	Allow(identifier string) bool
}

// DefaultRegistrationWindow is the window the product applies: one hour, the
// predecessor's per-phone window. The composition root (T6) wires the limit
// from the environment; the window stays a product decision.
const DefaultRegistrationWindow = time.Hour

const (
	// maxBuckets bounds the memory of the in-memory limiter. The product's
	// declared ceiling is 10k clients (PRD §7), so 100k live buckets leave an
	// order of magnitude of headroom for distinct senders inside one window.
	// Beyond it the limiter fails closed: an unbounded map keyed by a
	// caller-supplied value is a memory-growth vector, and refusing the excess
	// is the only answer that cannot be turned into a resource exhaustion.
	maxBuckets = 100_000

	// pruneThreshold is when expired buckets are swept. Sweeping on every
	// Allow would cost O(n) per call for no benefit; sweeping only past the
	// threshold keeps steady-state traffic allocation-free.
	pruneThreshold = 4_096
)

// MemoryRegistrationLimiter is the in-process implementation of the port: a
// fixed-window counter per identifier, mutex-guarded.
//
// Fixed window (not sliding) is deliberate: it costs one map entry and one
// timestamp per identifier, and its known weakness — a sender can spend a full
// budget at the end of one window and another at the start of the next — is
// bounded to 2× the limit for a loopback-only server whose caller-ids come from
// the gateway, not from the open internet. A sliding log would need O(limit)
// timestamps per identifier for a benefit no RF3 criterion observes.
//
// State is in process memory, so a restart resets every budget. That is
// acceptable (and documented) because the limiter protects the database from a
// runaway registration loop, not against a determined attacker with restart
// rights on the host.
type MemoryRegistrationLimiter struct {
	limit  int
	window time.Duration
	now    func() time.Time

	mu      sync.Mutex
	buckets map[string]*registrationBucket
}

// registrationBucket is the budget of one identifier in the current window.
type registrationBucket struct {
	count   int
	resetAt time.Time
}

// NewMemoryRegistrationLimiter wires the limiter with the product window.
//
// limit is the number of registration attempts one identifier may spend per
// window. Zero (or negative) means registration is DISABLED: Allow then always
// answers false. That is the fail-closed reading of "no limit configured" — an
// absent limit never means unlimited.
func NewMemoryRegistrationLimiter(limit int) *MemoryRegistrationLimiter {
	return NewMemoryRegistrationLimiterWithClock(limit, DefaultRegistrationWindow, time.Now)
}

// NewMemoryRegistrationLimiterWithClock is NewMemoryRegistrationLimiter with an
// explicit window and clock, for tests and for a caller that needs a different
// budget shape. A non-positive window falls back to DefaultRegistrationWindow
// and a nil clock to time.Now, so a partially configured limiter still bounds
// something instead of panicking.
func NewMemoryRegistrationLimiterWithClock(limit int, window time.Duration, now func() time.Time) *MemoryRegistrationLimiter {
	if window <= 0 {
		window = DefaultRegistrationWindow
	}
	if now == nil {
		now = time.Now
	}
	return &MemoryRegistrationLimiter{
		limit:   limit,
		window:  window,
		now:     now,
		buckets: make(map[string]*registrationBucket),
	}
}

// Allow implements RegistrationLimiter.
func (l *MemoryRegistrationLimiter) Allow(identifier string) bool {
	// Registration disabled, or an identifier that cannot be budgeted: no
	// budget, no admission. Both are fail-closed.
	if l.limit <= 0 || identifier == "" {
		return false
	}

	instant := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()

	bucket, known := l.buckets[identifier]
	if !known || !instant.Before(bucket.resetAt) {
		// A bucket is only carved when the map can still absorb one more
		// entry, so an exhausted map cannot be grown by trying new keys.
		if !known && len(l.buckets) >= maxBuckets {
			l.prune(instant)
			if len(l.buckets) >= maxBuckets {
				return false
			}
		}
		bucket = &registrationBucket{resetAt: instant.Add(l.window)}
		l.buckets[identifier] = bucket
	}

	if bucket.count >= l.limit {
		return false
	}
	bucket.count++
	l.prune(instant)
	return true
}

// prune drops expired buckets once the map has grown past the sweep threshold.
// The caller holds the lock.
func (l *MemoryRegistrationLimiter) prune(instant time.Time) {
	if len(l.buckets) < pruneThreshold {
		return
	}
	for identifier, bucket := range l.buckets {
		if !instant.Before(bucket.resetAt) {
			delete(l.buckets, identifier)
		}
	}
}
