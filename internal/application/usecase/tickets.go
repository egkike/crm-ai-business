package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/egkike/crm-ai-business/internal/application/dto"
	"github.com/egkike/crm-ai-business/internal/auth"
	"github.com/egkike/crm-ai-business/internal/domain"
)

// This file is the application layer of RF5 (tickets con historial auditable) as
// F2 left it: the creation and the transition EXECUTE the workflow the business
// configured for the ticket's type (RF5: "estados/deadlines computan desde la
// config del tipo"; RF6: transitions, roles and round-robin are configuration),
// and the two list views, the note and the re-engagement path derive "closed"
// from that same configuration instead of from a fixed vocabulary.
//
// The engine is thin on purpose. The questions it asks are the domain's
// (WorkflowConfig.State/Transition/IsTerminal, TransitionConfig.CanBeExecutedBy);
// what lives here is the orchestration: load the config of the ticket's type,
// map a resolved auth.Role onto the domain's transition vocabulary, compute the
// deadline of the state being entered, ask the area rotation who is next, build
// the audited event and hand the guarded write to the repository.
//
// Every use case re-asserts its own authorization (AGENTS.md boundary contract 1,
// double RBAC): the MCP transport already filtered the tool by role, and the
// re-assertion is the independent barrier that still holds when the same use case
// is reached from another adapter (the internal panel of F4) or when the
// transport's permission map is edited by mistake. On top of the role gate, the
// transition enforces the PER-TRANSITION minimum role the Owner configured (RF6).
//
// The AUDIT is the reason these carriers exist. RF5 requires that every
// transition, note, re-engagement message and assignment change lands in
// ticket_events with actor and timestamp, so every mutating use case builds its
// event in the same step that performs the change, and the repository writes both
// in one transaction. What the audit row carries, and what it deliberately does
// not:
//
//   - actor is the internal UUID of the accounts/clients row. Not the channel
//     identifier: the audit must never store raw PII (AGENTS.md masks phones to
//     their last 4 digits in every audit event) and the internal UUID is the
//     identity that survives a phone change, an e-mail claim and a merge
//     (PRD §3.7).
//   - payload is a JSON object; the free text a human wrote (the motive of the
//     contact, the body of a note) is masked by maskPhoneRuns before it is
//     stored, because a client routinely types their own phone into a message and
//     the audit is not the place it should be retained verbatim.

// TicketStore is the driven port of ticket persistence (RF5).
//
// It exposes the operations the use cases need and nothing else: no delete (the
// history is append-only and a ticket is the audit anchor of everything that
// references it) and no assignment write (taking a ticket is F2's RF9 flow).
type TicketStore interface {
	// CreateTicket inserts the ticket and its opening event in one transaction.
	CreateTicket(ctx context.Context, ticket dto.NewTicket) error

	// GetTicket returns one stored ticket, or found=false.
	GetTicket(ctx context.Context, id string) (dto.TicketRecord, bool, error)

	// ListTicketsByClient returns the tickets of one client, most recently
	// active first. includeClosed=false keeps only the open conversations.
	ListTicketsByClient(ctx context.Context, clientID string, includeClosed bool, limit, offset int) ([]dto.TicketRecord, error)

	// ListTickets returns the tickets of every client (the internal views),
	// most recently active first.
	ListTickets(ctx context.Context, query dto.TicketQuery) ([]dto.TicketRecord, error)

	// TransitionTicket applies the guarded state change and appends its event in
	// one transaction. It reports found=false when the row is no longer in
	// transition.From, which the use case renders as the "cambió mientras lo
	// operabas" rejection instead of overwriting another actor's work.
	TransitionTicket(ctx context.Context, transition dto.TicketTransition) (bool, error)

	// AddTicketNote appends a note to the history, moves the ticket's activity
	// and reads the ticket back, all in one transaction guarded by the ticket
	// still being open.
	//
	// It reports found=false when the guard refused (the ticket is gone or its
	// state is terminal in the type's configuration), in which case nothing was
	// written. It is one operation instead of append + touch + read because a
	// note that is audited while the ticket's activity stays put leaves the F2
	// inactivity clock stale, and a note that lands after a concurrent close
	// reopens a closed conversation.
	AddTicketNote(ctx context.Context, note dto.TicketNoteAppend) (dto.TicketRecord, bool, error)

	// ListTicketEvents returns a ticket's history, oldest first.
	ListTicketEvents(ctx context.Context, ticketID string) ([]dto.TicketEventRecord, error)
}

// AreaRotator is the round-robin queue of one area (RF6: "auto-asignación
// round-robin del área").
//
// It is a port of its own instead of a method of the ticket store because the
// question ("who is next in this area?") and the queue it reads (membership
// plus past assignments) are not the ticket's: the engine asks once per
// transition that declares the rule, and a fake with three members is all a use
// case test needs to pin the wiring.
type AreaRotator interface {
	// NextAreaAssignee returns the account the next ticket of the area belongs
	// to, or "" when the area has no members (the ticket then stays in the
	// area's queue: an empty area is not an error).
	NextAreaAssignee(ctx context.Context, areaID string) (string, error)
}

// ClientDirectory is the only thing the ticket use cases need from clients: "does
// this client exist?".
//
// It is a dedicated port instead of a widened ClientStore because the two
// consumers are unrelated: the registration seam (RF3) admits rows by natural
// identifier, and the ticket creation (RF5) only has to reject an internal
// caller naming a client that does not exist. Widening ClientStore would force
// every future implementer of the registration port to answer a question it does
// not have.
type ClientDirectory interface {
	// ClientExistsByID reports whether a clients row carries this internal UUID.
	ClientExistsByID(ctx context.Context, id string) (bool, error)
}

