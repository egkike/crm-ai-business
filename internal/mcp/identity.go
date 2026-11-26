package mcp

import (
	"context"
	"errors"

	"github.com/egkike/crm-ai-business/internal/auth"
)

// identityState is the outcome of the §3.8 chain for one request. It is a
// three-valued state, not a boolean: "we know who this is", "we know this is not
// a caller yet" and "we could not ask" demand different answers, and collapsing
// them would either leak an internal failure or pretend an outage is a
// registration opportunity.
type identityState int

const (
	// identityResolved: the resolver recognized the caller-id and assigned a
	// role from the database.
	identityResolved identityState = iota

	// identityRejected: the chain failed semantically (no header, no row,
	// disabled account). This is the ANONYMOUS principal: it may reach the tools
	// that declare Access.Anonymous — see admitsAnonymousTool, which excludes the
	// one rejection that has no identifier to register at all.
	identityRejected

	// identityFailed: the chain could not run (database unavailable, corrupt
	// row). Nothing may be executed and nothing may be announced.
	identityFailed
)

// identity is the per-request resolution result. It carries the caller-id
// VERBATIM regardless of outcome, because the anonymous registration seam feeds
// it back to the use case unchanged: a padded phone is a different phone to the
// database, and normalizing it here would make the resolver and the registration
// seam disagree about who the sender is.
type identity struct {
	state    identityState
	callerID string
	caller   auth.Caller
	// rejection is the typed semantic failure when state is identityRejected.
	rejection *auth.CallerError
}

// identityContextKey keys the identity in the request context. A private
// zero-size type: no other package can inject an identity, so a tool can never
// be handed one that did not come from the resolver.
type identityContextKey struct{}

// withIdentity attaches the resolved identity to the request context. The SDK
// passes the HTTP request context down to tool handlers, which is why this is
// the only channel the caller needs.
func withIdentity(ctx context.Context, ident *identity) context.Context {
	return context.WithValue(ctx, identityContextKey{}, ident)
}

// identityFromContext returns the identity the middleware resolved.
func identityFromContext(ctx context.Context) (*identity, bool) {
	ident, ok := ctx.Value(identityContextKey{}).(*identity)
	if !ok || ident == nil {
		return nil, false
	}
	return ident, true
}

// CallerFromContext returns the resolved caller of the current request and
// whether the request had one.
//
// Tools use it for the role-dependent business that the RBAC gate already
// permitted; false means the caller is an anonymous sender (register_client) or
// that the request carried no identity at all, and a tool must treat that as
// fail-closed.
func CallerFromContext(ctx context.Context) (auth.Caller, bool) {
	ident, ok := identityFromContext(ctx)
	if !ok || ident.state != identityResolved {
		return auth.Caller{}, false
	}
	return ident.caller, true
}

// CallerIDFromContext returns the X-Caller-Id of the current request EXACTLY as
// it arrived, and whether the middleware recorded one at all.
//
// It is the only identity input of the anonymous registration seam: the value
// goes to the use case untouched, which decides — not the transport — whether it
// can become a natural identifier.
func CallerIDFromContext(ctx context.Context) (string, bool) {
	ident, ok := identityFromContext(ctx)
	if !ok {
		return "", false
	}
	return ident.callerID, true
}

// admitsAnonymousTool reports whether this identity may reach a tool that
// declares Access.Anonymous.
//
// Only one of the rejection classes qualifies. A request with no X-Caller-Id at
// all has no identifier to register, so it is not an anonymous sender but a
// broken gateway: it answers -32000 and executes nothing, which is exactly what
// RF2's acceptance criterion asks for ("responde error JSON-RPC -32000 sin
// ejecutar nada"). Unknown and disabled callers, by contrast, are senders that
// exist on the channel and can only move forward through registration.
func (i *identity) admitsAnonymousTool() bool {
	if i == nil || i.state != identityRejected {
		return false
	}
	return i.rejection == nil || i.rejection.Code() != auth.CodeMissingCallerID
}

// resolveIdentity runs the §3.8 chain for one caller-id and classifies the
// outcome. It is the only place this package turns an auth error into an
// identityState, so the classification cannot drift between the tools/list gate
// and the tools/call gate.
func resolveIdentity(ctx context.Context, resolver CallerResolver, callerID string) *identity {
	caller, err := resolver.Resolve(ctx, callerID)
	if err == nil {
		return &identity{state: identityResolved, callerID: callerID, caller: caller}
	}

	var rejection *auth.CallerError
	if errors.As(err, &rejection) {
		return &identity{state: identityRejected, callerID: callerID, rejection: rejection}
	}

	// Everything else — a driver failure, a corrupt row, a wrapped cause — is
	// internal and must not be rendered. It is dropped here rather than logged:
	// this layer has no logger, and the caller-id it would have to log is PII
	// (AGENTS.md: never log tokens, passwords or raw PII).
	return &identity{state: identityFailed, callerID: callerID}
}
