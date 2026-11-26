package usecase_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/egkike/crm-ai-business/internal/application/dto"
	"github.com/egkike/crm-ai-business/internal/application/usecase"
	"github.com/egkike/crm-ai-business/internal/auth"
)

// fakeTickets is the TicketStore double: it records the commands the use cases
// send and replays the configured answers. The use case tests exercise decisions
// (RBAC, ownership, validation, audit contents), so the port must be answerable;
// the real SQL is pinned by the repository tests.
type fakeTickets struct {
	tickets map[string]dto.TicketRecord
	events  map[string][]dto.TicketEventRecord
	nextID  int64
	clock   int

	created     []dto.NewTicket
	appended    []dto.NewTicketEvent
	transitions []dto.TicketTransition
	touched     []string

	listQueries   []dto.TicketQuery
	clientQueries []clientQuery

	createErr     error
	getErr        error
	listErr       error
	transitionErr error
	noteErr       error

	// refuseTransition makes the guarded write report "the ticket moved", which is
	// how the use case sees a concurrent actor.
	refuseTransition bool

	// closeOnNote makes the guarded note write lose the race against a concurrent
	// close: the store closes the ticket and reports "not open", so the use case
	// has to phrase the rejection from what it reads afterwards.
	closeOnNote bool
}

// clientQuery records one ListTicketsByClient call.
type clientQuery struct {
	clientID      string
	includeClosed bool
	limit         int
	offset        int
}

func newFakeTickets() *fakeTickets {
	return &fakeTickets{
		tickets: map[string]dto.TicketRecord{},
		events:  map[string][]dto.TicketEventRecord{},
	}
}

// seed inserts a ticket (and its opening event) as if a previous call had done it.
func (f *fakeTickets) seed(ticket dto.TicketRecord) {
	if ticket.State == "" {
		ticket.State = dto.TicketStateInitial
	}
	if ticket.TicketTypeID == "" {
		ticket.TicketTypeID = dto.ConsultaTicketTypeID
	}
	ticket.CreatedAt, ticket.UpdatedAt = f.stamp()
	f.tickets[ticket.ID] = ticket
	f.append(ticket.ID, ticket.ClientID, dto.TicketEventKindCreated, `{"motive":"consulta"}`)
}

// stamp returns the next fake timestamp.
func (f *fakeTickets) stamp() (string, string) {
	f.clock++
	value := fmt.Sprintf("2026-01-01T00:00:%02d.000Z", f.clock)
	return value, value
}

// append stores one event and returns it.
func (f *fakeTickets) append(ticketID, actor, kind, payload string) dto.TicketEventRecord {
	f.nextID++
	createdAt, _ := f.stamp()
	record := dto.TicketEventRecord{
		ID:        f.nextID,
		TicketID:  ticketID,
		Actor:     actor,
		Kind:      kind,
		Payload:   payload,
		CreatedAt: createdAt,
	}
	f.events[ticketID] = append(f.events[ticketID], record)
	return record
}

func (f *fakeTickets) CreateTicket(_ context.Context, ticket dto.NewTicket) error {
	if f.createErr != nil {
		return f.createErr
	}
	f.created = append(f.created, ticket)

	createdAt, updatedAt := f.stamp()
	f.tickets[ticket.ID] = dto.TicketRecord{
		ID:           ticket.ID,
		TicketTypeID: ticket.TicketTypeID,
		ClientID:     ticket.ClientID,
		State:        ticket.State,
		CreatedAt:    createdAt,
		UpdatedAt:    updatedAt,
	}
	f.append(ticket.ID, ticket.OpeningEvent.Actor, ticket.OpeningEvent.Kind, ticket.OpeningEvent.Payload)
	return nil
}

func (f *fakeTickets) GetTicket(_ context.Context, id string) (dto.TicketRecord, bool, error) {
	if f.getErr != nil {
		return dto.TicketRecord{}, false, f.getErr
	}
	record, ok := f.tickets[id]
	return record, ok, nil
}

func (f *fakeTickets) ListTicketsByClient(_ context.Context, clientID string, includeClosed bool, limit, offset int) ([]dto.TicketRecord, error) {
	f.clientQueries = append(f.clientQueries, clientQuery{clientID: clientID, includeClosed: includeClosed, limit: limit, offset: offset})
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.page(true, clientID, includeClosed, limit, offset), nil
}

func (f *fakeTickets) ListTickets(_ context.Context, query dto.TicketQuery) ([]dto.TicketRecord, error) {
	f.listQueries = append(f.listQueries, query)
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.page(false, query.ClientID, query.IncludeClosed, query.Limit, query.Offset), nil
}

// page filters the stored tickets; byClient tells whether the client filter was
// the explicit ListTicketsByClient call.
func (f *fakeTickets) page(byClient bool, clientID string, includeClosed bool, limit, offset int) []dto.TicketRecord {
	matches := make([]dto.TicketRecord, 0, len(f.tickets))
	for _, ticket := range f.tickets {
		if byClient && ticket.ClientID != clientID {
			continue
		}
		if !includeClosed && ticket.State.Closed() {
			continue
		}
		matches = append(matches, ticket)
	}
	if offset >= len(matches) {
		return nil
	}
	matches = matches[offset:]
	if limit < len(matches) {
		matches = matches[:limit]
	}
	return matches
}