const (
	// DefaultTicketListLimit is the page size when the caller sends none: a
	// small page keeps the agent's context short, and the list carries each
	// ticket's history.
	DefaultTicketListLimit = 20

	// MaxTicketListLimit bounds one page. The limit is an input, and an unbounded
	// page is a way to pull every ticket of the business into an LLM context.
	// The transport decoder enforces the same bound, so an oversized page is
	// refused there instead of travelling one hop to be capped here.
	MaxTicketListLimit = 50

	// MaxTicketMotiveLen bounds the motive of a contact. 120 runes is the
	// display-name precedent of this product (maxDisplayNameLen,
	// maxCatalogNameLen): a motive is the client's one-line statement of what they
	// need ("me duele la espalda desde el lunes"), not a transcript — the
	// transcript is the ticket's history, and a long text would travel into every
	// list view whose ticket carries it.
	MaxTicketMotiveLen = 120

	// MaxTicketNoteLen bounds a note or re-engagement message. 2000 runes is the
	// catalog's long-text bound: a paragraph is legitimate, a payload is not, and
	// this text is stored in the event payload that every history read returns.
	MaxTicketNoteLen = 2000

	// minMaskedDigitRun is the shortest run of digits maskPhoneRuns masks. A
	// phone is at least 7 digits even when dialled locally, so a shorter run
	// (a quantity, a price, an age) is left alone, and the masking does not turn
	// every number in a message into noise.
	minMaskedDigitRun = 7

	// MaxTicketTypeNameLen bounds the ticket type a creation may name. It is the
	// workflow name bound of the domain (domain.MaxWorkflowNameLen), which is
	// also the DDL's `length(name) BETWEEN 1 AND 50` of ticket_types.
	MaxTicketTypeNameLen = domain.MaxWorkflowNameLen

	// defaultTicketTypeName is the type a creation that names none opens: the
	// general consultation of the seed templates (dto.ConsultaTicketTypeName).
	//
	// It is a default for the INPUT (the agent does not always know the type),
	// never a fallback for the CONFIG: a type that is not configured is rejected
	// with a semantic error even when it is this one (fail-closed).
	defaultTicketTypeName = dto.ConsultaTicketTypeName

	// ticketTimestampLayout is the instant format of the ticket columns: ISO-8601
	// UTC with millisecond precision, exactly what the schema's DEFAULT writes
	// (`strftime('%Y-%m-%dT%H:%M:%fZ')`), so a value computed here sorts next to a
	// value computed by SQLite.
	ticketTimestampLayout = "2006-01-02T15:04:05.000Z"
)

// The semantic failures of this file that are shared by more than one use case.
// They are built with newSemanticError because the struct's fields are owned
// by internal/domain since F2.
var (
	// errTicketIDRequired rejects a call that names no ticket. It is returned
	// unwrapped so a caller can match it with errors.Is and render Message
	// verbatim.
	errTicketIDRequired error = newSemanticError(CodeInvalidTicket,
		"el identificador del ticket es obligatorio.")

	// errTicketNotFound rejects a ticket id no visible row carries.
	errTicketNotFound error = newSemanticError(CodeTicketNotFound,
		"no existe un ticket con ese identificador.")

	// errTicketClosed rejects any activity on a closed conversation. RF5 opens a
	// new ticket when the client comes back: writing into the closed history
	// would erase the boundary the re-engagement policy is built on.
	errTicketClosed error = newSemanticError(CodeTicketClosed,
		"el ticket está cerrado: una conversación cerrada no se reabre con una nota. "+
			"Si el cliente vuelve, se abre un ticket nuevo.")
)

// requireTicketReader re-asserts the permission of the ticket surface every
// resolved caller has: opening a ticket, listing one's own and appending a note.
//
// A caller without a resolved role AND a caller without an internal id are both
// refused with the same rejection: a ticket needs an actor for its audit, and an
// identity that cannot be written as an actor cannot perform the action. The
// refusal carries no internal detail (fail-closed).
func requireTicketReader(caller auth.Caller) error {
	if caller.Role.Valid() && caller.ID != "" {
		return nil
	}
	return newSemanticError(CodeRoleNotAllowed, "tu rol no permite consultar tickets.")
}

// requireTicketLister re-asserts the permission of the internal list view
// (list_tickets): the tickets of every client are the team's working queue, not
// something a client may enumerate.
func requireTicketLister(caller auth.Caller) error {
	if caller.Role.IsInternal() {
		return nil
	}
	return newSemanticError(CodeRoleNotAllowed, "tu rol no permite listar los tickets del negocio.")
}

// requireTicketOperator re-asserts the permission of the transition: moving a
// ticket through its states is an internal operation (RF9: "internos operan
// tickets"; RF6 will restrict it further per configured transition).
func requireTicketOperator(caller auth.Caller) error {
	if caller.Role.IsInternal() {
		return nil
	}
	return newSemanticError(CodeRoleNotAllowed, "tu rol no permite cambiar el estado de un ticket.")
}

// ticketActor is the audit actor of a resolved caller: the internal UUID of its
// accounts or clients row (see the file comment for why it is not the channel
// identifier).
func ticketActor(caller auth.Caller) string {
	return caller.ID
}

