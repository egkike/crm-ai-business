package auth

// Role is the authorization role of a caller, resolved exclusively from the
// database (PRD §3.8). It is never derived from request content: a message
// claiming "soy el dueño" cannot produce a Role, only a database row can.
type Role string

// The complete role set. Internal roles come from `accounts`; clients come
// from `clients`. There are no other roles — anything else is invalid and is
// rejected by the resolver (fail-closed).
const (
	RoleOwner  Role = "owner"
	RoleAdmin  Role = "admin"
	RoleStaff  Role = "staff"
	RoleClient Role = "client"
)

// Valid reports whether r is one of the four known roles. Anything else,
// including the zero value, is a corrupt or unsupported row and must not
// authorize anything.
func (r Role) Valid() bool {
	switch r {
	case RoleOwner, RoleAdmin, RoleStaff, RoleClient:
		return true
	}
	return false
}

// IsInternal reports whether the role belongs to the business team. Internal
// roles operate the backend (Telegram bot and the internal panel); clients
// only reach the client-facing channel tools. An invalid role answers false:
// never grant internal status by accident.
func (r Role) IsInternal() bool {
	return r == RoleOwner || r == RoleAdmin || r == RoleStaff
}

// CanManageWorkflows reports whether the role may configure business-wide
// settings — profile, catalog, areas and workflows, the Owner/Admin surface of
// PRD §4.1.
//
// This predicate is the basis for the role-based `tools/list` filtering of
// RF2: the tools whose effect is owner/admin configuration must never be
// announced to a staff or client caller. An invalid role answers false, so an
// unrecognized row cannot unlock administrative tools.
func (r Role) CanManageWorkflows() bool {
	return r == RoleOwner || r == RoleAdmin
}