func (f *fakeTickets) TransitionTicket(_ context.Context, transition dto.TicketTransition) (bool, error) {
	if f.transitionErr != nil {
		return false, f.transitionErr
	}
	f.transitions = append(f.transitions, transition)
	if f.refuseTransition {
		return false, nil
	}

	current, ok := f.tickets[transition.TicketID]
	if !ok || current.State != transition.From {
		return false, nil
	}
	current.State = transition.To
	_, updatedAt := f.stamp()
	current.UpdatedAt = updatedAt
	f.tickets[transition.TicketID] = current
	f.append(transition.TicketID, transition.Event.Actor, transition.Event.Kind, transition.Event.Payload)
	return true, nil
}

// AddTicketNote is the atomic note write: it refuses a ticket that is not open
// (closing it first when closeOnNote simulates the race) and otherwise stores the
// event AND moves the activity, exactly like the repository transaction.
func (f *fakeTickets) AddTicketNote(_ context.Context, note dto.TicketNoteAppend) (dto.TicketRecord, bool, error) {
	if f.noteErr != nil {
		return dto.TicketRecord{}, false, f.noteErr
	}

	current, ok := f.tickets[note.TicketID]
	if !ok || current.State.Closed() {
		return dto.TicketRecord{}, false, nil
	}
	if f.closeOnNote {
		// Another actor's close won the race after the use case read the ticket.
		current.State = dto.TicketStateClosed
		f.tickets[note.TicketID] = current
		return dto.TicketRecord{}, false, nil
	}

	f.appended = append(f.appended, note.Event)
	f.touched = append(f.touched, note.TicketID)
	_, updatedAt := f.stamp()
	current.UpdatedAt = updatedAt
	f.tickets[note.TicketID] = current
	f.append(note.TicketID, note.Event.Actor, note.Event.Kind, note.Event.Payload)
	return current, true, nil
}

func (f *fakeTickets) ListTicketEvents(_ context.Context, ticketID string) ([]dto.TicketEventRecord, error) {
	return append([]dto.TicketEventRecord(nil), f.events[ticketID]...), nil
}

// fakeClientDirectory is the ClientDirectory double.
type fakeClientDirectory struct {
	known    map[string]bool
	errors   error
	lookedUp []string
}

func newFakeClientDirectory(ids ...string) *fakeClientDirectory {
	clients := &fakeClientDirectory{known: map[string]bool{}}
	for _, id := range ids {
		clients.known[id] = true
	}
	return clients
}

func (f *fakeClientDirectory) ClientExistsByID(_ context.Context, id string) (bool, error) {
	f.lookedUp = append(f.lookedUp, id)
	if f.errors != nil {
		return false, f.errors
	}
	return f.known[id], nil
}

// The identities of the ticket tests.
const (
	clientA = "aaaaaaaa-aaaa-4aaa-8aaa-00000000000a"
	clientB = "bbbbbbbb-bbbb-4bbb-8bbb-00000000000b"
	staffID = "cccccccc-cccc-4ccc-8ccc-00000000000c"
)

// ticketCaller builds a resolved caller with the given id and role.
func ticketCaller(id string, role auth.Role) auth.Caller {
	return auth.Caller{ID: id, Name: "Prueba", Role: role}
}

// createTicketUC wires the creation use case over the doubles.
func createTicketUC(t *testing.T, store *fakeTickets, clients *fakeClientDirectory) *usecase.CreateTicket {
	t.Helper()
	uc, err := usecase.NewCreateTicket(store, clients)
	if err != nil {
		t.Fatalf("NewCreateTicket() error inesperado: %v", err)
	}
	return uc
}

// payloadOf decodes an event payload into a generic object.
func payloadOf(t *testing.T, payload string) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		t.Fatalf("payload %q no es un documento JSON: %v", payload, err)
	}
	return decoded
}

// TestNewTicketUseCasesRequireTheirPorts pins the fail-closed construction rule of
// the five use cases: a nil port is a wiring error, never a degraded use case.
func TestNewTicketUseCasesRequireTheirPorts(t *testing.T) {
	if _, err := usecase.NewCreateTicket(nil, newFakeClientDirectory()); err == nil {
		t.Fatal("NewCreateTicket(sin store) debía fallar")
	}
	if _, err := usecase.NewCreateTicket(newFakeTickets(), nil); err == nil {
		t.Fatal("NewCreateTicket(sin directorio de clientes) debía fallar")
	}
	if _, err := usecase.NewListMyTickets(nil); err == nil {
		t.Fatal("NewListMyTickets(sin store) debía fallar")
	}
	if _, err := usecase.NewListTickets(nil); err == nil {
		t.Fatal("NewListTickets(sin store) debía fallar")
	}
	if _, err := usecase.NewAddTicketNote(nil); err == nil {
		t.Fatal("NewAddTicketNote(sin store) debía fallar")
	}
	if _, err := usecase.NewTransitionTicket(nil); err == nil {
		t.Fatal("NewTransitionTicket(sin store) debía fallar")
	}
}

