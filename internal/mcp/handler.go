package mcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	gomcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/egkike/crm-ai-business/internal/application/dto"
	"github.com/egkike/crm-ai-business/internal/auth"
)

const (
	// ServerName and ServerVersion identify this server in the MCP initialize
	// handshake.
	ServerName    = "crm-ai-business"
	ServerVersion = "0.1.0"

	// CallerIDHeader is the per-request identity header of PRD §3.8. Its value
	// is read verbatim and never normalized: the header transports an identity,
	// it never transports a role.
	CallerIDHeader = "X-Caller-Id"
)

// CallerResolver is the driven port of identity resolution (satisfied by
// *auth.CallerResolver). The transport depends on the port, not the adapter, so
// the §3.8 chain stays independently testable.
type CallerResolver interface {
	// Resolve turns a verbatim caller-id into a Caller, or returns an
	// *auth.CallerError for a semantic identity failure and any other error for
	// an internal one.
	Resolve(ctx context.Context, callerID string) (auth.Caller, error)
}

// RegisterClient is the anonymous registration seam (satisfied by
// *usecase.RegisterClient). It takes the caller-id, never a role: there is no
// parameter here through which a caller could claim one.
type RegisterClient interface {
	Execute(ctx context.Context, cmd dto.RegisterClientCommand) (dto.RegisterClientResult, error)
}

// Dependencies are the ports the MCP transport needs to exist.
//
// Both are required: a handler without a resolver could not identify anyone and
// a handler without the registration seam could not serve the unknown senders
// the §3.8 chain rejects by design. NewHandler refuses a nil port instead of
// building a server that fails per request.
type Dependencies struct {
	// CallerResolver resolves X-Caller-Id (PRD §3.8).
	CallerResolver CallerResolver

	// RegisterClient is the F1 registration use case behind the register_client
	// tool.
	RegisterClient RegisterClient

	// Catalog carries the three RF4 use cases (search, create, update). Nil
	// means "this handler has no catalog tools"; a partially filled Catalog is
	// a construction error, never a degraded server.
	Catalog *CatalogDependencies

	// Tickets carries the five RF5 use cases (create, my list, internal list,
	// note, transition). Nil means "this handler has no ticket tools"; a
	// partially filled Tickets is a construction error, never a degraded server.
	Tickets *TicketDependencies
}

// NewHandler builds the /mcp handler with the F1 tool set.
//
// The composition root mounts it on the loopback listener; this package never
// binds an address. The returned handler is POST-only and answers 405 with
// `Allow: POST` otherwise (the T2 contract of RF1), which is enforced here
// independently of the SDK's own behavior.
func NewHandler(deps Dependencies) (http.Handler, error) {
	return NewHandlerWithTools(deps)
}

// NewHandlerWithTools builds the /mcp handler with the F1 tool set plus extra
// tools (the extension point later phases use to declare their tools and their
// Access).
//
// The default set is register_client plus, when the matching Dependencies field is
// wired, the three RF4 catalog tools and the five RF5 ticket tools; the
// composition root decides which surfaces exist in this handler, and the registry
// decides who may see each tool. Tools are validated as a whole: a nil
// definition, a nil handler, a partially wired catalog or ticket set, or a
// duplicate name refuses construction, so a wiring mistake cannot silently shadow
// a tool. The F1 core tool (register_client) is always present and cannot be
// replaced.
func NewHandlerWithTools(deps Dependencies, extra ...Tool) (http.Handler, error) {
	if deps.CallerResolver == nil {
		return nil, errors.New("transporte MCP: se requiere un CallerResolver")
	}
	if deps.RegisterClient == nil {
		return nil, errors.New("transporte MCP: se requiere el caso de uso RegisterClient")
	}

	catalogTools, err := newCatalogTools(deps.Catalog)
	if err != nil {
		return nil, err
	}
	ticketTools, err := newTicketTools(deps.Tickets)
	if err != nil {
		return nil, err
	}

	tools := make([]Tool, 0, len(extra)+1+len(catalogTools)+len(ticketTools))
	tools = append(tools, registerClientTool(deps.RegisterClient))
	tools = append(tools, catalogTools...)
	tools = append(tools, ticketTools...)

	for _, tool := range extra {
		// The anonymous principal exists for ONE reason: the RF3 registration
		// seam that admits a sender with no row yet (AGENTS.md boundary contract
		// 1, the sole documented RBAC exception). A second anonymous tool is a
		// design change with its own compensating controls, not a field flip, so
		// the constructor refuses it instead of quietly widening the surface.
		if tool.Access.Anonymous {
			name := ""
			if tool.Definition != nil {
				name = tool.Definition.Name
			}
			return nil, fmt.Errorf("transporte MCP: la herramienta %q declara acceso anónimo; solo register_client puede hacerlo", name)
		}
	}
	tools = append(tools, extra...)

	reg, err := newRegistry(tools)
	if err != nil {
		return nil, err
	}

	return &handler{resolver: deps.CallerResolver, sdk: newSDKHandler(reg)}, nil
}