// CreateTicket is the RF5 creation use case behind create_ticket.
//
// RF5: "todo contacto con motivo de negocio genera un ticket" — also a contact
// that neither buys nor requests anything. The use case asks for a motive and the
// type, and the type's CONFIGURATION decides the rest (RF5: "estados/deadlines
// computan desde la config del tipo"): the state the ticket is born in, the area
// it starts in, and the deadline that state declares.
type CreateTicket struct {
	tickets   TicketStore
	clients   ClientDirectory
	workflows domain.WorkflowConfigStore
}

// NewCreateTicket wires the use case. A nil port is a fail-closed configuration
// error: without persistence the use case could only fail, without the client
// directory the internal "on behalf of" path could not tell a real client from a
// typo, and without the workflow store the creation could not know the initial
// state of the type it is opening.
func NewCreateTicket(tickets TicketStore, clients ClientDirectory, workflows domain.WorkflowConfigStore) (*CreateTicket, error) {
	if tickets == nil {
		return nil, errors.New("tickets: se requiere un TicketStore para crear tickets")
	}
	if clients == nil {
		return nil, errors.New("tickets: se requiere un ClientDirectory para crear tickets")
	}
	if workflows == nil {
		return nil, errors.New("tickets: se requiere un WorkflowConfigStore para crear tickets")
	}
	return &CreateTicket{tickets: tickets, clients: clients, workflows: workflows}, nil
}

// Execute opens a ticket for the caller, or for cmd.ClientID when the caller is
// internal.
//
// The two paths are deliberately different:
//
//   - client role: the ticket belongs to the caller. cmd.ClientID is IGNORED, not
//     validated against the caller — the identity travels in the request
//     context, and a body field never decides who a client is (PRD §3.8, "el
//     canal transporta una identidad, nunca un rol" is the same rule applied to
//     ownership).
//   - internal role: the caller names the client (a staff member opens the ticket
//     a phone call produced). An unknown client is the semantic "no existe un
//     cliente" rejection, never a foreign-key error.
func (uc *CreateTicket) Execute(ctx context.Context, caller auth.Caller, cmd dto.CreateTicketCommand) (dto.CreateTicketResult, error) {
	if err := requireTicketReader(caller); err != nil {
		return dto.CreateTicketResult{}, err
	}

	motive, err := validateTicketMotive(cmd.Motive)
	if err != nil {
		return dto.CreateTicketResult{}, err
	}

	clientID := ticketActor(caller)
	if caller.Role.IsInternal() {
		clientID = strings.TrimSpace(cmd.ClientID)
		if clientID == "" {
			return dto.CreateTicketResult{}, newSemanticError(CodeInvalidTicket,
				"indique el cliente para el que se abre el ticket.")
		}
		exists, err := uc.clients.ClientExistsByID(ctx, clientID)
		if err != nil {
			return dto.CreateTicketResult{}, fmt.Errorf("tickets: verificar el cliente del ticket: %w", err)
		}
		if !exists {
			return dto.CreateTicketResult{}, newSemanticError(CodeClientNotFound,
				"no existe un cliente con ese identificador.")
		}
	}

	// The type's configuration is the engine's first step: it answers where the
	// ticket starts, in which area and with which deadline. A type nobody
	// configured is a semantic rejection, never a fallback to a fixed pipeline.
	config, err := loadWorkflow(ctx, uc.workflows, cmd.TicketType)
	if err != nil {
		return dto.CreateTicketResult{}, err
	}

	initial, found := config.State(config.InitialState)
	if !found {
		// loadWorkflow validated the config, which rejects an undeclared initial
		// state; the branch keeps the invariant local instead of trusting it.
		return dto.CreateTicketResult{}, newSemanticError(CodeUnusableWorkflow, fmt.Sprintf(
			"el workflow del tipo '%s' no declara el estado inicial '%s'.",
			config.TypeName, config.InitialState))
	}

	id, err := newUUID()
	if err != nil {
		return dto.CreateTicketResult{}, fmt.Errorf("tickets: crear el ticket: %w", err)
	}

	payload, err := encodeTicketEventPayload(ticketCreatedPayload{
		TicketType: config.TypeName,
		State:      dto.TicketState(config.InitialState),
		Motive:     maskPhoneRuns(motive),
	})
	if err != nil {
		return dto.CreateTicketResult{}, fmt.Errorf("tickets: crear el ticket: %w", err)
	}

	row := dto.NewTicket{
		ID:         id,
		TicketType: config.TypeName,
		ClientID:   clientID,
		State:      dto.TicketState(config.InitialState),
		AreaID:     config.DefaultAreaID,
		DeadlineAt: nextDeadline(initial, time.Now()),
		OpeningEvent: dto.NewTicketEvent{
			// The id below belongs to the ticket, not to the event: the event's
			// own id is the AUTOINCREMENT key the database assigns.
			TicketID: id,
			Actor:    ticketActor(caller),
			Kind:     dto.TicketEventKindCreated,
			Payload:  payload,
		},
	}
	if err := uc.tickets.CreateTicket(ctx, row); err != nil {
		return dto.CreateTicketResult{}, fmt.Errorf("tickets: crear el ticket: %w", err)
	}

	created, found, err := uc.tickets.GetTicket(ctx, id)
	if err != nil {
		return dto.CreateTicketResult{}, fmt.Errorf("tickets: leer el ticket creado: %w", err)
	}
	if !found {
		return dto.CreateTicketResult{}, fmt.Errorf("tickets: el ticket %s no se encontró después de crearlo", id)
	}

	events, err := uc.events(ctx, id)
	if err != nil {
		return dto.CreateTicketResult{}, err
	}
	return dto.CreateTicketResult{Ticket: created, Events: events}, nil
}

