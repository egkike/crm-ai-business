package auth

// Caller is a resolved caller: the identity a request is attributed to, plus
// the role the database assigned to it.
//
// A Caller is only ever produced by CallerResolver.Resolve, which is the sole
// path that reads the role. Constructing one anywhere else would bypass the
// §3.8 chain, so transports must treat it as an opaque value they receive.
type Caller struct {
	// ID is the internal UUID primary key of the `accounts` or `clients` row.
	// It is the only foreign key the business logic may use: natural
	// identifiers (phone, email) are resolution inputs, never identities.
	ID string

	// Name is the display name of the row. It is informational — personas and
	// audit display — and carries no authorization meaning.
	Name string

	// Role is the authorization role taken from the resolved row.
	Role Role
}
