package dto

// This file is the cross-layer contract of RF5 (tickets with auditable history):
// the shapes the ticket use cases receive and return, and the shapes
// internal/repository implements against (AGENTS.md boundary contract 4).
// Carriers only — every decision lives in internal/application/usecase and every
// SQL statement in internal/repository.
//
// The two F1 facts the carriers expose — the fixed ticket type and the fixed
// state vocabulary — are mirrored from the canonical DDL (internal/db) rather
// than imported from it: the application layer must not depend on the
// persistence adapter, and a repository test pins both mirrors against the
// seeded rows, so the duplication cannot drift silently.

// TicketState is the state of a ticket.
//
// V1/F1 STATE VOCABULARY — F2-REPLACEABLE. F1 has no workflow engine: the states
// are this fixed three-value vocabulary and `tickets.state` is a free TEXT column
// with the initial value as its DEFAULT (PRD §3.7). F2 turns states into rows
// (ticket_states/ticket_transitions) configured per ticket type, at which point
// this type becomes a carrier of the configured value instead of an enum, the
// `Valid`/`Open` predicates below become lookups against that configuration, and
// the workflow engine decides which transitions are legal. Nothing else in this
// contract depends on the three values.
type TicketState string

const (
	// TicketStateNew is the initial state of every F1 ticket: the contact exists
	// and nobody has worked it yet.
	TicketStateNew TicketState = "nuevo"

	// TicketStateAttended is the state of a ticket an internal actor has worked.
	TicketStateAttended TicketState = "atendido"

	// TicketStateClosed is the state that ends the conversation. A closed ticket
	// accepts no further notes: RF5 creates a NEW ticket when the client comes
	// back after the previous one was closed.
	TicketStateClosed TicketState = "cerrado"
)

// TicketStateInitial mirrors db.TicketStateInitial (the DEFAULT of
// tickets.state). A repository test pins the equality.
const TicketStateInitial = TicketStateNew

// Valid reports whether the state is one the F1 vocabulary knows. An unknown
// value is a rejected input, never a default.
func (s TicketState) Valid() bool {
	return s == TicketStateNew || s == TicketStateAttended || s == TicketStateClosed
}

// Open reports whether a ticket in this state still accepts activity (notes,
// re-engagement messages). An invalid state answers false: never treat an
// unknown state as open.
func (s TicketState) Open() bool {
	return s.Valid() && s != TicketStateClosed
}

// Closed reports whether the state ends the conversation.
func (s TicketState) Closed() bool {
	return s == TicketStateClosed
}

// The single F1 ticket type, mirrored from the seed of migration 2
// (db.TicketTypeConsultaID/db.TicketTypeConsultaName). F2 turns ticket types into
// configuration; until then every ticket the product opens is a 'consulta'.
const (
	// ConsultaTicketTypeID is the internal UUID of the seeded F1 ticket type.
	ConsultaTicketTypeID = "00000000-0000-4000-8000-000000000001"

	// ConsultaTicketTypeName is the name of the seeded F1 ticket type.
	ConsultaTicketTypeName = "consulta"
)

// The ticket_events kinds F1 writes. They are open vocabulary on purpose (the
// column has no CHECK): F2 adds the engine's own kinds (on-enter/on-exit
// notifications, template events).
const (
	// TicketEventKindCreated is the opening event of a ticket. Its payload
	// carries the type, the initial state and the motive of the contact.
	TicketEventKindCreated = "creado"

	// TicketEventKindNote is a note or a re-engagement message. A client coming
	// back about an open ticket of the type appends to it instead of opening a
	// new one (RF5), and that message is exactly this kind.
	TicketEventKindNote = "nota"

	// TicketEventKindTransition is a state change. Its payload carries the from
	// and to states, so the history reads "who, when and from/to which state"
	// (RF5 acceptance criterion).
	TicketEventKindTransition = "transicion"
)

// TicketRecord is one stored ticket as every consumer reads it.
//
// The motive of the contact is NOT a column of `tickets` (the schema is the
// closed contract of F1): it lives in the payload of the opening event, which is
// the audited history of RF5. Readers that need it read the events, never a
// column that does not exist.
type TicketRecord struct {
	// ID is the internal UUID primary key.
	ID string

	// TicketTypeID is the ticket type of the ticket. F1 always carries
	// ConsultaTicketTypeID.
	TicketTypeID string

	// ClientID is the internal UUID of the clients row the ticket belongs to.
	ClientID string

	// State is the stored state (TicketState vocabulary in F1).
	State TicketState

	// AssignedTo is the internal UUID of the accounts row the ticket is
	// assigned to, or empty when it is in the unassigned pool (RF5).
	AssignedTo string

	// CreatedAt and UpdatedAt are the stored ISO-8601 UTC timestamps. UpdatedAt
	// moves on every activity (transition, note), which is the activity signal
	// the F2 scheduler reads for its inactivity alerts.
	CreatedAt string
	UpdatedAt string
}

// NewTicket carries the row a creation wants to insert: the ticket itself plus
// its opening event.
//
// The two travel together because RF5 requires the audited creation event, and a
// ticket without it would be a history hole. The repository inserts both inside
// one transaction.
type NewTicket struct {
	// ID is the internal UUID primary key, generated by the application layer.
	ID string

	// TicketTypeID is the ticket type of the ticket (F1: ConsultaTicketTypeID).
	TicketTypeID string

	// ClientID is the internal UUID of the owning client.
	ClientID string

	// State is the initial state of the ticket.
	State TicketState

	// OpeningEvent is the audited creation event. Its TicketID is ignored: the
	// repository writes the event against this ticket's ID.
	OpeningEvent NewTicketEvent
}