// events reads the history of one ticket.
func (uc *CreateTicket) events(ctx context.Context, ticketID string) ([]dto.TicketEventRecord, error) {
	events, err := uc.tickets.ListTicketEvents(ctx, ticketID)
	if err != nil {
		return nil, fmt.Errorf("tickets: leer el historial del ticket: %w", err)
	}
	return events, nil
}

// ListMyTickets is the RF5 client view behind list_my_tickets: "my tickets".
//
// A client sees ONLY its own tickets, and the filter is not a request: the
// caller's internal UUID overwrites whatever client filter the body carried, so a
// cross-client query returns the caller's tickets (or none) and never somebody
// else's. An internal caller may filter by client, or list every ticket of the
// business when it does not.
type ListMyTickets struct {
	tickets TicketStore
}

// NewListMyTickets wires the use case.
func NewListMyTickets(tickets TicketStore) (*ListMyTickets, error) {
	if tickets == nil {
		return nil, errors.New("tickets: se requiere un TicketStore para listar tickets")
	}
	return &ListMyTickets{tickets: tickets}, nil
}

// Execute returns the page of tickets the caller may see.
func (uc *ListMyTickets) Execute(ctx context.Context, caller auth.Caller, query dto.TicketQuery) (dto.TicketListResult, error) {
	if err := requireTicketReader(caller); err != nil {
		return dto.TicketListResult{}, err
	}

	query = normalizeTicketQuery(query)
	if !caller.Role.IsInternal() {
		// The caller's own identity wins over the body field (see Execute's
		// sibling in CreateTicket).
		query.ClientID = ticketActor(caller)
	}
	return listTickets(ctx, uc.tickets, query)
}

// ListTickets is the internal working queue behind list_tickets (RF8's "staff
// ve sus asignados + los no asignados de sus áreas; admin/owner ven todo" is the
// F2 refinement of this F1 list, which shows the open tickets of the business).
type ListTickets struct {
	tickets TicketStore
}

// NewListTickets wires the use case.
func NewListTickets(tickets TicketStore) (*ListTickets, error) {
	if tickets == nil {
		return nil, errors.New("tickets: se requiere un TicketStore para listar tickets")
	}
	return &ListTickets{tickets: tickets}, nil
}

// Execute returns the page of tickets matching the filter. The client role is
// refused by the use case even though the transport already hid the tool.
func (uc *ListTickets) Execute(ctx context.Context, caller auth.Caller, query dto.TicketQuery) (dto.TicketListResult, error) {
	if err := requireTicketLister(caller); err != nil {
		return dto.TicketListResult{}, err
	}
	return listTickets(ctx, uc.tickets, normalizeTicketQuery(query))
}

// listTickets runs the filtered read and attaches each ticket's history.
//
// The history is read per ticket: F1's page is bounded (MaxTicketListLimit) and a
// ticket's history is a single contact, so the read stays small, and the
// (ticket_id, id) index makes each one cheap. A caller that wants the whole
// history of a long-lived ticket asks for that ticket, not for the list.
func listTickets(ctx context.Context, store TicketStore, query dto.TicketQuery) (dto.TicketListResult, error) {
	var (
		rows []dto.TicketRecord
		err  error
	)
	if query.ClientID != "" {
		rows, err = store.ListTicketsByClient(ctx, query.ClientID, query.IncludeClosed, query.Limit, query.Offset)
	} else {
		rows, err = store.ListTickets(ctx, query)
	}
	if err != nil {
		return dto.TicketListResult{}, fmt.Errorf("tickets: listar tickets: %w", err)
	}

	views := make([]dto.TicketView, 0, len(rows))
	for _, ticket := range rows {
		events, err := store.ListTicketEvents(ctx, ticket.ID)
		if err != nil {
			return dto.TicketListResult{}, fmt.Errorf("tickets: leer el historial del ticket: %w", err)
		}
		views = append(views, dto.TicketView{Ticket: ticket, Events: events})
	}

	return dto.TicketListResult{Tickets: views, Limit: query.Limit, Offset: query.Offset}, nil
}

// normalizeTicketQuery applies the page policy: absent (or nonsense) means the
// default, an oversized request is capped instead of refused, and a negative
// offset is zero.
func normalizeTicketQuery(query dto.TicketQuery) dto.TicketQuery {
	query.ClientID = strings.TrimSpace(query.ClientID)

	switch {
	case query.Limit <= 0:
		query.Limit = DefaultTicketListLimit
	case query.Limit > MaxTicketListLimit:
		query.Limit = MaxTicketListLimit
	}
	if query.Offset < 0 {
		query.Offset = 0
	}
	return query
}

