package mcp

import (
	"encoding/json"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"

	"github.com/egkike/crm-ai-business/internal/application/usecase"
	"github.com/egkike/crm-ai-business/internal/auth"
)

// The JSON-RPC error codes of this transport.
//
// -32602 and -32603 are the standard codes the SDK owns. -32000 and -32002 are
// application codes inside the reserved server-error range (-32000..-32099):
// -32000 is pinned by RF2's acceptance criteria ("responde error JSON-RPC
// -32000 sin ejecutar nada") for every authorization failure, and -32002 is its
// sibling for business rejections, both defined so a caller can tell "I am not
// allowed" apart from "the business said no" without parsing Spanish prose.
const (
	// codeAuthorizationRejected marks an identity or RBAC failure: unknown
	// caller, missing header, disabled account, or a role that may not use the
	// tool. The finer class travels in data.code.
	codeAuthorizationRejected int64 = -32000

	// codeSemanticRejected marks a business rejection the use case already
	// phrased for a human (RF3 collisions, rate limit, invalid identifier).
	codeSemanticRejected int64 = -32002
)

// The messages this package owns. Everything else is quoted from the layer that
// phrased it (auth.CallerError.Message, usecase.SemanticError.Message) — never
// paraphrased, never translated, never reworded (AGENTS.md: user-facing
// failures are Spanish natural language).
const (
	// messageInternal is the ONLY text an internal failure may produce. It is
	// deliberately ignorant of the cause: a driver name, a table, a path or a
	// wrapped cause on the wire is an information leak into the LLM context.
	messageInternal = "error interno del servidor"

	// messageRoleDenied answers a caller whose identity resolved but whose role
	// may not use the named tool. It does not confirm that the tool exists
	// beyond the refusal itself.
	messageRoleDenied = "tu rol no permite usar esta herramienta."
)

// Data codes carried in `data.code`. The identity and semantic classes reuse
// the codes of the layers that produced them (auth.CallerErrorCode,
// usecase.ErrorCode): a caller branches on the exact class, and the transport
// adds no vocabulary of its own beyond the two classes it alone can observe.
const (
	// dataCodeRoleDenied is the RBAC denial.
	dataCodeRoleDenied = "role_not_allowed"

	// dataCodeInvalidArguments is a tool call whose arguments do not fit the
	// tool's input schema (AGENTS.md: validate every tool arg before
	// persistence). It is a protocol-level -32602, not a business rejection.
	dataCodeInvalidArguments = "invalid_arguments"
)

// errorData is the `data` object every semantic-level error carries: one field,
// the machine-readable class. It is marshaled, never string-concatenated — the
// code values are ours, but a marshaled payload cannot be broken by a future
// quoted value.
type errorData struct {
	Code string `json:"code"`
}

// newRPCError builds a JSON-RPC error carrying the class in `data.code`
// (omitted when the class is empty).
func newRPCError(code int64, message, dataCode string) *jsonrpc.Error {
	wire := &jsonrpc.Error{Code: code, Message: message}
	if dataCode == "" {
		return wire
	}
	data, err := json.Marshal(errorData{Code: dataCode})
	if err != nil {
		// Unreachable: the payload is a single string field. If it ever became
		// reachable, a code-less error still beats a leaked marshaling error.
		return wire
	}
	wire.Data = data
	return wire
}

// callerRejected maps an auth.CallerError to RF2's -32000: the Spanish message
// verbatim, the CallerErrorCode in data.code so the gateway can tell "register
// this sender" (unknown_caller) apart from "ask an administrator"
// (account_disabled) or "your gateway broke" (missing_caller_id).
func callerRejected(failure *auth.CallerError) *jsonrpc.Error {
	return newRPCError(codeAuthorizationRejected, failure.Message(), string(failure.Code()))
}

// roleDenied is the fail-closed answer to a resolved caller invoking a tool its
// role may not use.
func roleDenied() *jsonrpc.Error {
	return newRPCError(codeAuthorizationRejected, messageRoleDenied, dataCodeRoleDenied)
}

// semanticRejected maps a usecase.SemanticError to -32002: the business
// rejection the caller is meant to read, with its ErrorCode in data.code.
func semanticRejected(failure *usecase.SemanticError) *jsonrpc.Error {
	return newRPCError(codeSemanticRejected, failure.Message(), string(failure.Code()))
}

// invalidArguments answers a tool call whose arguments do not match the tool's
// schema. The message is phrased by the tool (Spanish, no echo of the untrusted
// value).
func invalidArguments(message string) *jsonrpc.Error {
	return newRPCError(jsonrpc.CodeInvalidParams, message, dataCodeInvalidArguments)
}

// internalFailure is the collapse of everything that is not a semantic failure.
func internalFailure() *jsonrpc.Error {
	return newRPCError(jsonrpc.CodeInternalError, messageInternal, "")
}

// sanitize is AGENTS.md boundary contract 2 made executable: exactly four error
// shapes cross the wire, and the default is the generic internal error.
//
// The order matters. A *jsonrpc.Error is an error a lower layer already phrased
// for the wire (invalidArguments, or a deliberate protocol error from a tool) and
// passes through untouched. Anything else that is not a typed semantic failure —
// including a wrapped driver error that carries table names, SQL fragments or
// filesystem paths in its text — is discarded and replaced by messageInternal.
func sanitize(err error) *jsonrpc.Error {
	if err == nil {
		return internalFailure()
	}

	var wire *jsonrpc.Error
	if errors.As(err, &wire) {
		return wire
	}

	var callerErr *auth.CallerError
	if errors.As(err, &callerErr) {
		return callerRejected(callerErr)
	}

	var semanticErr *usecase.SemanticError
	if errors.As(err, &semanticErr) {
		return semanticRejected(semanticErr)
	}

	return internalFailure()
}
