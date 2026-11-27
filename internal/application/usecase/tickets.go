package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/egkike/crm-ai-business/internal/application/dto"
	"github.com/egkike/crm-ai-business/internal/auth"
)

// This file is the application layer of RF5 (tickets con historial auditable,
// F1 con un tipo fijo): creation, the two list views, the note / re-engagement
// path and the staff transition.
//
// Every use case re-asserts its own authorization (AGENTS.md boundary contract 1,
// double RBAC): the MCP transport already filtered the tool by role, and the
// re-assertion is the independent barrier that still holds when the same use case
// is reached from another adapter (the internal panel of F4) or when the
// transport's permission map is edited by mistake.
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
	// It reports found=false when the guard refused (the ticket is gone or is
	// closed), in which case nothing was written. It is one operation instead of
	// append + touch + read because a note that is audited while the ticket's
	// activity stays put leaves the F2 inactivity clock stale, and a note that
	// lands after a concurrent close reopens a closed conversation.
	AddTicketNote(ctx context.Context, note dto.TicketNoteAppend) (dto.TicketRecord, bool, error)

	// ListTicketEvents returns a ticket's history, oldest first.
	ListTicketEvents(ctx context.Context, ticketID string) ([]dto.TicketEventRecord, error)
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
// that neither buys nor requests anything. The use case therefore asks for a
// motive and nothing else: what the client needs is audited in the opening event,
// and the state starts at the initial state of the (F1: single) ticket type.
type CreateTicket struct {
	tickets TicketStore
	clients ClientDirectory
}

// NewCreateTicket wires the use case. A nil port is a fail-closed configuration
// error: without persistence the use case could only fail, and without the client
// directory the internal "on behalf of" path could not tell a real client from a
// typo.
func NewCreateTicket(tickets TicketStore, clients ClientDirectory) (*CreateTicket, error) {
	if tickets == nil {
		return nil, errors.New("tickets: se requiere un TicketStore para crear tickets")
	}
	if clients == nil {
		return nil, errors.New("tickets: se requiere un ClientDirectory para crear tickets")
	}
	return &CreateTicket{tickets: tickets, clients: clients}, nil
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

	id, err := newUUID()
	if err != nil {
		return dto.CreateTicketResult{}, fmt.Errorf("tickets: crear el ticket: %w", err)
	}

	payload, err := encodeTicketEventPayload(ticketCreatedPayload{
		TicketTypeID: dto.ConsultaTicketTypeID,
		State:        dto.TicketStateInitial,
		Motive:       maskPhoneRuns(motive),
	})
	if err != nil {
		return dto.CreateTicketResult{}, fmt.Errorf("tickets: crear el ticket: %w", err)
	}

	row := dto.NewTicket{
		ID:           id,
		TicketTypeID: dto.ConsultaTicketTypeID,
		ClientID:     clientID,
		State:        dto.TicketStateInitial,
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
// to open a new ticket instead. That rule is enforced TWICE: read here for the
// readable rejection, and again by the open-state guard inside the repository
// transaction, which is what makes it hold under concurrency.
type AddTicketNote struct {
	tickets TicketStore
}

// NewAddTicketNote wires the use case.
func NewAddTicketNote(tickets TicketStore) (*AddTicketNote, error) {
	if tickets == nil {
		return nil, errors.New("tickets: se requiere un TicketStore para anotar tickets")
	}
	return &AddTicketNote{tickets: tickets}, nil
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

	if ticket.State.Closed() {
		return dto.TicketNoteResult{}, errTicketClosed
	}

	payload, err := encodeTicketEventPayload(ticketNotePayload{Text: maskPhoneRuns(text)})
	if err != nil {
		return dto.TicketNoteResult{}, fmt.Errorf("tickets: anotar el ticket: %w", err)
	}

	updated, applied, err := uc.tickets.AddTicketNote(ctx, dto.TicketNoteAppend{
		TicketID:    ticket.ID,
		ClosedState: dto.TicketStateClosed,
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
	if ticket.State.Closed() {
		return errTicketClosed
	}
	// Unreachable in F1 (the only refused state is the closed one) and kept as the
	// honest fallback of a guard whose reason we cannot name.
	return newSemanticError(CodeTicketStateChanged,
		"el ticket cambió mientras lo operabas: volvé a leerlo e intentá de nuevo.")
}

// TransitionTicket is the staff transition of RF5 behind transition_ticket.
//
// F1 validates the target state against the fixed vocabulary and nothing else: it
// does not implement a workflow. Which transitions are legal, who may execute
// them and what fires on enter/exit is the F2 engine (RF6), configured per ticket
// type. What F1 does guarantee is the audit: the state change and its event (with
// actor, from and to) are written in one transaction, so the history can never
// show a transition that did not happen or miss one that did.
type TransitionTicket struct {
	tickets TicketStore
}

// NewTransitionTicket wires the use case.
func NewTransitionTicket(tickets TicketStore) (*TransitionTicket, error) {
	if tickets == nil {
		return nil, errors.New("tickets: se requiere un TicketStore para transicionar tickets")
	}
	return &TransitionTicket{tickets: tickets}, nil
}

// Execute moves the ticket to cmd.State.
func (uc *TransitionTicket) Execute(ctx context.Context, caller auth.Caller, cmd dto.TransitionCommand) (dto.TransitionTicketResult, error) {
	if err := requireTicketOperator(caller); err != nil {
		return dto.TransitionTicketResult{}, err
	}

	if !cmd.State.Valid() {
		return dto.TransitionTicketResult{}, invalidTicketStateError(
			"el estado destino no es válido: en F1 los estados posibles son nuevo, atendido y cerrado.")
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
	if ticket.State == cmd.State {
		return dto.TransitionTicketResult{}, invalidTicketStateError(fmt.Sprintf(
			"el ticket ya está en el estado %q.", string(cmd.State)))
	}

	payload, err := encodeTicketEventPayload(ticketTransitionPayload{From: ticket.State, To: cmd.State})
	if err != nil {
		return dto.TransitionTicketResult{}, fmt.Errorf("tickets: transicionar el ticket: %w", err)
	}

	applied, err := uc.tickets.TransitionTicket(ctx, dto.TicketTransition{
		TicketID: ticket.ID,
		From:     ticket.State,
		To:       cmd.State,
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
	// ticketCreatedPayload is the opening event of a ticket.
	ticketCreatedPayload struct {
		TicketTypeID string          `json:"ticket_type_id"`
		State        dto.TicketState `json:"state"`
		Motive       string          `json:"motive"`
	}

	// ticketNotePayload is a note or re-engagement message.
	ticketNotePayload struct {
		Text string `json:"text"`
	}

	// ticketTransitionPayload is a state change.
	ticketTransitionPayload struct {
		From dto.TicketState `json:"from"`
		To   dto.TicketState `json:"to"`
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
