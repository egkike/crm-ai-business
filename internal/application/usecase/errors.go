package usecase

// ErrorCode identifies an application-level semantic failure. Transports need
// the code, not the prose, to map a rejection to their own wire contract (T6
// maps it to a JSON-RPC server error) without parsing strings.
//
// The shape mirrors auth.CallerErrorCode on purpose: a transport must be able to
// treat both packages the same way. When internal/domain lands, both types move
// there (odd/tasks/backlog.md, "CallerError placement"); until then the code
// contract is what keeps the mapping from losing information.
type ErrorCode string

// The semantic failures of the anonymous registration seam.
const (
	// CodeInvalidIdentifier: the caller-id cannot be a natural identifier at
	// all (empty, or longer than any identifier the product stores).
	CodeInvalidIdentifier ErrorCode = "invalid_identifier"
	// CodeRegistrationRateLimited: the per-identifier registration budget of
	// the current window is exhausted (RF3 rate limit).
	CodeRegistrationRateLimited ErrorCode = "registration_rate_limited"
	// CodeIdentifierInAccount: the identifier already belongs to an internal
	// account, so it can never become a client identifier (PRD §3.8 forbids
	// internal self-registration).
	CodeIdentifierInAccount ErrorCode = "identifier_in_account"
	// CodeIdentifierTaken: the identifier already belongs to another client
	// row and is NOT auto-linked (RF3, fail-closed).
	CodeIdentifierTaken ErrorCode = "identifier_taken"
)

// The semantic failures of the RF4 catalog.
const (
	// CodeRoleNotAllowed: the caller's role may not perform this operation. It
	// is the in-use-case re-assertion of AGENTS.md's double RBAC: the transport
	// already filtered the tool, and this is the second, independent barrier that
	// a use case called from another adapter (the panel of F4) still enforces.
	CodeRoleNotAllowed ErrorCode = "role_not_allowed"

	// CodeInvalidCatalogKind: the kind is outside the RF4 vocabulary
	// (producto|servicio).
	CodeInvalidCatalogKind ErrorCode = "invalid_catalog_kind"

	// CodeInvalidCatalogItem: a catalog field violates its rule (empty name,
	// negative price, duration on a product, oversized text).
	CodeInvalidCatalogItem ErrorCode = "invalid_catalog_item"

	// CodeCatalogItemNotFound: the update names an id no row carries.
	CodeCatalogItemNotFound ErrorCode = "catalog_item_not_found"
)

// SemanticError is the application layer's user-facing failure.
//
// Its Message is the only text that may cross the sanitization boundary
// (AGENTS.md boundary contract 2): Spanish prose addressed to the human on the
// other side of the channel, with no internals — no paths, no SQL, no wrapped
// causes. Every other error a use case returns is an internal failure and must
// collapse to a generic error at the transport boundary.
type SemanticError struct {
	code    ErrorCode
	message string
}

// Error implements error. It returns the user-facing text so that incidental
// logging stays semantic; transports branch on Code.
func (e *SemanticError) Error() string { return e.message }

// Code returns the machine-readable failure class.
func (e *SemanticError) Code() ErrorCode { return e.code }

// Message returns the Spanish, user-facing text of the failure.
func (e *SemanticError) Message() string { return e.message }

// newSemanticError builds a semantic failure where the phrasing depends on the
// offending input (which field, which value). The code carries the
// machine-readable class, so a transport never parses the prose.
func newSemanticError(code ErrorCode, message string) error {
	return &SemanticError{code: code, message: message}
}

// The sentinel semantic failures of the registration seam. They are returned
// unwrapped so callers can match with errors.Is and render Message verbatim.
var (
	// ErrInvalidIdentifier rejects a caller-id that cannot be a natural
	// identifier. It is a validation failure, not a registration limit: no
	// budget is consumed and nothing is written.
	ErrInvalidIdentifier error = &SemanticError{
		code:    CodeInvalidIdentifier,
		message: "el identificador recibido no es válido. Contactá al negocio.",
	}

	// ErrRateLimited rejects an attempt once the per-identifier budget of the
	// window is exhausted. The caller is told both ways out: wait, or ask the
	// business to register them (the operator path always exists).
	ErrRateLimited error = &SemanticError{
		code:    CodeRegistrationRateLimited,
		message: "se superó el límite de intentos de registro. Volvé a intentarlo más tarde o pedile al negocio que te registre.",
	}

	// ErrIdentifierInAccountPhone rejects a phone that already belongs to an
	// internal account. The caller is told to ask an administrator: an internal
	// identifier is never self-registrable (PRD §3.8), so retrying cannot help.
	ErrIdentifierInAccountPhone error = &SemanticError{
		code:    CodeIdentifierInAccount,
		message: "este número ya pertenece a una cuenta del negocio. Contactá al administrador.",
	}

	// ErrIdentifierInAccountEmail is the e-mail variant of the account
	// collision: the phrasing names the identifier the sender used.
	ErrIdentifierInAccountEmail error = &SemanticError{
		code:    CodeIdentifierInAccount,
		message: "este email ya pertenece a una cuenta del negocio. Contactá al administrador.",
	}

	// ErrIdentifierTaken rejects a client-identifier collision. It is the
	// fail-closed answer of RF3: the attempt is refused, the existing row is
	// untouched and nothing is auto-linked.
	ErrIdentifierTaken error = &SemanticError{
		code:    CodeIdentifierTaken,
		message: "ya existe un cliente con ese dato. Contactá al negocio.",
	}
)
