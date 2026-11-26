package auth

import (
	"context"
	"errors"
	"fmt"
)

// CallerResolver applies the authorization chain of PRD §3.8 to an incoming
// caller-id and produces the Caller a request is attributed to.
//
// The chain, in order, fail-closed at every step:
//
//  1. empty caller-id            → ErrMissingCallerID (before any lookup)
//  2. no row for the caller-id   → ErrUnknownCaller
//  3. row with an invalid role   → internal error (corrupt data, never grants)
//  4. row without an internal id → internal error (corrupt row)
//  5. disabled account           → ErrAccountDisabled
//  6. any live row               → Caller with the row's role
//
// Steps 3 and 4 exist because a database is an input too: an unrecognized role
// or an unidentifiable row is evidence of corruption, not of permission, and
// must never be upgraded into access.
type CallerResolver struct {
	lookup CallerLookup
}

// NewCallerResolver wires the resolver to its lookup port. A nil lookup is a
// fail-closed configuration error: without a database of identities the
// resolver could only reject, so it refuses to exist instead of pretending.
func NewCallerResolver(lookup CallerLookup) (*CallerResolver, error) {
	if lookup == nil {
		return nil, errors.New("resolver de identidad: se requiere un CallerLookup")
	}
	return &CallerResolver{lookup: lookup}, nil
}

// Resolve resolves callerID — the X-Caller-Id header exactly as received, with
// no trimming, no case folding and no caching across senders — into a Caller.
//
// The returned error is one of the *CallerError sentinels for semantic
// failures; any other error is internal and must be collapsed by the transport.
func (r *CallerResolver) Resolve(ctx context.Context, callerID string) (Caller, error) {
	if callerID == "" {
		return Caller{}, ErrMissingCallerID
	}

	identity, err := r.lookup.LookupCaller(ctx, callerID)
	if err != nil {
		if errors.Is(err, ErrCallerNotFound) {
			return Caller{}, ErrUnknownCaller
		}
		return Caller{}, fmt.Errorf("resolver de identidad: %w", err)
	}

	if !identity.Role.Valid() {
		return Caller{}, fmt.Errorf("resolver de identidad: rol desconocido %q", string(identity.Role))
	}
	if identity.ID == "" {
		return Caller{}, errors.New("resolver de identidad: la fila resuelta no tiene id interno")
	}
	if !identity.Enabled {
		return Caller{}, ErrAccountDisabled
	}

	return Caller{ID: identity.ID, Name: identity.Name, Role: identity.Role}, nil
}
