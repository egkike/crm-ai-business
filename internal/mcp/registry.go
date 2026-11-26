package mcp

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	gomcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/egkike/crm-ai-business/internal/auth"
)

// The MCP protocol methods this transport gates. They are protocol wire
// strings, not SDK implementation details: the SDK routes by these names, and
// they are frozen by the specification.
const (
	methodListTools = "tools/list"
	methodCallTool  = "tools/call"
)

// Access declares which principals may see and invoke a tool (RF2's role-based
// tools/list, PRD §3.8).
//
// The zero value admits NOBODY. That is deliberate: a tool whose author forgot
// to declare its access disappears from every tools/list and refuses every call,
// which is a visible bug — the opposite default (public by omission) would be a
// silent authorization hole.
type Access struct {
	// Anonymous admits callers whose identity did not resolve: the
	// register_client seam, and only it. A tool that needs a caller-id must
	// still handle its absence itself (register_client rejects an empty one
	// through the use case).
	Anonymous bool

	// Roles are the resolved roles that may use the tool. A role outside
	// auth.Role.Valid() (the zero value included) is never granted.
	Roles []auth.Role
}

// allows reports whether a resolved role may use the tool.
func (a Access) allows(role auth.Role) bool {
	if !role.Valid() {
		return false
	}
	return slices.Contains(a.Roles, role)
}

// Tool is one tool the transport exposes, together with the authorization
// declaration that decides who may see it.
//
// Definition is the wire schema (name, description, input/output JSON Schema)
// and Handler is the typed tool handler: new tools arrive by declaring both,
// which is exactly the AGENTS.md rule that wire shapes live in the transport
// adapter.
type Tool struct {
	// Definition is the tool as announced by tools/list.
	Definition *gomcp.Tool

	// Handler runs the tool. Its errors are sanitized by the transport (see
	// sanitize): returning a *usecase.SemanticError, an *auth.CallerError or a
	// *jsonrpc.Error is how a tool chooses its wire class; anything else becomes
	// the generic internal error.
	Handler gomcp.ToolHandler

	// Access decides visibility and invocation.
	Access Access
}

// registry is the role-gated tool set: the single source of truth for both what
// tools/list announces and what tools/call executes.
//
// It exists so those two answers cannot disagree. Filtering the list is
// presentation; gating the call is the control; both read the same Access
// declaration.
type registry struct {
	tools  []Tool
	byName map[string]Tool
}

// newRegistry validates and indexes the tool set. Every failure here is a
// wiring bug that would otherwise surface as a missing or duplicated tool at
// runtime, so it refuses to build instead of guessing.
func newRegistry(tools []Tool) (*registry, error) {
	reg := &registry{
		tools:  make([]Tool, 0, len(tools)),
		byName: make(map[string]Tool, len(tools)),
	}
	for _, tool := range tools {
		if tool.Definition == nil {
			return nil, errors.New("transporte MCP: una herramienta no tiene definición")
		}
		name := tool.Definition.Name
		if name == "" {
			return nil, errors.New("transporte MCP: una herramienta no tiene nombre")
		}
		if tool.Handler == nil {
			return nil, fmt.Errorf("transporte MCP: la herramienta %q no tiene handler", name)
		}
		// The SDK panics on a nil input schema instead of quietly accepting every
		// input, so the omission is caught here, where it becomes a startup error
		// a human can read. Malformed schemas past this point are the SDK's own
		// AddTool validation (it requires a JSON object with type "object").
		if tool.Definition.InputSchema == nil {
			return nil, fmt.Errorf("transporte MCP: la herramienta %q no declara inputSchema", name)
		}
		if _, duplicate := reg.byName[name]; duplicate {
			return nil, fmt.Errorf("transporte MCP: la herramienta %q está registrada dos veces", name)
		}
		reg.tools = append(reg.tools, tool)
		reg.byName[name] = tool
	}
	return reg, nil
}