// AddTicketNote is the re-engagement base of RF5 behind add_ticket_note: "si el
// Cliente vuelve sobre lo mismo con un ticket abierto de ese tipo, sus mensajes
// se agregan como eventos al ticket existente y no se crea uno nuevo".
//
// The use case appends the message as an event of the existing ticket and moves
// its activity in ONE repository operation, so it is that one open ticket that
// grows instead of the funnel counter. Finding "the open ticket of that type" is
// the caller's loop (Hermes lists the client's tickets with list_my_tickets and
// appends here); F1 does not guess which conversation a message belongs to.
//
// A CLOSED ticket rejects the note: "ticket nuevo solo cuando el anterior está
// cerrado" (RF5). Reopening a closed conversation by writing into it would erase
// the very boundary the re-engagement policy is built on, so the client is told
// to open a new ticket instead.
//
// "Closed" is CONFIGURATION since F2 (rider R3-001): a state is terminal when the
// ticket's type declares no transition out of it (domain.WorkflowConfig.
// IsTerminal), and the fixed 'cerrado' value is gone. The rule is enforced TWICE:
// read here for the readable rejection, and again by the guard inside the
// repository transaction — derived from the same configured transitions — which
// is what makes it hold under concurrency.
type AddTicketNote struct {
	tickets   TicketStore
	workflows domain.WorkflowConfigStore
}

// NewAddTicketNote wires the use case. The workflow store is required because the
// closed rule is read from the ticket's type configuration, not from a fixed
// state name.
func NewAddTicketNote(tickets TicketStore, workflows domain.WorkflowConfigStore) (*AddTicketNote, error) {
	if tickets == nil {
		return nil, errors.New("tickets: se requiere un TicketStore para anotar tickets")
	}
	if workflows == nil {
		return nil, errors.New("tickets: se requiere un WorkflowConfigStore para anotar tickets")
	}
	return &AddTicketNote{tickets: tickets, workflows: workflows}, nil
}

// Execute appends the note and returns the ticket with its updated history.
//
// The authorization (ownership, caller identity) and the readable rejections are
// decided here; the ATOMICITY belongs to the repository, which writes the event,
// the activity of the ticket and the readback in one transaction under the
// open-state guard. The read below is what phrases "not found" and "closed" for
// a human, and the guard is what still holds when another actor closes the
// ticket between that read and the write.
func (uc *AddTicketNote) Execute(ctx context.Context, caller auth.Caller, note dto.TicketNote) (dto.TicketNoteResult, error) {
	if err := requireTicketReader(caller); err != nil {
		return dto.TicketNoteResult{}, err
	}

	text, err := validateTicketNoteText(note.Text)
	if err != nil {
		return dto.TicketNoteResult{}, err
	}

	ticketID := strings.TrimSpace(note.TicketID)
	if ticketID == "" {
		return dto.TicketNoteResult{}, errTicketIDRequired
	}

	ticket, found, err := uc.tickets.GetTicket(ctx, ticketID)
	if err != nil {
		return dto.TicketNoteResult{}, fmt.Errorf("tickets: leer el ticket: %w", err)
	}
	if !found {
		return dto.TicketNoteResult{}, errTicketNotFound
	}

	if !caller.Role.IsInternal() && ticket.ClientID != ticketActor(caller) {
		// A client naming somebody else's ticket gets the SAME rejection as a
		// ticket that does not exist: the answer must not confirm which ids are
		// tickets of this business (no oracle).
		return dto.TicketNoteResult{}, errTicketNotFound
	}

	config, err := loadWorkflow(ctx, uc.workflows, ticket.TicketType)
	if err != nil {
		return dto.TicketNoteResult{}, err
	}
	if config.IsTerminal(string(ticket.State)) {
		return dto.TicketNoteResult{}, errTicketClosed
	}

	payload, err := encodeTicketEventPayload(ticketNotePayload{Text: maskPhoneRuns(text)})
	if err != nil {
		return dto.TicketNoteResult{}, fmt.Errorf("tickets: anotar el ticket: %w", err)
	}

	updated, applied, err := uc.tickets.AddTicketNote(ctx, dto.TicketNoteAppend{
		TicketID: ticket.ID,
		Event: dto.NewTicketEvent{
			TicketID: ticket.ID,
			Actor:    ticketActor(caller),
			Kind:     dto.TicketEventKindNote,
			Payload:  payload,
		},
	})
	if err != nil {
		return dto.TicketNoteResult{}, fmt.Errorf("tickets: anotar el ticket: %w", err)
	}
	if !applied {
		// The open-state guard refused the write: another actor closed (or removed)
		// the ticket between the read above and the write. The rejection is the one
		// that path already owns, never a silent insertion into a closed history.
		return dto.TicketNoteResult{}, uc.rejectStaleNote(ctx, ticket.ID)
	}

	events, err := uc.tickets.ListTicketEvents(ctx, ticket.ID)
	if err != nil {
		return dto.TicketNoteResult{}, fmt.Errorf("tickets: leer el historial del ticket: %w", err)
	}
	return dto.TicketNoteResult{Ticket: updated, Events: events}, nil
}

// rejectStaleNote phrases the rejection of a note the open-state guard refused.
//
// The guard is binary (the row is open or it is not), so one more read tells
// "closed meanwhile" apart from "gone", which is what lets the caller receive
// the same error the pre-write check would have produced.
func (uc *AddTicketNote) rejectStaleNote(ctx context.Context, ticketID string) error {
	ticket, found, err := uc.tickets.GetTicket(ctx, ticketID)
	if err != nil {
		return fmt.Errorf("tickets: leer el ticket: %w", err)
	}
	if !found {
		return errTicketNotFound
	}
	config, err := loadWorkflow(ctx, uc.workflows, ticket.TicketType)
	if err != nil {
		return err
	}
	if config.IsTerminal(string(ticket.State)) {
		return errTicketClosed
	}
	// Unreachable while the guard and IsTerminal are the same rule (the guard
	// refused because the state is terminal); kept as the honest fallback of a
	// guard whose reason we cannot name.
	return newSemanticError(CodeTicketStateChanged,
		"el ticket cambió mientras lo operabas: volvé a leerlo e intentá de nuevo.")
}

