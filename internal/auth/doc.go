// Package auth owns caller identity and authorization for the CRM-AI-Business
// backend (PRD §3.8, RF2).
//
// The frozen principles this package implements:
//
//   - The channel transports an identity, never a role. The gateway reads the
//     sender from each message and injects X-Caller-Id verbatim, per request,
//     per sender. There is no shared state between senders and no
//     normalization: the resolver never trims, lowercases or otherwise rewrites
//     the caller-id.
//   - The role comes only from the database: an `accounts` row (owner, admin,
//     staff) or a `clients` row (client). No row means rejection.
//   - Anti-injection is structural. No exported API of this package accepts a
//     role, a "mode", or any caller-supplied authorization hint: the only role
//     input is the row a CallerLookup adapter returned.
//   - Fail-closed. Missing identity, unknown identity, disabled account, an
//     unrecognized role or a corrupt (id-less) row are all rejections. There is
//     no benefit of the doubt.
//
// Layer rules (AGENTS.md, hexagonal enforcement contract): this package is a
// shared edge. It holds the domain-facing port (CallerLookup) and the pure
// decision logic of the §3.8 chain. It contains no SQL and no transport
// concerns: the SQLite adapter arrives with T4 and the JSON-RPC boundary
// (-32000) with T6, which consumes the typed CallerError values defined here.
package auth
