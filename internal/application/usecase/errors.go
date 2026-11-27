package usecase

import "github.com/egkike/crm-ai-business/internal/domain"

// The semantic error machinery moved to internal/domain in F2 (the package
// the AGENTS.md layer map always reserved for it). This file is the
// compatibility surface every F1 use case already compiles against: the type
// aliases below are the SAME types, not copies — errors.As and the
// sanitization boundary in internal/mcp keep matching a single
// *domain.SemanticError.

// ErrorCode identifies an application-level semantic failure. Alias of
// domain.ErrorCode (same type, zero migration).
type ErrorCode = domain.ErrorCode

// SemanticError is the user-facing failure of a business rule. Alias of
// domain.SemanticError.
type SemanticError = domain.SemanticError

// The error-code constants, re-exported so the F1 files keep their names.
const (
	CodeInvalidIdentifier       = domain.CodeInvalidIdentifier
	CodeRegistrationRateLimited = domain.CodeRegistrationRateLimited
	CodeIdentifierInAccount     = domain.CodeIdentifierInAccount
	CodeIdentifierTaken         = domain.CodeIdentifierTaken

	CodeRoleNotAllowed      = domain.CodeRoleNotAllowed
	CodeInvalidCatalogKind  = domain.CodeInvalidCatalogKind
	CodeInvalidCatalogItem  = domain.CodeInvalidCatalogItem
	CodeCatalogItemNotFound = domain.CodeCatalogItemNotFound

	CodeInvalidTicket      = domain.CodeInvalidTicket
	CodeInvalidTicketState = domain.CodeInvalidTicketState
	CodeTicketNotFound     = domain.CodeTicketNotFound
	CodeTicketClosed       = domain.CodeTicketClosed
	CodeTicketStateChanged = domain.CodeTicketStateChanged
	CodeClientNotFound     = domain.CodeClientNotFound

	// The engine's own classes (F2 T3): a type nobody configured, and a stored
	// workflow that cannot be executed.
	CodeUnknownTicketType = domain.CodeUnknownTicketType
	CodeUnusableWorkflow  = domain.CodeUnusableWorkflow
)

// The sentinel semantic failures of the registration seam, re-exported as the
// same values (errors.Is keeps working across the move).
var (
	ErrInvalidIdentifier        = domain.ErrInvalidIdentifier
	ErrRateLimited              = domain.ErrRateLimited
	ErrIdentifierInAccountPhone = domain.ErrIdentifierInAccountPhone
	ErrIdentifierInAccountEmail = domain.ErrIdentifierInAccountEmail
	ErrIdentifierTaken          = domain.ErrIdentifierTaken
)

// newSemanticError phrases a rejection whose text depends on the offending
// input. It delegates to the single constructor the domain owns.
func newSemanticError(code ErrorCode, message string) error {
	return domain.NewSemanticError(code, message)
}