// TransitionTicket is the transition of RF5 behind transition_ticket, executed by
// the configured workflow of the ticket's type (RF6).
//
// F1 validated the target state against a fixed vocabulary; F2 asks the type's
// configuration instead — is the destination a declared state? does the config
// declare this exact from→to transition? — and refuses anything else, so the
// engine can never move a ticket along an edge nobody configured. The audit is
// unchanged: the state change and its event (with actor, from and to) are written
// in one transaction, so the history can never show a transition that did not
// happen or miss one that did.
type TransitionTicket struct {
	tickets   TicketStore
	workflows domain.WorkflowConfigStore
	rotation  AreaRotator
}

// NewTransitionTicket wires the use case. Every port is fail-closed: without the
// workflow store there is no engine (only a fixed vocabulary, which F2 retired),
// and without the rotation the round-robin rule of a configured transition could
// not resolve an assignee.
func NewTransitionTicket(tickets TicketStore, workflows domain.WorkflowConfigStore, rotation AreaRotator) (*TransitionTicket, error) {
	if tickets == nil {
		return nil, errors.New("tickets: se requiere un TicketStore para transicionar tickets")
	}
	if workflows == nil {
		return nil, errors.New("tickets: se requiere un WorkflowConfigStore para transicionar tickets")
	}
	if rotation == nil {
		return nil, errors.New("tickets: se requiere un AreaRotator para transicionar tickets")
	}
	return &TransitionTicket{tickets: tickets, workflows: workflows, rotation: rotation}, nil
}

// Execute moves the ticket through the transition the workflow declares.
func (uc *TransitionTicket) Execute(ctx context.Context, caller auth.Caller, cmd dto.TransitionCommand) (dto.TransitionTicketResult, error) {
	if err := requireTicketOperator(caller); err != nil {
		return dto.TransitionTicketResult{}, err
	}

	ticketID := strings.TrimSpace(cmd.TicketID)
	if ticketID == "" {
		return dto.TransitionTicketResult{}, errTicketIDRequired
	}

	ticket, found, err := uc.tickets.GetTicket(ctx, ticketID)
	if err != nil {
		return dto.TransitionTicketResult{}, fmt.Errorf("tickets: leer el ticket: %w", err)
	}
	if !found {
		return dto.TransitionTicketResult{}, errTicketNotFound
	}

	config, err := loadWorkflow(ctx, uc.workflows, ticket.TicketType)
	if err != nil {
		return dto.TransitionTicketResult{}, err
	}

	target := dto.TicketState(strings.TrimSpace(string(cmd.State)))
	if target == "" {
		return dto.TransitionTicketResult{}, invalidTicketStateError("indicá el estado destino del ticket.")
	}
	// One lookup answers both questions about the destination: does the type
	// declare it, and (when it does) what deadline does it carry.
	destination, declared := config.State(string(target))
	if !declared {
		return dto.TransitionTicketResult{}, invalidTicketStateError(fmt.Sprintf(
			"el estado '%s' no existe en el workflow del tipo '%s'.", target, config.TypeName))
	}
	if ticket.State == target {
		return dto.TransitionTicketResult{}, invalidTicketStateError(fmt.Sprintf(
			"el ticket ya está en el estado '%s'.", target))
	}

	transition, declared := config.Transition(string(ticket.State), string(target))
	if !declared {
		return dto.TransitionTicketResult{}, invalidTicketStateError(fmt.Sprintf(
			"no se puede pasar el ticket del estado '%s' al '%s': esa transición no está declarada en el workflow del tipo '%s'.",
			ticket.State, target, config.TypeName))
	}

	// RF6: "rol autorizado por transición". The transport and requireTicketOperator
	// already narrowed the caller to an internal role; this is the PER-TRANSITION
	// minimum the Owner configured, and a role below it is refused with the
	// semantic role rejection (a client never gets here: it is not a transition
	// role at all, so the same check would refuse it before this message).
	if !transition.CanBeExecutedBy(transitionRoleOf(caller.Role)) {
		return dto.TransitionTicketResult{}, newSemanticError(CodeRoleNotAllowed, fmt.Sprintf(
			"tu rol no permite pasar un ticket del estado '%s' al '%s': el workflow del tipo '%s' reserva esa transición al rol %s o superior.",
			ticket.State, target, config.TypeName, transition.MinRole))
	}

	// RF6: "auto-asignación round-robin del área". The rotation is asked only when
	// the transition declares it AND the ticket sits in an area; an area with no
	// members answers "" and the ticket stays in the queue.
	assignee := ""
	if transition.RoundRobin && ticket.AreaID != "" {
		assignee, err = uc.rotation.NextAreaAssignee(ctx, ticket.AreaID)
		if err != nil {
			return dto.TransitionTicketResult{}, fmt.Errorf("tickets: resolver el asignado del área: %w", err)
		}
	}

	payload, err := encodeTicketEventPayload(ticketTransitionPayload{
		From:       ticket.State,
		To:         target,
		AssignedTo: assignee,
	})
	if err != nil {
		return dto.TransitionTicketResult{}, fmt.Errorf("tickets: transicionar el ticket: %w", err)
	}

	applied, err := uc.tickets.TransitionTicket(ctx, dto.TicketTransition{
		TicketID: ticket.ID,
		From:     ticket.State,
		To:       target,
		// The three computed columns are configuration answers, decided here and
		// written by the adapter in the same guarded statement as the state.
		DeadlineAt: nextDeadline(destination, time.Now()),
		Closes:     config.IsTerminal(string(target)),
		AssignTo:   assignee,
		Event: dto.NewTicketEvent{
			TicketID: ticket.ID,
			Actor:    ticketActor(caller),
			Kind:     dto.TicketEventKindTransition,
			Payload:  payload,
		},
	})
	if err != nil {
		return dto.TransitionTicketResult{}, fmt.Errorf("tickets: transicionar el ticket: %w", err)
	}
	if !applied {
		// Another actor moved the ticket between the read and the guarded write.
		// The answer is a rejection, never a silent overwrite.
		return dto.TransitionTicketResult{}, newSemanticError(CodeTicketStateChanged,
			"el ticket cambió mientras lo operabas: volvé a leerlo e intentá de nuevo.")
	}

	updated, found, err := uc.tickets.GetTicket(ctx, ticket.ID)
	if err != nil {
		return dto.TransitionTicketResult{}, fmt.Errorf("tickets: leer el ticket transicionado: %w", err)
	}
	if !found {
		return dto.TransitionTicketResult{}, fmt.Errorf("tickets: el ticket %s no se encontró después de transicionarlo", ticket.ID)
	}

	events, err := uc.tickets.ListTicketEvents(ctx, ticket.ID)
	if err != nil {
		return dto.TransitionTicketResult{}, fmt.Errorf("tickets: leer el historial del ticket: %w", err)
	}
	return dto.TransitionTicketResult{Ticket: updated, Events: events}, nil
}

