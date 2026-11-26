// Package mcp is the MCP transport adapter of the product (AGENTS.md layer map,
// "transport adapters"): Streamable HTTP over the official Go SDK, the
// X-Caller-Id identity middleware and the role-gated tool registry behind it.
//
// # The HTTP edge (RF1, F1 T2 contract)
//
// The composition root mounts the handler returned by NewHandler at /mcp on
// 127.0.0.1:3001. The handler is POST-only: every other method answers 405 with
// `Allow: POST` before anything else runs, whatever the SDK version does
// underneath. Binding the loopback address is the composition root's job — this
// package never opens a listener.
//
// The MCP protocol is served by the SDK's stateless Streamable HTTP path
// (spec 2025-11-25): no Mcp-Session-Id, no server-initiated streams, responses
// as application/json instead of text/event-stream. Statelessness is not an
// optimization here, it is the security shape: the product has no per-connection
// state that could outlive a sender, so identity cannot leak from one request
// into the next (PRD §3.8: verbatim, per request, per sender).
//
// # Identity (RF2, PRD §3.8)
//
// Every request carries X-Caller-Id. The header is read VERBATIM — no trimming,
// no case folding — and resolved through auth.CallerResolver, the single path
// that turns an identifier into a role. The resolved auth.Caller, never a
// caller-supplied hint, travels with the request context; tools read it with
// CallerFromContext. Unresolved, unknown or disabled identities do not abort the
// request: they are granted the ANONYMOUS principal, which may only see and
// invoke the tools that declare it (today exactly one: register_client, the
// RF3 registration seam).
//
// # The sanitization boundary (AGENTS.md boundary contract 2)
//
// This package is where an error stops being an error and becomes a sentence a
// human or an LLM may read. Four classes cross, and nothing else does:
//
//   - *auth.CallerError → JSON-RPC -32000, its Spanish Message verbatim, and its
//     Code() in `data.code` so the caller can branch without parsing prose.
//   - *usecase.SemanticError → JSON-RPC -32002, its Spanish Message verbatim and
//     its Code() in `data.code`.
//   - a role denial → JSON-RPC -32000 with data.code "role_not_allowed".
//   - invalid tool arguments → JSON-RPC -32602 with data.code
//     "invalid_arguments".
//
// EVERY other error — SQL, driver, filesystem, an unrecovered bug turned into a
// recovered panic — collapses to JSON-RPC -32603 carrying nothing but "error
// interno del servidor". Internals never reach the wire (backlog R3-001: the
// collapse used to be documentation only; this package makes it executable).
//
// # RBAC (RF2)
//
// Each tool declares its Access — the resolved roles that may invoke it plus
// whether the anonymous principal may. tools/list answers only the tools the
// principal may invoke, and a tool named directly anyway is refused with a
// JSON-RPC error before its handler runs: hiding a tool is presentation, the
// gate is the control. register_client is the sole documented exception to
// double RBAC (AGENTS.md boundary contract 1, F1 T5): it declares Anonymous
// because the sender by definition has no row yet; its compensating controls
// live in the use case, not here.
package mcp
