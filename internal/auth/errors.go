package auth

// CallerErrorCode identifies a semantic identity failure. Transports need the
// code, not the prose, to map a rejection to their own wire contract (T6 maps
// it to a JSON-RPC -32000 server error) without parsing strings.
type CallerErrorCode string

// The semantic identity failures of the §3.8 chain.
const (
	// CodeMissingCallerID: the request carried no X-Caller-Id.
	CodeMissingCallerID CallerErrorCode = "missing_caller_id"
	// CodeAccountDisabled: an `accounts` row exists but is_active = 0.
	CodeAccountDisabled CallerErrorCode = "account_disabled"
	// CodeUnknownCaller: neither an `accounts` nor a `clients` row matched.
	CodeUnknownCaller CallerErrorCode = "unknown_caller"
)

// CallerError is a typed, user-facing identity failure. Its Message is the
// only text that may cross the sanitization boundary (AGENTS.md boundary
// contract 2): it is Spanish prose addressed to the human on the other side of
// the channel, with no internals — no paths, no SQL, no wrapped causes.
//
// Every other error the resolver returns is an internal failure and must
// collapse to a generic error at the transport boundary.
type CallerError struct {
	code    CallerErrorCode
	message string
}

// Error implements error. It returns the same user-facing text as Message so
// that incidental logging stays semantic; transports should branch on Code.
func (e *CallerError) Error() string { return e.message }

// Code returns the machine-readable failure class.
func (e *CallerError) Code() CallerErrorCode { return e.code }

// Message returns the Spanish, user-facing text of the failure.
func (e *CallerError) Message() string { return e.message }

// The sentinel semantic failures. They are returned unwrapped so callers can
// match with errors.Is and render Message verbatim.
var (
	// ErrMissingCallerID rejects a request that carries no identity at all.
	ErrMissingCallerID error = &CallerError{
		code:    CodeMissingCallerID,
		message: "no se proporcionó X-Caller-Id",
	}

	// ErrAccountDisabled rejects a known internal account that is switched
	// off. The caller is told to ask an administrator — self-reactivation is
	// not a thing.
	ErrAccountDisabled error = &CallerError{
		code:    CodeAccountDisabled,
		message: "tu cuenta está deshabilitada. Contacta al administrador.",
	}

	// ErrUnknownCaller rejects an identity with no row. For clients this is the
	// entry point of the registration loop (RF3); for an unknown internal
	// phone there is no self-registration at all (PRD §3.8).
	ErrUnknownCaller error = &CallerError{
		code:    CodeUnknownCaller,
		message: "no te reconozco. Por favor regístrate primero.",
	}
)
