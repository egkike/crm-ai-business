package auth

import (
	"context"
	"errors"
)

// ErrCallerNotFound is the only sentinel a CallerLookup adapter returns: the
// caller-id matched neither an `accounts` nor a `clients` row. Adapters may
// wrap it; the resolver matches it with errors.Is.
//
// The distinction matters: "no row" is a semantic, user-facing outcome
// (registration loop / unknown caller), while a database failure is internal
// and must not be rendered to the caller.
var ErrCallerNotFound = errors.New("caller no encontrado")

// Identity is the single resolved row a CallerLookup found for a caller-id.
//
// It is a carrier, not an authorization decision: the adapter's only job is to
// fetch the row and report what it says. Every branch of the decision chain
// lives in CallerResolver.
type Identity struct {
	// ID is the internal UUID primary key of the row (accounts.id or
	// clients.id). Required: a row without an identity is corrupt.
	ID string

	// Name is the display name of the row. Informational only.
	Name string

	// Role is the role the row carries — from accounts.role for internal rows,
	// and RoleClient for a clients row. It is never supplied by the caller.
	Role Role

	// Enabled mirrors `accounts.is_active`. A `clients` row has no such flag
	// and an adapter must report it as enabled. Adapters that fail to set it
	// cause a fail-closed rejection, never an implicit grant.
	Enabled bool
}

// CallerLookup is the driven port of identity resolution: one lookup by the
// verbatim caller-id (the X-Caller-Id header exactly as received), returning
// the matching row or ErrCallerNotFound.
//
// The signature is the anti-injection guarantee: there is no parameter — and
// no implementation may add one — through which the caller could hint, claim
// or override a role. The implementation lands with T4 (SQLite: exact match on
// clients.phone from WhatsApp, case-insensitive match on clients.email from
// e-mail, then accounts).
type CallerLookup interface {
	LookupCaller(ctx context.Context, callerID string) (Identity, error)
}