// TestCreateTicketClientAlwaysOpensItsOwn pins the anti-injection rule of the
// creation: a client-role caller naming another client in the body still gets the
// ticket attributed to ITSELF, because the identity travels in the request context
// and never in an argument.
func TestCreateTicketClientAlwaysOpensItsOwn(t *testing.T) {
	store := newFakeTickets()
	clients := newFakeClientDirectory(clientA, clientB)
	uc := createTicketUC(t, store, clients)

	result, err := uc.Execute(context.Background(), ticketCaller(clientA, auth.RoleClient), dto.CreateTicketCommand{
		Motive:   "consulta por corte de pelo",
		ClientID: clientB,
	})
	if err != nil {
		t.Fatalf("Execute() error inesperado: %v", err)
	}

	if result.Ticket.ClientID != clientA {
		t.Fatalf("client_id = %q, se esperaba el del caller %q (el body nunca decide la identidad)", result.Ticket.ClientID, clientA)
	}
	if len(clients.lookedUp) != 0 {
		t.Fatalf("el cliente del body se consultó: %v", clients.lookedUp)
	}
	if result.Ticket.State != dto.TicketStateInitial || result.Ticket.TicketTypeID != dto.ConsultaTicketTypeID {
		t.Fatalf("ticket = %+v, se esperaba el estado inicial del tipo fijo", result.Ticket)
	}

	// The creation event is audited: actor, kind and the motive.
	if len(result.Events) != 1 {
		t.Fatalf("eventos = %d, se esperaba el evento de creación", len(result.Events))
	}
	opening := result.Events[0]
	if opening.Kind != dto.TicketEventKindCreated || opening.Actor != clientA {
		t.Fatalf("evento = %+v, se esperaba la creación con actor %q", opening, clientA)
	}
	payload := payloadOf(t, opening.Payload)
	if payload["motive"] != "consulta por corte de pelo" {
		t.Fatalf("payload.motive = %v, se esperaba el motivo", payload["motive"])
	}
	if payload["state"] != string(dto.TicketStateInitial) {
		t.Fatalf("payload.state = %v, se esperaba el estado inicial", payload["state"])
	}
	if payload["ticket_type_id"] != dto.ConsultaTicketTypeID {
		t.Fatalf("payload.ticket_type_id = %v, se esperaba el tipo fijo", payload["ticket_type_id"])
	}
}

// TestCreateTicketInternalOpensOnBehalf pins the internal path: the staff member
// names the client, an unknown one is a semantic rejection, and a missing one is
// refused before any query.
func TestCreateTicketInternalOpensOnBehalf(t *testing.T) {
	t.Run("cliente existente", func(t *testing.T) {
		store := newFakeTickets()
		clients := newFakeClientDirectory(clientA)
		uc := createTicketUC(t, store, clients)

		result, err := uc.Execute(context.Background(), ticketCaller(staffID, auth.RoleStaff), dto.CreateTicketCommand{
			Motive:   "reclamo recibido por teléfono",
			ClientID: clientA,
		})
		if err != nil {
			t.Fatalf("Execute() error inesperado: %v", err)
		}
		if result.Ticket.ClientID != clientA {
			t.Fatalf("client_id = %q, se esperaba %q", result.Ticket.ClientID, clientA)
		}
		if len(result.Events) != 1 || result.Events[0].Actor != staffID {
			t.Fatalf("eventos = %+v, se esperaba la creación con el actor interno", result.Events)
		}
	})

	t.Run("cliente inexistente", func(t *testing.T) {
		store := newFakeTickets()
		clients := newFakeClientDirectory(clientA)
		uc := createTicketUC(t, store, clients)

		_, err := uc.Execute(context.Background(), ticketCaller(staffID, auth.RoleStaff), dto.CreateTicketCommand{
			Motive:   "reclamo",
			ClientID: clientB,
		})
		assertSemantic(t, err, usecase.CodeClientNotFound, "no existe un cliente con ese identificador.")
		if len(store.created) != 0 {
			t.Fatal("se creó un ticket para un cliente inexistente")
		}
	})

	t.Run("sin cliente", func(t *testing.T) {
		store := newFakeTickets()
		clients := newFakeClientDirectory(clientA)
		uc := createTicketUC(t, store, clients)

		_, err := uc.Execute(context.Background(), ticketCaller(staffID, auth.RoleStaff), dto.CreateTicketCommand{
			Motive: "reclamo",
		})
		assertSemantic(t, err, usecase.CodeInvalidTicket, "indique el cliente para el que se abre el ticket.")
		if len(clients.lookedUp) != 0 {
			t.Fatalf("se consultó un cliente vacío: %v", clients.lookedUp)
		}
	})
}

// TestCreateTicketValidatesTheMotive pins the motive rules: RF5's exception is the
// contact with no business motive, so an empty one is refused, and the cap keeps a
// transcript from becoming a ticket field.
func TestCreateTicketValidatesTheMotive(t *testing.T) {
	store := newFakeTickets()
	uc := createTicketUC(t, store, newFakeClientDirectory(clientA))
	caller := ticketCaller(clientA, auth.RoleClient)

	cases := []struct {
		name    string
		motive  string
		message string
	}{
		{
			name:    "vacío",
			motive:  "",
			message: "el motivo del ticket es obligatorio: un mensaje sin motivo de negocio no genera ticket.",
		},
		{
			name:    "solo espacios",
			motive:  "   \n\t ",
			message: "el motivo del ticket es obligatorio: un mensaje sin motivo de negocio no genera ticket.",
		},
		{name: "al límite", motive: strings.Repeat("á", 120)},
		{
			name:    "demasiado largo",
			motive:  strings.Repeat("á", 121),
			message: "el motivo del ticket no puede superar los 120 caracteres.",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := uc.Execute(context.Background(), caller, dto.CreateTicketCommand{Motive: tc.motive})
			if tc.message != "" {
				assertSemantic(t, err, usecase.CodeInvalidTicket, tc.message)
				return
			}
			if err != nil {
				t.Fatalf("Execute() error inesperado: %v", err)
			}
		})
	}

	if len(store.created) != 1 {
		t.Fatalf("tickets creados = %d, se esperaba solo el del motivo válido", len(store.created))
	}
	if motive := store.created[0].OpeningEvent.Payload; !strings.Contains(motive, strings.Repeat("á", 120)) {
		t.Fatalf("el motivo al límite no se guardó completo: %s", motive)
	}
}