// The event payload documents. They are typed structs instead of maps so a
// payload key cannot be invented at a call site, and so the audit of a kind always
// has the same shape.
type (
	// ticketCreatedPayload is the opening event of a ticket. It names the type by
	// its configured NAME, not by its internal UUID: the type is what the engine
	// loads the config by, and the name survives a re-seeded row.
	ticketCreatedPayload struct {
		TicketType string          `json:"ticket_type"`
		State      dto.TicketState `json:"state"`
		Motive     string          `json:"motive"`
	}

	// ticketNotePayload is a note or re-engagement message.
	ticketNotePayload struct {
		Text string `json:"text"`
	}

	// ticketTransitionPayload is a state change. AssignedTo is present only when
	// the transition's round-robin rule assigned the ticket: RF5 audits every
	// assignment change, and the assignment happens as part of the transition.
	ticketTransitionPayload struct {
		From       dto.TicketState `json:"from"`
		To         dto.TicketState `json:"to"`
		AssignedTo string          `json:"assigned_to,omitempty"`
	}
)

// encodeTicketEventPayload marshals one event payload document.
func encodeTicketEventPayload(payload any) (string, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("serializar el evento del ticket: %w", err)
	}
	return string(encoded), nil
}

// validateTicketMotive trims the motive and applies its rules.
func validateTicketMotive(motive string) (string, error) {
	trimmed := strings.TrimSpace(motive)
	if trimmed == "" {
		return "", newSemanticError(CodeInvalidTicket,
			"el motivo del ticket es obligatorio: un mensaje sin motivo de negocio no genera ticket.")
	}
	if utf8.RuneCountInString(trimmed) > MaxTicketMotiveLen {
		return "", newSemanticError(CodeInvalidTicket, fmt.Sprintf(
			"el motivo del ticket no puede superar los %d caracteres.", MaxTicketMotiveLen))
	}
	return trimmed, nil
}

// validateTicketNoteText trims the note and applies its rules.
func validateTicketNoteText(text string) (string, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "", newSemanticError(CodeInvalidTicket, "el texto de la nota es obligatorio.")
	}
	if utf8.RuneCountInString(trimmed) > MaxTicketNoteLen {
		return "", newSemanticError(CodeInvalidTicket, fmt.Sprintf(
			"la nota no puede superar los %d caracteres.", MaxTicketNoteLen))
	}
	return trimmed, nil
}

// invalidTicketStateError builds a state-level rejection: the caller branches on
// the code, a human reads the prose.
func invalidTicketStateError(message string) error {
	return newSemanticError(CodeInvalidTicketState, message)
}