// handler is the /mcp HTTP edge: the POST-only contract and the identity
// middleware, with the SDK serving the protocol behind them.
type handler struct {
	resolver CallerResolver
	sdk      http.Handler
}

// ServeHTTP is the whole transport in three steps: refuse non-POST, resolve the
// caller-id, serve the protocol with the resolved identity in the context.
//
// The identity is resolved BEFORE the protocol handler runs, which is what makes
// RF2's "header ausente → rejected before any handler" true at the protocol
// level: by the time the SDK dispatches tools/call or tools/list, the identity
// state is already fixed and cannot be influenced by request content.
func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		// RFC 9110 §15.5.6: a 405 response MUST carry Allow. RF1 pins POST-only.
		w.Header().Set("Allow", http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	// Header.Get returns the first value verbatim: no trimming, no case folding,
	// no normalization of any kind (PRD §3.8). The value is read exactly once and
	// carried in the identity, so the resolver and the registration seam can never
	// disagree about who the sender is — not even when a request carries the
	// header twice.
	callerID := r.Header.Get(CallerIDHeader)

	h.sdk.ServeHTTP(w, r.WithContext(withIdentity(r.Context(), resolveIdentity(r.Context(), h.resolver, callerID))))
}

// newSDKHandler wires the registry into the SDK's Streamable HTTP server.
//
// Options, and why each one is a decision rather than a default:
//
//   - Stateless: no Mcp-Session-Id and no session state between requests. The
//     product's identity is per-request by construction (PRD §3.8), so a session
//     would be the one place a sender could inherit another's authority. It also
//     makes the endpoint trivially safe to expose behind a message gateway.
//   - JSONResponse: replies as application/json instead of text/event-stream
//     (spec §2.1.5 allows it). There is nothing to stream — every tool answers
//     within the request — and a bounded JSON reply is what an HTTP gateway
//     proxies without a streaming client.
//   - No logger: the SDK logger would receive request metadata, and the only
//     identifiers this layer holds are PII (phones, e-mails). Internal failures
//     are therefore silent here by deliberate choice, not by omission.
//
// Capabilities are declared instead of inherited: the SDK's default advertises a
// logging capability this server does not implement, and its tools capability
// would claim listChanged although the tool set is fixed at construction. This
// server announces exactly what it does — tools, nothing else, no change
// notifications.
//
// The single server instance is intentional: the caller is not a server
// property, it is a request property, so the registry gate reads it from the
// request context rather than from a per-caller server.
func newSDKHandler(reg *registry) http.Handler {
	server := gomcp.NewServer(&gomcp.Implementation{
		Name:        ServerName,
		Title:       "CRM-AI-Business",
		Description: "CRM, tickets y reservas del negocio para el agente conversacional Hermes.",
		Version:     ServerVersion,
	}, &gomcp.ServerOptions{
		Capabilities: &gomcp.ServerCapabilities{Tools: &gomcp.ToolCapabilities{}},
	})

	for _, tool := range reg.tools {
		server.AddTool(tool.Definition, toolHandler(tool.Handler))
	}

	// Registered last, so it wraps everything the server already installed and
	// runs first for tools/list and tools/call.
	server.AddReceivingMiddleware(reg.gate)

	return gomcp.NewStreamableHTTPHandler(
		func(*http.Request) *gomcp.Server { return server },
		&gomcp.StreamableHTTPOptions{
			Stateless:    true,
			JSONResponse: true,
		},
	)
}