// TestCreateTicketRefusesAnUnresolvedCaller pins the in-use-case RBAC of the
// creation: the transport already gated the tool, and the use case refuses an
// identity that is not resolved (or that has no internal id to audit).
func TestCreateTicketRefusesAnUnresolvedCaller(t *testing.T) {
	store := newFakeTickets()
	uc := createTicketUC(t, store, newFakeClientDirectory())

	for _, caller := range []auth.Caller{{}, {ID: clientA}, {Role: auth.RoleClient}} {
		_, err := uc.Execute(context.Background(), caller, dto.CreateTicketCommand{Motive: "consulta"})
		assertSemantic(t, err, usecase.CodeRoleNotAllowed, "tu rol no permite consultar tickets.")
	}
	if len(store.created) != 0 {
		t.Fatal("se creó un ticket sin identidad resuelta")
	}
}

// TestCreateTicketMasksPhonesInTheAudit pins AGENTS.md's PII rule for the audit
// payload: a phone the client typed into the motive is stored masked to its last
// four digits.
func TestCreateTicketMasksPhonesInTheAudit(t *testing.T) {
	store := newFakeTickets()
	uc := createTicketUC(t, store, newFakeClientDirectory(clientA))

	result, err := uc.Execute(context.Background(), ticketCaller(clientA, auth.RoleClient), dto.CreateTicketCommand{
		Motive: "llamame al +5491155550000 por favor",
	})
	if err != nil {
		t.Fatalf("Execute() error inesperado: %v", err)
	}

	payload := payloadOf(t, result.Events[0].Payload)
	motive, _ := payload["motive"].(string)
	if strings.Contains(motive, "5491155550000") || strings.Contains(motive, "115555") {
		t.Fatalf("motive = %q, no debe retener el teléfono completo", motive)
	}
	if !strings.Contains(motive, "****0000") {
		t.Fatalf("motive = %q, se esperaba el teléfono enmascarado en sus últimos 4 dígitos", motive)
	}
	// The rest of the message survives: masking is not truncation.
	if !strings.Contains(motive, "llamame al") || !strings.Contains(motive, "por favor") {
		t.Fatalf("motive = %q, el texto alrededor del teléfono debe sobrevivir", motive)
	}
}

// TestCreateTicketMasksSplitPhones pins the correction R3-mask-split-run: a phone
// written with separators ("9 11 5555-0000", digit runs of 1/2/4/4) is ONE cluster
// and is masked as a whole — per-run masking left it verbatim in the audit.
func TestCreateTicketMasksSplitPhones(t *testing.T) {
	store := newFakeTickets()
	uc := createTicketUC(t, store, newFakeClientDirectory(clientA))

	result, err := uc.Execute(context.Background(), ticketCaller(clientA, auth.RoleClient), dto.CreateTicketCommand{
		Motive: "mi numero es 9 11 5555-0000 llamenme",
	})
	if err != nil {
		t.Fatalf("Execute() error inesperado: %v", err)
	}

	payload := payloadOf(t, result.Events[0].Payload)
	motive, _ := payload["motive"].(string)
	if strings.Contains(motive, "5555") || strings.Contains(motive, "9 11") {
		t.Fatalf("motive = %q, el teléfono partido por separadores debe quedar enmascarado", motive)
	}
	if !strings.Contains(motive, "****-0000") {
		t.Fatalf("motive = %q, se esperaba el cluster enmascarado con los últimos 4 dígitos", motive)
	}
}

// TestCreateTicketKeepsShortNumbers pins the cluster threshold: short digit
// groups below the phone threshold stay verbatim (a date, a quantity, a street
// number) — masking is for phone-shaped clusters only.
func TestCreateTicketKeepsShortNumbers(t *testing.T) {
	store := newFakeTickets()
	uc := createTicketUC(t, store, newFakeClientDirectory(clientA))

	result, err := uc.Execute(context.Background(), ticketCaller(clientA, auth.RoleClient), dto.CreateTicketCommand{
		Motive: "compre 3 articulos el 26/11/2026 en la calle 1234",
	})
	if err != nil {
		t.Fatalf("Execute() error inesperado: %v", err)
	}

	payload := payloadOf(t, result.Events[0].Payload)
	motive, _ := payload["motive"].(string)
	if !strings.Contains(motive, "3 articulos") || !strings.Contains(motive, "26/11/2026") || !strings.Contains(motive, "calle 1234") {
		t.Fatalf("motive = %q, los números cortos no-phone deben sobrevivir verbatim", motive)
	}
}

