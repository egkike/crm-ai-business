package auth

import (
	"context"
	"sync"
)

// memLookup is the in-memory CallerLookup used by the package tests: a map
// from the verbatim caller-id to the row it resolves to.
//
// It records every lookup (id and context) so tests can prove the resolver did
// not query when it should not have, did not normalize the id, and did not
// cache one sender's answer for another. It is safe for concurrent use because
// the per-request contract is exercised concurrently.
type memLookup struct {
	mu   sync.Mutex
	rows map[string]Identity
	err  error
	ids  []string
	ctxs []context.Context
}

// newMemLookup builds a lookup over the given rows. A nil map is an empty
// lookup: every caller-id is unknown.
func newMemLookup(rows map[string]Identity) *memLookup {
	if rows == nil {
		rows = map[string]Identity{}
	}
	return &memLookup{rows: rows}
}

// LookupCaller implements CallerLookup.
func (m *memLookup) LookupCaller(ctx context.Context, callerID string) (Identity, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.ids = append(m.ids, callerID)
	m.ctxs = append(m.ctxs, ctx)

	if m.err != nil {
		return Identity{}, m.err
	}
	row, ok := m.rows[callerID]
	if !ok {
		return Identity{}, ErrCallerNotFound
	}
	return row, nil
}

// failWith primes the lookup to answer every call with err, simulating an
// infrastructure failure (database down, timeout).
func (m *memLookup) failWith(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.err = err
}

// callCount reports how many lookups were performed.
func (m *memLookup) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.ids)
}

// seenIDs returns a copy of the caller-ids the lookup received, in order.
func (m *memLookup) seenIDs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.ids...)
}

// seenContexts returns a copy of the contexts the lookup received, in order.
func (m *memLookup) seenContexts() []context.Context {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]context.Context(nil), m.ctxs...)
}
