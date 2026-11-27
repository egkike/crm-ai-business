package dto

// This file is the cross-layer contract of RF5 (tickets with auditable history):
// the shapes the ticket use cases receive and return, and the shapes
// internal/repository implements against (AGENTS.md boundary contract 4).
// Carriers only — every decision lives in internal/application/usecase and every
// SQL statement in internal/repository.
//
// F2 REPLACED the two fixed facts F1 mirrored here. The ticket type is no longer
// a constant (the creation names one and the engine loads ITS workflow), and the
// state vocabulary is no longer an enum: a ticket's states, its transitions, the
// role each transition requires and the state that ends the conversation are all
// configuration (domain.WorkflowConfig, RF6). What remains here is therefore the
// TYPE that carries a configured state name — `TicketState` — and no predicate
// about which names are legal, because that question has no answer without the
// ticket's type config.

// The ticket types of the shipped seed templates, mirrored from internal/db's
// seeded rows (db.TicketTypeConsultaID/db.TicketTypeConsultaName). The
// application layer must not import the persistence adapter, so the values are
// duplicated and a repository test pins them.
const (
	// ConsultaTicketTypeID is the internal UUID of the seeded 'consulta' type.
	ConsultaTicketTypeID = "00000000-0000-4000-8000-000000000001"

	// ConsultaTicketTypeName is the name of the seeded 'consulta' type: the
	// general contact type, and the type a creation that names none falls back
	// to (a general consultation, not a fixed pipeline).
	ConsultaTicketTypeName = "consulta"
)

// TicketState is the state of a ticket: the NAME of one of the states its type
// configures. It is a carrier, not an enum — its vocabulary is
// domain.WorkflowConfig.States, loaded per ticket type, and every question the
// product asks about a state (is it declared? is it terminal? may this role move
// the ticket into it?) is answered against that config, never against this type.
type TicketState string

// The state names the shipped templates declare, as VALUES. They are not a
// vocabulary: nothing in the product validates a state against this list, and
// deleting or renaming a state in a type's configuration never touches it. They
// stay here because the seeded pipelines are named by them everywhere — and
// because the ceiling fixture of internal/db (an append-only package) writes
// tickets with these two names.
const (
	// TicketStateNew is the initial state of both seed templates.
	TicketStateNew TicketState = "nuevo"

	// TicketStateAttended is the second state of the 'consulta' template.
	TicketStateAttended TicketState = "atendido"
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

	// TicketTypeID is the internal UUID of the ticket type (the key of its
	// configuration rows).
	TicketTypeID string

	// TicketType is the NAME of the ticket type: the key the workflow engine
	// loads the config by (domain.WorkflowConfigStore.LoadConfig).
	TicketType string

	// ClientID is the internal UUID of the clients row the ticket belongs to.
	ClientID string

	// State is the stored state: the name of one state of the type's config.
	State TicketState

	// AssignedTo is the internal UUID of the accounts row the ticket is
	// assigned to, or empty when it is in the unassigned pool (RF5).
	AssignedTo string

	// AreaID is the internal UUID of the area the ticket sits in (RF7): the
	// type's default area at creation, and the queue the round-robin rule picks
	// its assignee from (RF6). Empty means the type declares no fixed area
	// ('consulta': "sin área fija").
	AreaID string

	// DeadlineAt is the computed deadline of the current state (RF6: "`deadline`
	// por estado con aviso overdue"): the instant the scheduler compares against.
	// Empty means the current state declares no deadline.
	DeadlineAt string

	// ClosedAt is when the ticket reached a terminal state (RF6: terminality is
	// configuration, so the instant has to be stored). Empty means the
	// conversation is still open.
	ClosedAt string

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

	// TicketType is the NAME of the ticket type the ticket belongs to. The
	// adapter resolves it to the internal id: the engine addresses types by name
	// (their configuration is keyed by name), and resolving the row here keeps
	// the internal UUID out of the entity that crosses layers.
	TicketType string

	// ClientID is the internal UUID of the owning client.
	ClientID string

	// State is the initial state of the ticket: the type's configured initial
	// state (RF5: "queda en el estado inicial del tipo").
	State TicketState

	// AreaID is the type's default area, or empty when it declares none.
	AreaID string

	// DeadlineAt is the deadline computed from the initial state's
	// deadline_hours, or empty when that state declares no deadline.
	DeadlineAt string

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

	// DeadlineAt is the deadline the destination state declares, or empty when
	// it declares none. The write stores it (and clears the previous one), so
	// the scheduler compares one stored instant per ticket.
	DeadlineAt string

	// Closes reports that the destination state is TERMINAL in the type's
	// configuration. The write stamps closed_at for it (RF6: terminality is
	// configured, so the instant the conversation ended is stored).
	Closes bool

	// AssignTo is the accounts row the round-robin rule assigned the ticket to
	// (RF6: "auto-asignación round-robin del área"), or empty for "leave the
	// assignment as it is". An area with no members yields an empty value: the
	// ticket stays in the area's queue and the transition still applies.
	AssignTo string

	// Event is the audited transition event.
	Event NewTicketEvent
}

// TicketNoteAppend is one note to append to a ticket's history, guarded by the
// ticket still being open.
//
// The note travels with its guard for the same reason the transition does: the
// audit and the activity signal must be written by the same statement group, so
// a concurrent close cannot let the message land on a closed conversation.
//
// The guard carries NO state vocabulary any more (rider R3-001): whether a state
// ends the conversation is configuration (domain.WorkflowConfig.IsTerminal), so
// the adapter derives it from the transitions stored for the ticket's type — the
// same rule the use case reads from the config, expressed once for SQL instead of
// travelling here as a fixed value that a migrated ticket could contradict.
type TicketNoteAppend struct {
	// TicketID is the internal UUID of the ticket the note belongs to.
	TicketID string

	// Event is the audited note event.
	Event NewTicketEvent
}

// TicketQuery is the filter of a ticket list.
//
// IncludeClosed defaults to false: the list views are the ACTIVE views, so a
// ticket whose state is terminal drops out of the working list unless the caller
// asks for it explicitly. "Terminal" is configuration (the states with no
// outgoing transition), so the filter is derived from the transitions of each
// ticket's type — never from a fixed state name. Limit and Offset arrive already
// normalized by the application layer (a caller asks for a page, not for a page
// size).
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

	// TicketType is the NAME of the ticket type the contact belongs to, as the
	// business configures it (RF6). Empty means the general consultation type
	// (ConsultaTicketTypeName); a name nobody configured is the fail-closed
	// "no existe un tipo de ticket" rejection, never a silent fallback.
	TicketType string

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

	// State is the target state, as a NAME. The vocabulary is the configured one
	// of the ticket's type, so the wire carries a string and the decision is the
	// engine's (RF6): a pair the workflow does not declare is rejected, and so is
	// a transition the caller's role may not execute.
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