// TestListMyTicketsClientSeesOnlyItsOwn pins the client view: the caller's id
// overwrites the body filter, so a cross-client query returns the caller's data and
// never somebody else's.
func TestListMyTicketsClientSeesOnlyItsOwn(t *testing.T) {
	store := newFakeTickets()
	store.seed(dto.TicketRecord{ID: "ticket-a", ClientID: clientA})
	store.seed(dto.TicketRecord{ID: "ticket-b", ClientID: clientB})

	uc, err := usecase.NewListMyTickets(store)
	if err != nil {
		t.Fatalf("NewListMyTickets() error inesperado: %v", err)
	}

	result, err := uc.Execute(context.Background(), ticketCaller(clientA, auth.RoleClient), dto.TicketQuery{ClientID: clientB})
	if err != nil {
		t.Fatalf("Execute() error inesperado: %v", err)
	}
	if len(result.Tickets) != 1 || result.Tickets[0].Ticket.ID != "ticket-a" {
		t.Fatalf("tickets = %+v, se esperaba solo el del caller", result.Tickets)
	}
	if len(store.clientQueries) != 1 || store.clientQueries[0].clientID != clientA {
		t.Fatalf("consultas = %+v, se esperaba el id del caller y no el del body", store.clientQueries)
	}
	if len(result.Tickets[0].Events) != 1 || result.Tickets[0].Events[0].Kind != dto.TicketEventKindCreated {
		t.Fatalf("el listado no lleva el historial del ticket: %+v", result.Tickets[0].Events)
	}

	// A client with no tickets gets an empty page, never somebody else's.
	empty, err := uc.Execute(context.Background(), ticketCaller("dddddddd-dddd-4ddd-8ddd-00000000000d", auth.RoleClient), dto.TicketQuery{})
	if err != nil {
		t.Fatalf("Execute() error inesperado: %v", err)
	}
	if len(empty.Tickets) != 0 {
		t.Fatalf("tickets = %+v, se esperaba una lista vacía", empty.Tickets)
	}
}

// TestListMyTicketsInternalMayFilterByClient pins the internal half: the filter is
// honored, and no filter lists the whole business.
func TestListMyTicketsInternalMayFilterByClient(t *testing.T) {
	store := newFakeTickets()
	store.seed(dto.TicketRecord{ID: "ticket-a", ClientID: clientA})
	store.seed(dto.TicketRecord{ID: "ticket-b", ClientID: clientB})

	uc, err := usecase.NewListMyTickets(store)
	if err != nil {
		t.Fatalf("NewListMyTickets() error inesperado: %v", err)
	}
	caller := ticketCaller(staffID, auth.RoleStaff)

	filtered, err := uc.Execute(context.Background(), caller, dto.TicketQuery{ClientID: clientB})
	if err != nil {
		t.Fatalf("Execute() error inesperado: %v", err)
	}
	if len(filtered.Tickets) != 1 || filtered.Tickets[0].Ticket.ID != "ticket-b" {
		t.Fatalf("tickets = %+v, se esperaba el del cliente filtrado", filtered.Tickets)
	}

	every, err := uc.Execute(context.Background(), caller, dto.TicketQuery{})
	if err != nil {
		t.Fatalf("Execute() error inesperado: %v", err)
	}
	if len(every.Tickets) != 2 {
		t.Fatalf("tickets = %d, se esperaban los dos del negocio", len(every.Tickets))
	}
	if len(store.listQueries) != 1 || store.listQueries[0].ClientID != "" {
		t.Fatalf("consultas = %+v, se esperaba el listado sin filtro de cliente", store.listQueries)
	}
}

// TestListMyTicketsNormalizesPagination pins the page policy: the default is small,
// an oversized request is capped and a negative offset is zero.
func TestListMyTicketsNormalizesPagination(t *testing.T) {
	cases := []struct {
		name       string
		query      dto.TicketQuery
		wantLimit  int
		wantOffset int
	}{
		{name: "sin límite usa el default", wantLimit: usecase.DefaultTicketListLimit},
		{name: "límite negativo usa el default", query: dto.TicketQuery{Limit: -4}, wantLimit: usecase.DefaultTicketListLimit},
		{name: "límite excesivo se recorta", query: dto.TicketQuery{Limit: 5000}, wantLimit: usecase.MaxTicketListLimit},
		{name: "offset negativo es cero", query: dto.TicketQuery{Offset: -7}, wantLimit: usecase.DefaultTicketListLimit, wantOffset: 0},
		{name: "valores válidos se respetan", query: dto.TicketQuery{Limit: 5, Offset: 10}, wantLimit: 5, wantOffset: 10},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeTickets()
			uc, err := usecase.NewListMyTickets(store)
			if err != nil {
				t.Fatalf("NewListMyTickets() error inesperado: %v", err)
			}

			result, err := uc.Execute(context.Background(), ticketCaller(clientA, auth.RoleClient), tc.query)
			if err != nil {
				t.Fatalf("Execute() error inesperado: %v", err)
			}
			if result.Limit != tc.wantLimit || result.Offset != tc.wantOffset {
				t.Fatalf("paginación = (%d, %d), se esperaba (%d, %d)", result.Limit, result.Offset, tc.wantLimit, tc.wantOffset)
			}
		})
	}
}

// TestListTicketsIsInternalOnly pins the double RBAC of the internal queue: the
// transport hid the tool, and the use case independently refuses a client role with
// the Spanish role rejection before touching persistence.
func TestListTicketsIsInternalOnly(t *testing.T) {
	store := newFakeTickets()
	uc, err := usecase.NewListTickets(store)
	if err != nil {
		t.Fatalf("NewListTickets() error inesperado: %v", err)
	}

	for _, role := range []auth.Role{auth.RoleClient, ""} {
		caller := ticketCaller(clientA, role)
		_, err := uc.Execute(context.Background(), caller, dto.TicketQuery{})
		assertSemantic(t, err, usecase.CodeRoleNotAllowed, "tu rol no permite listar los tickets del negocio.")
	}
	if len(store.listQueries) != 0 {
		t.Fatalf("consultas = %+v, no debe llegar ninguna al repositorio", store.listQueries)
	}

	for _, role := range []auth.Role{auth.RoleOwner, auth.RoleAdmin, auth.RoleStaff} {
		t.Run(string(role), func(t *testing.T) {
			if _, err := uc.Execute(context.Background(), ticketCaller(staffID, role), dto.TicketQuery{}); err != nil {
				t.Fatalf("Execute(%q) error inesperado: %v", role, err)
			}
		})
	}
}