// NewTicketEvent carries one event to append to the history (RF5: every
// transition, note, re-engagement message and assignment change is a row with
// actor and timestamp).
//
// The timestamp is NOT part of the carrier: it is the database's DEFAULT, so the
// recorded instant is the instant of the write and never a value a caller could
// choose.
type NewTicketEvent struct {
	// TicketID is the internal UUID of the ticket the event belongs to.
	TicketID string

	// Actor is the identity that acted: the internal UUID of the accounts or
	// clients row. An internal UUID is used instead of the channel identifier
	// because an audit row must never carry raw PII (AGENTS.md: phones are
	// masked in every audit event; storing the UUID means there is nothing to
	// mask) and because the identity survives a phone or e-mail change.
	Actor string

	// Kind is one of the TicketEventKind* values (open vocabulary).
	Kind string

	// Payload is a JSON object document (possibly "{}"). The database rejects a
	// malformed document (CHECK json_valid), so every reader can parse it.
	Payload string
}

// TicketEventRecord is one stored event, as the audit view reads it.
type TicketEventRecord struct {
	// ID is the AUTOINCREMENT primary key: the history is ordered by insertion
	// and a deleted event's id is never reused.
	ID int64

	// TicketID is the internal UUID of the ticket.
	TicketID string

	// Actor is the identity that acted (internal UUID), or "system".
	Actor string

	// Kind is the event kind.
	Kind string

	// Payload is the stored JSON object document.
	Payload string

	// CreatedAt is the instant the event was written.
	CreatedAt string
}

// TicketTransition is one state change to record: the guarded row update plus the
// transition event, written in one transaction.
//
// The pair travels together for the same reason the creation does: an update
// without its event is an unaudited state change, and an event without its update
// is a lie about the history.
type TicketTransition struct {
	// TicketID is the internal UUID of the ticket.
	TicketID string

	// From is the state the caller read. It guards the update (the row is only
	// changed if it is still in From), which is how two concurrent actors cannot
	// silently overwrite each other's transition.
	From TicketState

	// To is the target state.
	To TicketState

	// Event is the audited transition event.
	Event NewTicketEvent
}

// TicketQuery is the filter of a ticket list.
//
// IncludeClosed defaults to false: the list views of F1 are the ACTIVE views, so
// a closed ticket drops out of the working list unless the caller asks for it
// explicitly. Limit and Offset arrive already normalized by the application
// layer (a caller asks for a page, not for a page size).
type TicketQuery struct {
	// ClientID filters the tickets of one client. Empty means "no client
	// filter" — which the application layer only ever allows for an internal
	// caller.
	ClientID string

	// IncludeClosed includes the tickets whose conversation is closed.
	IncludeClosed bool

	// Limit is the page size already normalized.
	Limit int

	// Offset is the number of tickets to skip.
	Offset int
}

// TicketView is one ticket plus its audited history.
//
// The history travels with the ticket because RF5's criterion is "cuando se
// listan sus eventos, entonces cada transición muestra quién, cuándo y
// desde/hacia qué estado" — and F1 has no separate events tool. F1's history per
// ticket is small (the ticket lives for one contact), so the read stays bounded
// by the list page.
type TicketView struct {
	// Ticket is the stored ticket.
	Ticket TicketRecord

	// Events is the ticket's history, oldest first.
	Events []TicketEventRecord
}

// CreateTicketCommand is the input of the creation use case.
type CreateTicketCommand struct {
	// Motive is the business motive of the contact — the reason the ticket
	// exists. It is required: RF5's exception is the message with no business
	// motive (off-topic puro), which must not open a ticket at all.
	Motive string

	// ClientID is the client the ticket is opened for. It is honored ONLY for an
	// internal caller opening a ticket on behalf of a client; a client role
	// always opens its own ticket and this field is ignored (the caller's
	// identity from the request context wins, never a field of the body).
	ClientID string
}

// CreateTicketResult is the answer of a creation: the created ticket plus its
// opening event, so the caller sees the audited creation without a second read.
type CreateTicketResult struct {
	// Ticket is the created ticket.
	Ticket TicketRecord

	// Events is the ticket's history (the opening event, in F1).
	Events []TicketEventRecord
}

// TicketNote is a note or a re-engagement message to append to a ticket.
type TicketNote struct {
	// TicketID is the internal UUID of the ticket.
	TicketID string

	// Text is the message. It is the re-engagement base of RF5: a client that
	// comes back about an open ticket of the type appends here instead of
	// opening a second ticket.
	Text string
}

// TicketNoteResult is the answer of a note: the ticket after the activity plus its
// full history.
type TicketNoteResult struct {
	// Ticket is the ticket after the note.
	Ticket TicketRecord

	// Events is the ticket's history, oldest first (the note is the last one).
	Events []TicketEventRecord
}

// TransitionCommand is the input of the transition use case.
type TransitionCommand struct {
	// TicketID is the internal UUID of the ticket.
	TicketID string

	// State is the target state. F1 validates it against the fixed vocabulary;
	// F2 validates it against the configured transitions of the ticket type.
	State TicketState
}

// TransitionTicketResult is the answer of a transition: the ticket in its new
// state plus its full history, which is where "quién, cuándo y desde/hacia qué
// estado" is readable.
type TransitionTicketResult struct {
	// Ticket is the ticket after the transition.
	Ticket TicketRecord

	// Events is the ticket's history, oldest first (the transition is the last
	// one).
	Events []TicketEventRecord
}

// TicketListResult is a page of tickets with their histories, plus the pagination
// the application layer applied.
type TicketListResult struct {
	// Tickets are the matching tickets, most recently active first.
	Tickets []TicketView

	// Limit is the page size applied.
	Limit int

	// Offset is the number of tickets skipped.
	Offset int
}