// gate is the receiving middleware that enforces the registry on both gated
// methods. It runs before the SDK's own method handlers, so a refusal never
// reaches a tool.
//
// Non-gated methods (initialize, ping, notifications) pass through: they carry
// no business authority, and initialize must answer for an anonymous sender —
// that is how the gateway discovers register_client in the first place.
func (r *registry) gate(next gomcp.MethodHandler) gomcp.MethodHandler {
	return func(ctx context.Context, method string, req gomcp.Request) (gomcp.Result, error) {
		ident, hasIdentity := identityFromContext(ctx)

		switch method {
		case methodListTools:
			// An identity we could not even resolve is not an anonymous
			// sender: nothing may be announced, because the announcement itself
			// would be speculation.
			if !hasIdentity || ident.state == identityFailed {
				return nil, internalFailure()
			}
			res, err := next(ctx, method, req)
			if err != nil {
				return nil, err
			}
			if list, ok := res.(*gomcp.ListToolsResult); ok {
				list.Tools = r.visible(ident, list.Tools)
				// The SDK defaults the scope to "public", which on a role-filtered
				// list would be a lie: what this caller sees is not what another
				// caller sees, so no intermediary may reuse it. The list is
				// per-caller by construction (RF2).
				list.CacheScope = "private"
			}
			return res, nil

		case methodCallTool:
			call, ok := req.(*gomcp.CallToolRequest)
			if !ok || call.Params == nil {
				// Malformed request: let the SDK answer it in the standard way.
				return next(ctx, method, req)
			}
			if refusal := r.authorize(ident, hasIdentity, call.Params.Name); refusal != nil {
				return nil, refusal
			}
			return next(ctx, method, req)
		}

		return next(ctx, method, req)
	}
}

// authorize is the fail-closed gate of tools/call. It returns the refusal to
// put on the wire, or nil when the call may proceed.
func (r *registry) authorize(ident *identity, hasIdentity bool, name string) *jsonrpc.Error {
	if !hasIdentity {
		// No identity in the context means the middleware did not run: a
		// wiring bug, never a permission.
		return internalFailure()
	}
	if ident.state == identityFailed {
		return internalFailure()
	}

	tool, known := r.byName[name]

	if ident.state == identityRejected {
		// The anonymous principal: the ONLY way through is a tool that
		// declares Anonymous AND a caller-id that could be registered at all.
		// Everything else — a missing header, a tool that does not exist, a
		// tool that needs a role — answers with the identity failure, so an
		// unknown sender can never probe the tool set and RF2's "-32000 without
		// executing anything" holds for any tool name.
		if ident.rejection == nil {
			// Unreachable by construction; never a reason to answer -32000 with a
			// nil message.
			return internalFailure()
		}
		if known && tool.Access.Anonymous && ident.admitsAnonymousTool() {
			return nil
		}
		return callerRejected(ident.rejection)
	}

	if !known {
		// Unknown tool with a resolved identity: the SDK's standard -32602,
		// produced by falling through to its handler.
		return nil
	}
	if !tool.Access.allows(ident.caller.Role) {
		return roleDenied()
	}
	return nil
}

// visible filters the announced tools down to the ones the principal may
// invoke.
func (r *registry) visible(ident *identity, listed []*gomcp.Tool) []*gomcp.Tool {
	visible := make([]*gomcp.Tool, 0, len(listed))
	for _, definition := range listed {
		if definition == nil {
			continue
		}
		tool, known := r.byName[definition.Name]
		if !known {
			// Not ours: never announce what we cannot gate.
			continue
		}
		if ident.state == identityResolved {
			if tool.Access.allows(ident.caller.Role) {
				visible = append(visible, definition)
			}
			continue
		}
		if tool.Access.Anonymous && ident.admitsAnonymousTool() {
			visible = append(visible, definition)
		}
	}
	return visible
}

// toolHandler adapts a registry entry into the handler the SDK invokes: the
// sanitization boundary plus panic recovery, in one place, so no tool can
// forget either.
func toolHandler(handler gomcp.ToolHandler) gomcp.ToolHandler {
	return func(ctx context.Context, req *gomcp.CallToolRequest) (result *gomcp.CallToolResult, err error) {
		// A panic is a bug, and a bug must not become a stack trace on the
		// wire. Recovering here keeps the process serving other senders while
		// the caller gets the same generic failure as any internal error.
		defer func() {
			if recovered := recover(); recovered != nil {
				result, err = nil, internalFailure()
			}
		}()

		result, err = handler(ctx, req)
		if err != nil {
			return nil, sanitize(err)
		}
		return result, nil
	}
}