// TestAddTicketNoteOnOwnOpenTicket pins the re-engagement base: the client's message
// becomes an event of the existing open ticket, the ticket's activity moves, and no
// new ticket is created.
func TestAddTicketNoteOnOwnOpenTicket(t *testing.T) {
	store := newFakeTickets()
	store.seed(dto.TicketRecord{ID: "ticket-a", ClientID: clientA})

	uc, err := usecase.NewAddTicketNote(store)
	if err != nil {
		t.Fatalf("NewAddTicketNote() error inesperado: %v", err)
	}

	result, err := uc.Execute(context.Background(), ticketCaller(clientA, auth.RoleClient), dto.TicketNote{
		TicketID: "ticket-a",
		Text:     "  vuelvo por lo mismo, mi número es 1155550000  ",
	})
	if err != nil {
		t.Fatalf("Execute() error inesperado: %v", err)
	}

	if len(store.created) != 0 {
		t.Fatal("la nota abrió un ticket nuevo: el re-engagement nunca debe crear otro")
	}
	if len(result.Events) != 2 {
		t.Fatalf("eventos = %d, se esperaba la creación y la nota", len(result.Events))
	}
	note := result.Events[1]
	if note.Kind != dto.TicketEventKindNote || note.Actor != clientA {
		t.Fatalf("nota = %+v, se esperaba una nota con el actor del caller", note)
	}
	payload := payloadOf(t, note.Payload)
	text, _ := payload["text"].(string)
	if strings.Contains(text, "1155550000") {
		t.Fatalf("text = %q, no debe retener el teléfono completo", text)
	}
	if !strings.Contains(text, "****0000") {
		t.Fatalf("text = %q, se esperaba el teléfono enmascarado", text)
	}
	if !strings.Contains(text, "vuelvo por lo mismo") {
		t.Fatalf("text = %q, se esperaba el mensaje recortado y completo", text)
	}
	if len(store.touched) != 1 || store.touched[0] != "ticket-a" {
		t.Fatalf("tocados = %v, se esperaba la marca de actividad del ticket", store.touched)
	}
}

// TestAddTicketNoteRejections pins the note rules: a foreign ticket is
// indistinguishable from a missing one (no oracle), a closed ticket cannot be
// written into, and the text is required and bounded.
func TestAddTicketNoteRejections(t *testing.T) {
	store := newFakeTickets()
	store.seed(dto.TicketRecord{ID: "ticket-a", ClientID: clientA})
	store.seed(dto.TicketRecord{ID: "ticket-closed", ClientID: clientA, State: dto.TicketStateClosed})

	uc, err := usecase.NewAddTicketNote(store)
	if err != nil {
		t.Fatalf("NewAddTicketNote() error inesperado: %v", err)
	}
	client := ticketCaller(clientA, auth.RoleClient)

	t.Run("ticket de otro cliente", func(t *testing.T) {
		_, err := uc.Execute(context.Background(), ticketCaller(clientB, auth.RoleClient), dto.TicketNote{
			TicketID: "ticket-a",
			Text:     "hola",
		})
		assertSemantic(t, err, usecase.CodeTicketNotFound, "no existe un ticket con ese identificador.")
	})

	t.Run("ticket inexistente", func(t *testing.T) {
		// Same rejection as somebody else's ticket: the answer never confirms that a
		// ticket exists.
		_, err := uc.Execute(context.Background(), client, dto.TicketNote{TicketID: "no-existe", Text: "hola"})
		assertSemantic(t, err, usecase.CodeTicketNotFound, "no existe un ticket con ese identificador.")
	})

	t.Run("ticket cerrado", func(t *testing.T) {
		_, err := uc.Execute(context.Background(), client, dto.TicketNote{TicketID: "ticket-closed", Text: "hola"})
		assertSemantic(t, err, usecase.CodeTicketClosed,
			"el ticket está cerrado: una conversación cerrada no se reabre con una nota. Si el cliente vuelve, se abre un ticket nuevo.")
	})

	t.Run("texto vacío", func(t *testing.T) {
		_, err := uc.Execute(context.Background(), client, dto.TicketNote{TicketID: "ticket-a", Text: "   "})
		assertSemantic(t, err, usecase.CodeInvalidTicket, "el texto de la nota es obligatorio.")
	})

	t.Run("texto demasiado largo", func(t *testing.T) {
		_, err := uc.Execute(context.Background(), client, dto.TicketNote{TicketID: "ticket-a", Text: strings.Repeat("x", 2001)})
		assertSemantic(t, err, usecase.CodeInvalidTicket, "la nota no puede superar los 2000 caracteres.")
	})

	t.Run("sin ticket", func(t *testing.T) {
		_, err := uc.Execute(context.Background(), client, dto.TicketNote{Text: "hola"})
		assertSemantic(t, err, usecase.CodeInvalidTicket, "el identificador del ticket es obligatorio.")
	})

	if len(store.appended) != 0 {
		t.Fatalf("eventos escritos = %d, ningún rechazo debe escribir", len(store.appended))
	}
}