// maskPhoneRuns masks every digit cluster long enough to be a phone number,
// keeping its last maskedSuffixDigits digits ("+5491155550000" → "****0000").
//
// It exists because the free text a human writes lands in the audit (the motive of
// the contact, the body of a note) and AGENTS.md forbids retaining raw PII in an
// audit row. The rule is the product's masking rule — last 4 digits — and the
// caller-supplied text is never trusted to carry a mask of its own: whatever the
// client typed is re-masked here.
//
// It is a heuristic, and the honest limits are worth stating: masking works on
// digit CLUSTERS, not isolated runs — a phone split across separators
// ("9 11 5555-0000", runs of 1/2/4/4) is one cluster and is masked as a whole
// (correction R3-mask-split-run: per-run masking left separator-split phones
// verbatim). A cluster is a maximal span of digits interleaved with common phone
// separators (space, dash, dot, parentheses, slash, plus) whose TOTAL digit count
// reaches the threshold; its digits keep the last 4 and mask the rest, separators
// stay as written. A long number that is not a phone (a 12-digit reference) is
// masked too. Over-masking a number in an audit note is the safe direction;
// retaining a phone verbatim is not.
func maskPhoneRuns(text string) string {
	runes := []rune(text)
	var builder strings.Builder
	builder.Grow(len(text))

	for index := 0; index < len(runes); {
		if !unicode.IsDigit(runes[index]) {
			builder.WriteRune(runes[index])
			index++
			continue
		}
		// Grow the cluster: digits interleaved with common phone separators.
		end := index
		digits := 0
		for end < len(runes) {
			if unicode.IsDigit(runes[end]) {
				digits++
				end++
				continue
			}
			if end+1 < len(runes) && unicode.IsDigit(runes[end+1]) && isPhoneSeparator(runes[end]) {
				end++
				continue
			}
			break
		}
		if digits < minMaskedDigitRun {
			// Not phone-shaped as a whole: copy the cluster verbatim.
			builder.WriteString(string(runes[index:end]))
			index = end
			continue
		}
		// Mask the cluster keeping its LAST 4 digits; separators stay as written.
		digitIndex := 0
		var cluster strings.Builder
		for scan := index; scan < end; scan++ {
			if unicode.IsDigit(runes[scan]) {
				if digits-digitIndex <= maskedSuffixDigits {
					cluster.WriteRune(runes[scan])
				} else {
					cluster.WriteRune('*')
				}
				digitIndex++
			} else {
				cluster.WriteRune(runes[scan])
			}
		}
		builder.WriteString(cluster.String())
		index = end
	}
	return builder.String()
}

// isPhoneSeparator reports whether a rune commonly separates phone digit groups.
// Slash is deliberately absent: dates like 26/11/2026 are not phone clusters.
func isPhoneSeparator(r rune) bool {
	switch r {
	case ' ', '-', '.', '(', ')', '+':
		return true
	}
	return false
}

// --- The engine's boundary helpers -----------------------------------------

// loadWorkflow resolves the configuration of a ticket type and proves it is
// executable before anything is written.
//
// The two rejections are deliberately different:
//
//   - the type does not exist (found=false): no business configured it, so the
//     engine fails closed with "no existe el tipo de ticket" instead of falling
//     back to a fixed vocabulary (RF6 retired it).
//   - the type exists but its stored config does not validate: the engine refuses
//     to execute half a workflow, and the rejection carries the domain's own
//     Spanish message (which names the broken part) so the Owner can fix it.
//
// An empty name is the input default (the general consultation), never a
// fallback for a config that is missing.
func loadWorkflow(ctx context.Context, workflows domain.WorkflowConfigStore, typeName string) (domain.WorkflowConfig, error) {
	typeName = strings.TrimSpace(typeName)
	if typeName == "" {
		typeName = defaultTicketTypeName
	}

	config, found, err := workflows.LoadConfig(ctx, typeName)
	if err != nil {
		return domain.WorkflowConfig{}, fmt.Errorf("tickets: leer el workflow del tipo %q: %w", typeName, err)
	}
	if !found {
		return domain.WorkflowConfig{}, newSemanticError(CodeUnknownTicketType, fmt.Sprintf(
			"no existe el tipo de ticket '%s'.", typeName))
	}
	if err := config.Validate(); err != nil {
		return domain.WorkflowConfig{}, newSemanticError(CodeUnusableWorkflow, fmt.Sprintf(
			"el workflow del tipo '%s' no se puede ejecutar: %s", typeName, configErrorMessage(err)))
	}
	return config, nil
}

// configErrorMessage renders the domain's validation message. The error is always
// a semantic one (Validate phrases every rejection with NewSemanticError), and the
// fallback exists only so a future non-semantic cause cannot leak internals into
// the rejection.
func configErrorMessage(err error) string {
	var semantic *domain.SemanticError
	if errors.As(err, &semantic) {
		return semantic.Message()
	}
	return "la configuración no es válida."
}

// transitionRoleOf maps a resolved auth.Role onto the domain's transition-role
// vocabulary.
//
// The mapping lives here because the dependency rule forbids internal/domain from
// importing internal/auth: the domain names the roles a transition may require
// (staff < admin < owner) and this boundary names the roles a caller can hold. A
// client — and an unresolved caller — maps to the empty value, which is not a
// transition role at all, so TransitionConfig.CanBeExecutedBy refuses it without a
// special case. That is the RF6 criterion ("cuando un client intenta ejecutarla,
// entonces es rechazada con FORBIDDEN semántico") executed as a role comparison
// instead of as a role special case.
func transitionRoleOf(role auth.Role) domain.TransitionRole {
	switch role {
	case auth.RoleStaff:
		return domain.TransitionRoleStaff
	case auth.RoleAdmin:
		return domain.TransitionRoleAdmin
	case auth.RoleOwner:
		return domain.TransitionRoleOwner
	default:
		return ""
	}
}

// nextDeadline is the deadline of a ticket that enters state at now: the state's
// deadline_hours added to the instant of the transition (RF6: "`deadline` por
// estado con aviso overdue").
//
// An empty answer means the state declares no deadline, which the column stores as
// NULL — and which CLEARS the deadline of the previous state, so the scheduler
// never compares an instant that belongs to a state the ticket already left.
func nextDeadline(state domain.StateConfig, now time.Time) string {
	if state.DeadlineHours == nil {
		return ""
	}
	return now.UTC().Add(time.Duration(*state.DeadlineHours) * time.Hour).Format(ticketTimestampLayout)
}