// TestAddTicketNoteRejectsAConcurrentClose pins the guard of the note (review
// R1-NOTE-RACE): the closed check the use case reads is not the one that decides
// the write. When another actor closes the ticket between that read and the
// atomic repository operation, the guard refuses and the caller gets the closed
// rejection — the note never lands on a closed conversation.
func TestAddTicketNoteRejectsAConcurrentClose(t *testing.T) {
	store := newFakeTickets()
	store.seed(dto.TicketRecord{ID: "ticket-a", ClientID: clientA})
	store.closeOnNote = true

	uc, err := usecase.NewAddTicketNote(store)
	if err != nil {
		t.Fatalf("NewAddTicketNote() error inesperado: %v", err)
	}

	_, err = uc.Execute(context.Background(), ticketCaller(clientA, auth.RoleClient), dto.TicketNote{
		TicketID: "ticket-a",
		Text:     "hola",
	})
	assertSemantic(t, err, usecase.CodeTicketClosed,
		"el ticket está cerrado: una conversación cerrada no se reabre con una nota. Si el cliente vuelve, se abre un ticket nuevo.")

	// Nothing was written by the losing call: no event and no activity of its own.
	if len(store.appended) != 0 {
		t.Fatalf("eventos escritos = %d, la nota rechazada no debe escribir", len(store.appended))
	}
	if len(store.touched) != 0 {
		t.Fatalf("actividades = %v, la nota rechazada no debe mover el reloj de inactividad", store.touched)
	}
}

// TestAddTicketNoteInternalOnAnyTicket pins the internal half: the team annotates
// any ticket, including one assigned to somebody else and one that is closed.
func TestAddTicketNoteInternalOnAnyTicket(t *testing.T) {
	store := newFakeTickets()
	store.seed(dto.TicketRecord{ID: "ticket-a", ClientID: clientA})
	store.seed(dto.TicketRecord{ID: "ticket-closed", ClientID: clientB, State: dto.TicketStateClosed})

	uc, err := usecase.NewAddTicketNote(store)
	if err != nil {
		t.Fatalf("NewAddTicketNote() error inesperado: %v", err)
	}
	staff := ticketCaller(staffID, auth.RoleStaff)

	if _, err := uc.Execute(context.Background(), staff, dto.TicketNote{TicketID: "ticket-a", Text: "lo estoy viendo"}); err != nil {
		t.Fatalf("Execute() error inesperado: %v", err)
	}
	if len(store.appended) != 1 || store.appended[0].Actor != staffID {
		t.Fatalf("eventos = %+v, se esperaba la nota con el actor interno", store.appended)
	}

	// The closed rule is about the conversation, not about the role: RF5 opens a
	// new ticket when the client comes back, and the audit history stays closed.
	_, err = uc.Execute(context.Background(), staff, dto.TicketNote{TicketID: "ticket-closed", Text: "reabro"})
	assertSemantic(t, err, usecase.CodeTicketClosed,
		"el ticket está cerrado: una conversación cerrada no se reabre con una nota. Si el cliente vuelve, se abre un ticket nuevo.")
}

// TestTransitionTicketIsInternalOnly pins the double RBAC of the transition: a
// client role is refused by the use case even though the transport already hid the
// tool.
func TestTransitionTicketIsInternalOnly(t *testing.T) {
	store := newFakeTickets()
	store.seed(dto.TicketRecord{ID: "ticket-a", ClientID: clientA})

	uc, err := usecase.NewTransitionTicket(store)
	if err != nil {
		t.Fatalf("NewTransitionTicket() error inesperado: %v", err)
	}

	for _, role := range []auth.Role{auth.RoleClient, ""} {
		_, err := uc.Execute(context.Background(), ticketCaller(clientA, role), dto.TransitionCommand{
			TicketID: "ticket-a",
			State:    dto.TicketStateAttended,
		})
		assertSemantic(t, err, usecase.CodeRoleNotAllowed, "tu rol no permite cambiar el estado de un ticket.")
	}
	if len(store.transitions) != 0 {
		t.Fatalf("transiciones = %+v, no debe llegar ninguna al repositorio", store.transitions)
	}
}

// TestTransitionTicketValidatesTheState pins the F1 vocabulary: an unknown state and
// the state the ticket is already in are both rejected with Spanish prose, before
// any write.
func TestTransitionTicketValidatesTheState(t *testing.T) {
	store := newFakeTickets()
	store.seed(dto.TicketRecord{ID: "ticket-a", ClientID: clientA, State: dto.TicketStateAttended})

	uc, err := usecase.NewTransitionTicket(store)
	if err != nil {
		t.Fatalf("NewTransitionTicket() error inesperado: %v", err)
	}
	staff := ticketCaller(staffID, auth.RoleStaff)

	_, err = uc.Execute(context.Background(), staff, dto.TransitionCommand{TicketID: "ticket-a", State: "en-proceso"})
	assertSemantic(t, err, usecase.CodeInvalidTicketState,
		"el estado destino no es válido: en F1 los estados posibles son nuevo, atendido y cerrado.")

	_, err = uc.Execute(context.Background(), staff, dto.TransitionCommand{TicketID: "ticket-a", State: dto.TicketStateAttended})
	assertSemantic(t, err, usecase.CodeInvalidTicketState, `el ticket ya está en el estado "atendido".`)

	_, err = uc.Execute(context.Background(), staff, dto.TransitionCommand{State: dto.TicketStateClosed})
	assertSemantic(t, err, usecase.CodeInvalidTicket, "el identificador del ticket es obligatorio.")

	_, err = uc.Execute(context.Background(), staff, dto.TransitionCommand{TicketID: "no-existe", State: dto.TicketStateClosed})
	assertSemantic(t, err, usecase.CodeTicketNotFound, "no existe un ticket con ese identificador.")

	if len(store.transitions) != 0 {
		t.Fatalf("transiciones = %+v, ningún rechazo debe escribir", store.transitions)
	}
}

// TestTransitionTicketAuditsTheChange pins the RF5 acceptance criterion for a
// transition: the state changes, and the history shows who, when and from/to which
// state.
func TestTransitionTicketAuditsTheChange(t *testing.T) {
	store := newFakeTickets()
	store.seed(dto.TicketRecord{ID: "ticket-a", ClientID: clientA})

	uc, err := usecase.NewTransitionTicket(store)
	if err != nil {
		t.Fatalf("NewTransitionTicket() error inesperado: %v", err)
	}

	result, err := uc.Execute(context.Background(), ticketCaller(staffID, auth.RoleStaff), dto.TransitionCommand{
		TicketID: "ticket-a",
		State:    dto.TicketStateAttended,
	})
	if err != nil {
		t.Fatalf("Execute() error inesperado: %v", err)
	}

	if result.Ticket.State != dto.TicketStateAttended {
		t.Fatalf("state = %q, se esperaba %q", result.Ticket.State, dto.TicketStateAttended)
	}
	if len(result.Events) != 2 {
		t.Fatalf("eventos = %d, se esperaba la creación y la transición", len(result.Events))
	}
	transition := result.Events[1]
	if transition.Kind != dto.TicketEventKindTransition || transition.Actor != staffID {
		t.Fatalf("evento = %+v, se esperaba la transición con el actor interno", transition)
	}
	if transition.CreatedAt == "" {
		t.Fatal("la transición no quedó fechada")
	}
	payload := payloadOf(t, transition.Payload)
	if payload["from"] != string(dto.TicketStateNew) || payload["to"] != string(dto.TicketStateAttended) {
		t.Fatalf("payload = %v, se esperaba el estado de origen y destino", payload)
	}
}

// TestTransitionTicketRejectsAConcurrentChange pins the guarded write: when another
// actor moved the ticket between the read and the write, the caller gets a semantic
// rejection instead of a silent overwrite.
func TestTransitionTicketRejectsAConcurrentChange(t *testing.T) {
	store := newFakeTickets()
	store.seed(dto.TicketRecord{ID: "ticket-a", ClientID: clientA})
	store.refuseTransition = true

	uc, err := usecase.NewTransitionTicket(store)
	if err != nil {
		t.Fatalf("NewTransitionTicket() error inesperado: %v", err)
	}

	_, err = uc.Execute(context.Background(), ticketCaller(staffID, auth.RoleStaff), dto.TransitionCommand{
		TicketID: "ticket-a",
		State:    dto.TicketStateClosed,
	})
	assertSemantic(t, err, usecase.CodeTicketStateChanged,
		"el ticket cambió mientras lo operabas: volvé a leerlo e intentá de nuevo.")
}

// TestTicketUseCasesWrapInternalFailures pins AGENTS.md boundary contract 2: a
// persistence failure is NOT semantic, so the transport collapses it to the generic
// internal error instead of rendering a driver message.
func TestTicketUseCasesWrapInternalFailures(t *testing.T) {
	cause := errors.New("sqlite: no such table: tickets")

	store := newFakeTickets()
	store.createErr = cause
	create := createTicketUC(t, store, newFakeClientDirectory(clientA))
	_, err := create.Execute(context.Background(), ticketCaller(clientA, auth.RoleClient), dto.CreateTicketCommand{Motive: "consulta"})
	assertInternal(t, err, cause)

	store = newFakeTickets()
	store.seed(dto.TicketRecord{ID: "ticket-a", ClientID: clientA})
	store.listErr = cause
	list, err := usecase.NewListMyTickets(store)
	if err != nil {
		t.Fatalf("NewListMyTickets() error inesperado: %v", err)
	}
	_, listErr := list.Execute(context.Background(), ticketCaller(clientA, auth.RoleClient), dto.TicketQuery{})
	assertInternal(t, listErr, cause)

	store = newFakeTickets()
	store.seed(dto.TicketRecord{ID: "ticket-a", ClientID: clientA})
	store.noteErr = cause
	note, err := usecase.NewAddTicketNote(store)
	if err != nil {
		t.Fatalf("NewAddTicketNote() error inesperado: %v", err)
	}
	_, err = note.Execute(context.Background(), ticketCaller(clientA, auth.RoleClient), dto.TicketNote{TicketID: "ticket-a", Text: "hola"})
	assertInternal(t, err, cause)

	store = newFakeTickets()
	store.seed(dto.TicketRecord{ID: "ticket-a", ClientID: clientA})
	store.transitionErr = cause
	transition, err := usecase.NewTransitionTicket(store)
	if err != nil {
		t.Fatalf("NewTransitionTicket() error inesperado: %v", err)
	}
	_, err = transition.Execute(context.Background(), ticketCaller(staffID, auth.RoleStaff), dto.TransitionCommand{
		TicketID: "ticket-a",
		State:    dto.TicketStateAttended,
	})
	assertInternal(t, err, cause)
}
