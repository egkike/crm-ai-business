package repository_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/egkike/crm-ai-business/internal/application/dto"
	"github.com/egkike/crm-ai-business/internal/db"
	"github.com/egkike/crm-ai-business/internal/repository"
)

// newTicketsOf wires the adapter over handle.
func newTicketsOf(t *testing.T, handle *sql.DB) *repository.TicketsRepo {
	t.Helper()
	repo, err := repository.NewTicketsRepo(handle)
	if err != nil {
		t.Fatalf("NewTicketsRepo() error inesperado: %v", err)
	}
	return repo
}

// ticketFixture is one clients row plus the two ids used to build tickets.
type ticketFixture struct {
	handle *sql.DB
	repo   *repository.TicketsRepo
	client string
	other  string
}

// newTicketFixture opens a migrated database and seeds the two clients the
// ticket tests reference (the tickets.client_id FK requires real rows).
func newTicketFixture(t *testing.T) *ticketFixture {
	t.Helper()

	handle := openDB(t)
	seedClient(t, handle, client{id: uuid(1), phone: "+5491100000001", name: "Cliente ****0001"})
	seedClient(t, handle, client{id: uuid(2), phone: "+5491100000002", name: "Cliente ****0002"})

	return &ticketFixture{
		handle: handle,
		repo:   newTicketsOf(t, handle),
		client: uuid(1),
		other:  uuid(2),
	}
}

// The state names the seeded 'consulta' template declares (PRD §3.7: "'consulta':
// nuevo → atendido → resuelto"). They are values of that configuration, not a
// vocabulary: the adapter validates nothing against them, and the closed rule is
// derived from the configured transitions.
const (
	stateNew      dto.TicketState = "nuevo"
	stateAttended dto.TicketState = "atendido"
	stateResolved dto.TicketState = "resuelto"
)

// openTicket is the fixture shape of a created ticket.
type openTicket struct {
	id       string
	clientID string
	state    dto.TicketState
}

// createTicket inserts one ticket with its opening event through the adapter.
func (f *ticketFixture) createTicket(t *testing.T, ticket openTicket) {
	t.Helper()

	if ticket.state == "" {
		ticket.state = stateNew
	}
	motive, err := json.Marshal(map[string]string{"motive": "consulta por corte de pelo"})
	if err != nil {
		t.Fatalf("serializar el motivo: %v", err)
	}

	err = f.repo.CreateTicket(context.Background(), dto.NewTicket{
		ID:         ticket.id,
		TicketType: dto.ConsultaTicketTypeName,
		ClientID:   ticket.clientID,
		State:      ticket.state,
		OpeningEvent: dto.NewTicketEvent{
			TicketID: ticket.id,
			Actor:    ticket.clientID,
			Kind:     dto.TicketEventKindCreated,
			Payload:  string(motive),
		},
	})
	if err != nil {
		t.Fatalf("CreateTicket(%s) error inesperado: %v", ticket.id, err)
	}
}

// TestNewTicketsRepoRejectsNilHandle pins the fail-closed construction rule.
func TestNewTicketsRepoRejectsNilHandle(t *testing.T) {
	repo, err := repository.NewTicketsRepo(nil)
	if err == nil {
		t.Fatal("NewTicketsRepo(nil) debía fallar (fail-closed)")
	}
	if repo != nil {
		t.Fatalf("NewTicketsRepo(nil) devolvió un adaptador utilizable: %+v", repo)
	}
}

// TestTicketConstantsMirrorTheSchema pins the mirror the application layer owns:
// the seeded 'consulta' ticket type. The application layer must not import the
// persistence adapter, so the values are duplicated — and this test is what keeps
// the duplication from drifting away from the seeded DDL.
//
// The STATE vocabulary is no longer mirrored (F2 T3): a ticket's states are the
// ones its type configures, so there is no fixed list to compare against. What
// this test still pins is that the seeded types exist and are active.
func TestTicketConstantsMirrorTheSchema(t *testing.T) {
	if dto.ConsultaTicketTypeID != db.TicketTypeConsultaID {
		t.Fatalf("dto.ConsultaTicketTypeID = %q, se esperaba %q", dto.ConsultaTicketTypeID, db.TicketTypeConsultaID)
	}
	if dto.ConsultaTicketTypeName != db.TicketTypeConsultaName {
		t.Fatalf("dto.ConsultaTicketTypeName = %q, se esperaba %q", dto.ConsultaTicketTypeName, db.TicketTypeConsultaName)
	}
	if string(stateNew) != db.TicketStateInitial {
		t.Fatalf("el estado inicial de la plantilla = %q, se esperaba %q", stateNew, db.TicketStateInitial)
	}

	handle := openDB(t)
	name := queryScalar(t, handle, `SELECT name FROM ticket_types WHERE id = ?`, dto.ConsultaTicketTypeID)
	if name != db.TicketTypeConsultaName {
		t.Fatalf("ticket_types.name = %q, se esperaba %q", name, db.TicketTypeConsultaName)
	}
	if active := queryScalar(t, handle, `SELECT is_active FROM ticket_types WHERE id = ?`, dto.ConsultaTicketTypeID); active != "1" {
		t.Fatalf("is_active = %q, se esperaba el tipo sembrado activo", active)
	}

	// The states the fixture speaks are the ones the seeded config declares, and
	// the ending of the conversation is the template's terminal state.
	for _, state := range []dto.TicketState{stateNew, stateAttended, stateResolved} {
		if got := queryScalar(t, handle, `SELECT COUNT(*) FROM ticket_states WHERE name = ? AND ticket_type_id = ?`, state, db.TicketTypeConsultaID); got != "1" {
			t.Fatalf("%q no es un estado declarado por la plantilla 'consulta' (count=%s)", state, got)
		}
	}
}

// TestCreateTicketWritesTicketAndOpeningEvent pins RF5's creation criterion: the
// ticket exists in the configured initial state of its type and its creation event
// is audited with actor and timestamp.
func TestCreateTicketWritesTicketAndOpeningEvent(t *testing.T) {
	fixture := newTicketFixture(t)
	id := uuid(700)

	fixture.createTicket(t, openTicket{id: id, clientID: fixture.client})

	record, found, err := fixture.repo.GetTicket(context.Background(), id)
	if err != nil {
		t.Fatalf("GetTicket() error inesperado: %v", err)
	}
	if !found {
		t.Fatalf("GetTicket(%s) no encontró el ticket recién creado", id)
	}
	if record.State != stateNew {
		t.Fatalf("state = %q, se esperaba %q", record.State, stateNew)
	}
	if record.TicketTypeID != db.TicketTypeConsultaID {
		t.Fatalf("ticket_type_id = %q, se esperaba el tipo sembrado %q", record.TicketTypeID, db.TicketTypeConsultaID)
	}
	if record.ClientID != fixture.client {
		t.Fatalf("client_id = %q, se esperaba %q", record.ClientID, fixture.client)
	}
	if record.AssignedTo != "" {
		t.Fatalf("assigned_to = %q, se esperaba vacío (pool sin asignar)", record.AssignedTo)
	}
	if record.CreatedAt == "" || record.UpdatedAt == "" {
		t.Fatalf("created_at/updated_at = %q/%q, se esperaban marcas de tiempo", record.CreatedAt, record.UpdatedAt)
	}

	events, err := fixture.repo.ListTicketEvents(context.Background(), id)
	if err != nil {
		t.Fatalf("ListTicketEvents() error inesperado: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("eventos = %d, se esperaba el evento de creación", len(events))
	}
	opening := events[0]
	if opening.Kind != dto.TicketEventKindCreated {
		t.Fatalf("kind = %q, se esperaba %q", opening.Kind, dto.TicketEventKindCreated)
	}
	if opening.Actor != fixture.client {
		t.Fatalf("actor = %q, se esperaba el actor %q", opening.Actor, fixture.client)
	}
	if opening.TicketID != id {
		t.Fatalf("ticket_id = %q, se esperaba %q", opening.TicketID, id)
	}
	if opening.ID == 0 || opening.CreatedAt == "" {
		t.Fatalf("evento sin id ni timestamp: %+v", opening)
	}
	if !json.Valid([]byte(opening.Payload)) {
		t.Fatalf("payload = %q, se esperaba un documento JSON", opening.Payload)
	}
}

// TestCreateTicketIsAtomic pins that a refused creation writes NOTHING: an
// unknown ticket type fails the foreign key, and neither the ticket nor its event
// may survive the rollback.
func TestCreateTicketIsAtomic(t *testing.T) {
	fixture := newTicketFixture(t)
	id := uuid(701)

	err := fixture.repo.CreateTicket(context.Background(), dto.NewTicket{
		ID:           id,
		TicketType:   "no-existe", // no existe en ticket_types
		ClientID:     fixture.client,
		State:        stateNew,
		OpeningEvent: dto.NewTicketEvent{Actor: fixture.client, Kind: dto.TicketEventKindCreated},
	})
	if err == nil {
		t.Fatal("CreateTicket() con un tipo inexistente debía fallar")
	}

	if _, found, err := fixture.repo.GetTicket(context.Background(), id); err != nil || found {
		t.Fatalf("GetTicket() = %t, %v; se esperaba que el ticket NO existiera", found, err)
	}
	events, err := fixture.repo.ListTicketEvents(context.Background(), id)
	if err != nil {
		t.Fatalf("ListTicketEvents() error inesperado: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("eventos = %d, se esperaba ninguno tras el rollback", len(events))
	}
}

// TestAppendTicketEventKeepsInsertionOrder pins the append-only history: events
// come back in insertion order (the AUTOINCREMENT id), with the id and timestamp
// the database assigned.
func TestAppendTicketEventKeepsInsertionOrder(t *testing.T) {
	fixture := newTicketFixture(t)
	id := uuid(702)
	fixture.createTicket(t, openTicket{id: id, clientID: fixture.client})

	ctx := context.Background()
	texts := []string{"primera consulta", "vuelve sobre lo mismo", "tercera vez"}
	ids := make([]int64, 0, len(texts))
	for _, text := range texts {
		payload, err := json.Marshal(map[string]string{"text": text})
		if err != nil {
			t.Fatalf("serializar el payload: %v", err)
		}
		record, err := fixture.repo.AppendTicketEvent(ctx, dto.NewTicketEvent{
			TicketID: id,
			Actor:    fixture.client,
			Kind:     dto.TicketEventKindNote,
			Payload:  string(payload),
		})
		if err != nil {
			t.Fatalf("AppendTicketEvent(%q) error inesperado: %v", text, err)
		}
		if record.CreatedAt == "" {
			t.Fatalf("el evento %q no trae created_at", text)
		}
		ids = append(ids, record.ID)
	}

	for index := 1; index < len(ids); index++ {
		if ids[index] <= ids[index-1] {
			t.Fatalf("ids = %v, se esperaban crecientes (append-only)", ids)
		}
	}

	events, err := fixture.repo.ListTicketEvents(ctx, id)
	if err != nil {
		t.Fatalf("ListTicketEvents() error inesperado: %v", err)
	}
	if len(events) != len(texts)+1 {
		t.Fatalf("eventos = %d, se esperaban %d (creación + notas)", len(events), len(texts)+1)
	}
	if events[0].Kind != dto.TicketEventKindCreated {
		t.Fatalf("primer evento = %q, se esperaba la creación", events[0].Kind)
	}
	for index, text := range texts {
		var stored struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(events[index+1].Payload), &stored); err != nil {
			t.Fatalf("payload del evento %d ilegible: %v", index+1, err)
		}
		if stored.Text != text {
			t.Fatalf("payload del evento %d = %q, se esperaba %q", index+1, stored.Text, text)
		}
	}
}

// TestAppendTicketEventRejectsBrokenInput pins that the adapter refuses what the
// schema would reject, BEFORE a statement runs: an empty event has no actor and a
// malformed payload fails json_valid.
func TestAppendTicketEventRejectsBrokenInput(t *testing.T) {
	fixture := newTicketFixture(t)
	id := uuid(703)
	fixture.createTicket(t, openTicket{id: id, clientID: fixture.client})

	ctx := context.Background()
	cases := []struct {
		name  string
		event dto.NewTicketEvent
	}{
		{name: "sin ticket", event: dto.NewTicketEvent{Actor: fixture.client, Kind: dto.TicketEventKindNote}},
		{name: "sin actor", event: dto.NewTicketEvent{TicketID: id, Kind: dto.TicketEventKindNote}},
		{name: "sin tipo", event: dto.NewTicketEvent{TicketID: id, Actor: fixture.client}},
		{name: "payload inválido", event: dto.NewTicketEvent{TicketID: id, Actor: fixture.client, Kind: dto.TicketEventKindNote, Payload: "{no es json"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := fixture.repo.AppendTicketEvent(ctx, tc.event); err == nil {
				t.Fatalf("AppendTicketEvent(%+v) debía fallar", tc.event)
			}
		})
	}

	// An empty payload is the empty object, not an empty string: the column is
	// CHECKed as valid JSON.
	record, err := fixture.repo.AppendTicketEvent(ctx, dto.NewTicketEvent{
		TicketID: id,
		Actor:    fixture.client,
		Kind:     dto.TicketEventKindNote,
	})
	if err != nil {
		t.Fatalf("AppendTicketEvent() sin payload error inesperado: %v", err)
	}
	if record.Payload != "{}" {
		t.Fatalf("payload = %q, se esperaba {}", record.Payload)
	}
}

// TestListTicketsFilters pins the two list views: the client's tickets, the whole
// business, the closed-ticket filter and the page window.
func TestListTicketsFilters(t *testing.T) {
	fixture := newTicketFixture(t)
	ctx := context.Background()

	openOne := uuid(710)
	closedOne := uuid(711)
	openOther := uuid(712)
	fixture.createTicket(t, openTicket{id: openOne, clientID: fixture.client})
	fixture.createTicket(t, openTicket{id: closedOne, clientID: fixture.client, state: stateResolved})
	fixture.createTicket(t, openTicket{id: openOther, clientID: fixture.other})

	t.Run("cliente sin cerrados", func(t *testing.T) {
		tickets, err := fixture.repo.ListTicketsByClient(ctx, fixture.client, false, 10, 0)
		if err != nil {
			t.Fatalf("ListTicketsByClient() error inesperado: %v", err)
		}
		if len(tickets) != 1 || tickets[0].ID != openOne {
			t.Fatalf("tickets = %v, se esperaba solo el abierto del cliente", ticketIDs(tickets))
		}
	})

	t.Run("cliente con cerrados", func(t *testing.T) {
		tickets, err := fixture.repo.ListTicketsByClient(ctx, fixture.client, true, 10, 0)
		if err != nil {
			t.Fatalf("ListTicketsByClient() error inesperado: %v", err)
		}
		if len(tickets) != 2 {
			t.Fatalf("tickets = %v, se esperaban los dos del cliente", ticketIDs(tickets))
		}
	})

	t.Run("todos los clientes", func(t *testing.T) {
		tickets, err := fixture.repo.ListTickets(ctx, dto.TicketQuery{Limit: 10})
		if err != nil {
			t.Fatalf("ListTickets() error inesperado: %v", err)
		}
		if len(tickets) != 2 {
			t.Fatalf("tickets = %v, se esperaban los dos abiertos", ticketIDs(tickets))
		}
		if hasTicketID(tickets, closedOne) {
			t.Fatalf("tickets = %v, no debe incluir el cerrado por defecto", ticketIDs(tickets))
		}
	})

	t.Run("todos con cerrados", func(t *testing.T) {
		tickets, err := fixture.repo.ListTickets(ctx, dto.TicketQuery{IncludeClosed: true, Limit: 10})
		if err != nil {
			t.Fatalf("ListTickets() error inesperado: %v", err)
		}
		if len(tickets) != 3 {
			t.Fatalf("tickets = %v, se esperaban los tres", ticketIDs(tickets))
		}
	})

	t.Run("paginación", func(t *testing.T) {
		first, err := fixture.repo.ListTickets(ctx, dto.TicketQuery{IncludeClosed: true, Limit: 1, Offset: 0})
		if err != nil {
			t.Fatalf("ListTickets() error inesperado: %v", err)
		}
		second, err := fixture.repo.ListTickets(ctx, dto.TicketQuery{IncludeClosed: true, Limit: 1, Offset: 1})
		if err != nil {
			t.Fatalf("ListTickets() error inesperado: %v", err)
		}
		if len(first) != 1 || len(second) != 1 {
			t.Fatalf("páginas = %v / %v, se esperaba un ticket por página", ticketIDs(first), ticketIDs(second))
		}
		if first[0].ID == second[0].ID {
			t.Fatalf("las dos páginas devolvieron el mismo ticket %q", first[0].ID)
		}
	})

	t.Run("límite no positivo", func(t *testing.T) {
		if _, err := fixture.repo.ListTickets(ctx, dto.TicketQuery{}); err == nil {
			t.Fatal("ListTickets() sin límite debía fallar")
		}
	})
}

// TestTransitionTicketIsGuarded pins the single transition write: the row changes
// state and gains its audited event, and a transition whose `from` no longer
// matches writes NOTHING (no state change, no event).
func TestTransitionTicketIsGuarded(t *testing.T) {
	fixture := newTicketFixture(t)
	ctx := context.Background()
	id := uuid(720)
	fixture.createTicket(t, openTicket{id: id, clientID: fixture.client})

	payload := `{"from":"nuevo","to":"atendido"}`
	applied, err := fixture.repo.TransitionTicket(ctx, dto.TicketTransition{
		TicketID: id,
		From:     stateNew,
		To:       stateAttended,
		Event:    dto.NewTicketEvent{Actor: uuid(50), Kind: dto.TicketEventKindTransition, Payload: payload},
	})
	if err != nil {
		t.Fatalf("TransitionTicket() error inesperado: %v", err)
	}
	if !applied {
		t.Fatal("TransitionTicket() no aplicó una transición desde el estado real")
	}

	record, found, err := fixture.repo.GetTicket(ctx, id)
	if err != nil || !found {
		t.Fatalf("GetTicket() = %t, %v", found, err)
	}
	if record.State != stateAttended {
		t.Fatalf("state = %q, se esperaba %q", record.State, stateAttended)
	}

	events, err := fixture.repo.ListTicketEvents(ctx, id)
	if err != nil {
		t.Fatalf("ListTicketEvents() error inesperado: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("eventos = %d, se esperaba la creación y la transición", len(events))
	}
	last := events[len(events)-1]
	if last.Kind != dto.TicketEventKindTransition || last.Actor != uuid(50) {
		t.Fatalf("último evento = %+v, se esperaba la transición con su actor", last)
	}
	if last.Payload != payload {
		t.Fatalf("payload = %q, se esperaba %q", last.Payload, payload)
	}

	// The same `from` is now stale: the guarded update applies nothing.
	applied, err = fixture.repo.TransitionTicket(ctx, dto.TicketTransition{
		TicketID: id,
		From:     stateNew,
		To:       stateResolved,
		Event:    dto.NewTicketEvent{Actor: uuid(50), Kind: dto.TicketEventKindTransition},
	})
	if err != nil {
		t.Fatalf("TransitionTicket() error inesperado: %v", err)
	}
	if applied {
		t.Fatal("TransitionTicket() aplicó una transición desde un estado obsoleto")
	}

	afterStale, _, err := fixture.repo.GetTicket(ctx, id)
	if err != nil {
		t.Fatalf("GetTicket() error inesperado: %v", err)
	}
	if afterStale.State != stateAttended {
		t.Fatalf("state = %q, se esperaba que siguiera en %q", afterStale.State, stateAttended)
	}
	staleEvents, err := fixture.repo.ListTicketEvents(ctx, id)
	if err != nil {
		t.Fatalf("ListTicketEvents() error inesperado: %v", err)
	}
	if len(staleEvents) != 2 {
		t.Fatalf("eventos = %d, la transición rechazada no debe dejar evento", len(staleEvents))
	}

	// A ticket that does not exist behaves like a stale guard: found=false, no
	// event, no state.
	applied, err = fixture.repo.TransitionTicket(ctx, dto.TicketTransition{
		TicketID: uuid(721),
		From:     stateNew,
		To:       stateAttended,
		Event:    dto.NewTicketEvent{Actor: uuid(50), Kind: dto.TicketEventKindTransition},
	})
	if err != nil {
		t.Fatalf("TransitionTicket() error inesperado: %v", err)
	}
	if applied {
		t.Fatal("TransitionTicket() aplicó una transición sobre un ticket inexistente")
	}
}

// TestAddTicketNoteWritesEventAndActivity pins the single write group of a note
// (review R3-note-nonatomic): the audited event AND the activity of the ticket
// land together, and the ticket the adapter reads back is the one after its own
// note.
func TestAddTicketNoteWritesEventAndActivity(t *testing.T) {
	fixture := newTicketFixture(t)
	ctx := context.Background()
	id := uuid(725)
	fixture.createTicket(t, openTicket{id: id, clientID: fixture.client})

	before, found, err := fixture.repo.GetTicket(ctx, id)
	if err != nil || !found {
		t.Fatalf("GetTicket() = %t, %v", found, err)
	}

	// The timestamps have millisecond precision, so the two writes must be at
	// least a millisecond apart to be distinguishable.
	time.Sleep(3 * time.Millisecond)

	payload := `{"text":"vuelvo por lo mismo"}`
	record, applied, err := fixture.repo.AddTicketNote(ctx, dto.TicketNoteAppend{
		TicketID: id,
		Event:    dto.NewTicketEvent{Actor: uuid(51), Kind: dto.TicketEventKindNote, Payload: payload},
	})
	if err != nil {
		t.Fatalf("AddTicketNote() error inesperado: %v", err)
	}
	if !applied {
		t.Fatal("AddTicketNote() no anotó un ticket abierto")
	}
	if record.ID != id {
		t.Fatalf("record.ID = %q, se esperaba %q", record.ID, id)
	}
	if record.UpdatedAt == before.UpdatedAt {
		t.Fatalf("updated_at = %q, se esperaba que la nota moviera la actividad", record.UpdatedAt)
	}
	if record.State != before.State {
		t.Fatalf("state = %q, la nota no debe cambiar el estado", record.State)
	}

	events, err := fixture.repo.ListTicketEvents(ctx, id)
	if err != nil {
		t.Fatalf("ListTicketEvents() error inesperado: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("eventos = %d, se esperaba la creación y la nota", len(events))
	}
	note := events[len(events)-1]
	if note.Kind != dto.TicketEventKindNote || note.Actor != uuid(51) {
		t.Fatalf("último evento = %+v, se esperaba la nota con su actor", note)
	}
	if note.Payload != payload {
		t.Fatalf("payload = %q, se esperaba %q", note.Payload, payload)
	}
}

// TestAddTicketNoteRefusesAClosedTicket pins the state guard of the note (review
// R1-NOTE-RACE): against a closed ticket the guarded touch matches no row, so the
// note writes NOTHING — no event and no activity — instead of reopening a closed
// conversation. A ticket that does not exist behaves the same (found=false).
func TestAddTicketNoteRefusesAClosedTicket(t *testing.T) {
	fixture := newTicketFixture(t)
	ctx := context.Background()
	id := uuid(726)
	fixture.createTicket(t, openTicket{id: id, clientID: fixture.client, state: stateResolved})

	before, found, err := fixture.repo.GetTicket(ctx, id)
	if err != nil || !found {
		t.Fatalf("GetTicket() = %t, %v", found, err)
	}
	time.Sleep(3 * time.Millisecond)

	note := dto.TicketNoteAppend{
		TicketID: id,
		Event:    dto.NewTicketEvent{Actor: fixture.client, Kind: dto.TicketEventKindNote},
	}
	if _, applied, err := fixture.repo.AddTicketNote(ctx, note); err != nil || applied {
		t.Fatalf("AddTicketNote(ticket cerrado) = %t, %v; se esperaba false sin error", applied, err)
	}

	after, _, err := fixture.repo.GetTicket(ctx, id)
	if err != nil {
		t.Fatalf("GetTicket() error inesperado: %v", err)
	}
	if after.UpdatedAt != before.UpdatedAt {
		t.Fatalf("updated_at = %q, el ticket cerrado no debe moverse", after.UpdatedAt)
	}
	if events, err := fixture.repo.ListTicketEvents(ctx, id); err != nil || len(events) != 1 {
		t.Fatalf("eventos = %d, %v; se esperaba solo la creación", len(events), err)
	}

	note.TicketID = uuid(727)
	if _, applied, err := fixture.repo.AddTicketNote(ctx, note); err != nil || applied {
		t.Fatalf("AddTicketNote(ticket inexistente) = %t, %v; se esperaba false sin error", applied, err)
	}
}

// TestAddTicketNoteRollsBackAFailedAppend pins the atomicity of the failed path:
// when the event cannot be written, the activity the transaction already touched
// is rolled back with it, so the F2 inactivity clock never reads a note that does
// not exist.
func TestAddTicketNoteRollsBackAFailedAppend(t *testing.T) {
	fixture := newTicketFixture(t)
	ctx := context.Background()
	id := uuid(728)
	fixture.createTicket(t, openTicket{id: id, clientID: fixture.client})

	before, found, err := fixture.repo.GetTicket(ctx, id)
	if err != nil || !found {
		t.Fatalf("GetTicket() = %t, %v", found, err)
	}
	time.Sleep(3 * time.Millisecond)

	if _, err := fixture.handle.Exec(`DROP TABLE ticket_events`); err != nil {
		t.Fatalf("romper la tabla de eventos: %v", err)
	}
	if _, _, err := fixture.repo.AddTicketNote(ctx, dto.TicketNoteAppend{
		TicketID: id,
		Event:    dto.NewTicketEvent{Actor: fixture.client, Kind: dto.TicketEventKindNote},
	}); err == nil {
		t.Fatal("AddTicketNote() debía fallar con la tabla de eventos ausente")
	}

	after, found, err := fixture.repo.GetTicket(ctx, id)
	if err != nil || !found {
		t.Fatalf("GetTicket() = %t, %v", found, err)
	}
	if after.UpdatedAt != before.UpdatedAt {
		t.Fatalf("updated_at = %q, la nota fallida no debe mover la actividad", after.UpdatedAt)
	}
}

// TestAddTicketNoteRejectsBrokenInput pins that the adapter refuses what cannot
// be written BEFORE a transaction exists. The guard needs no state argument any
// more (it derives terminality from the configured transitions), so what remains
// refusable is the event itself.
func TestAddTicketNoteRejectsBrokenInput(t *testing.T) {
	fixture := newTicketFixture(t)
	id := uuid(729)
	fixture.createTicket(t, openTicket{id: id, clientID: fixture.client})

	cases := []struct {
		name string
		note dto.TicketNoteAppend
	}{
		{"sin ticket", dto.TicketNoteAppend{
			Event: dto.NewTicketEvent{Actor: fixture.client, Kind: dto.TicketEventKindNote},
		}},
		{"sin actor", dto.TicketNoteAppend{
			TicketID: id,
			Event:    dto.NewTicketEvent{Kind: dto.TicketEventKindNote},
		}},
		{"payload inválido", dto.TicketNoteAppend{
			TicketID: id,
			Event:    dto.NewTicketEvent{Actor: fixture.client, Kind: dto.TicketEventKindNote, Payload: "{no es json"},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, applied, err := fixture.repo.AddTicketNote(context.Background(), tc.note); err == nil || applied {
				t.Fatalf("AddTicketNote(%+v) = %t, %v; debía fallar", tc.note, applied, err)
			}
		})
	}
}

// TestTouchTicketMovesActivity pins the activity signal: updated_at moves forward
// (it is what the F2 scheduler reads for inactivity) while the state is untouched.
func TestTouchTicketMovesActivity(t *testing.T) {
	fixture := newTicketFixture(t)
	ctx := context.Background()
	id := uuid(730)
	fixture.createTicket(t, openTicket{id: id, clientID: fixture.client})

	before, found, err := fixture.repo.GetTicket(ctx, id)
	if err != nil || !found {
		t.Fatalf("GetTicket() = %t, %v", found, err)
	}

	// The timestamps have millisecond precision, so the two writes must be at
	// least a millisecond apart to be distinguishable.
	time.Sleep(3 * time.Millisecond)

	touched, err := fixture.repo.TouchTicket(ctx, id)
	if err != nil {
		t.Fatalf("TouchTicket() error inesperado: %v", err)
	}
	if !touched {
		t.Fatal("TouchTicket() no encontró el ticket")
	}

	after, _, err := fixture.repo.GetTicket(ctx, id)
	if err != nil {
		t.Fatalf("GetTicket() error inesperado: %v", err)
	}
	if after.UpdatedAt == before.UpdatedAt {
		t.Fatalf("updated_at = %q, se esperaba que la actividad lo moviera", after.UpdatedAt)
	}
	if after.State != before.State {
		t.Fatalf("state = %q, la actividad no debe cambiar el estado", after.State)
	}

	if touched, err := fixture.repo.TouchTicket(ctx, uuid(731)); err != nil || touched {
		t.Fatalf("TouchTicket(inexistente) = %t, %v; se esperaba false sin error", touched, err)
	}
}

// TestTicketsRepoPropagatesDatabaseFailure pins that an infrastructure failure is
// reported as an error and never as an empty result.
func TestTicketsRepoPropagatesDatabaseFailure(t *testing.T) {
	fixture := newTicketFixture(t)
	ctx := context.Background()
	id := uuid(740)
	fixture.createTicket(t, openTicket{id: id, clientID: fixture.client})

	if _, err := fixture.handle.Exec(`DROP TABLE ticket_events`); err != nil {
		t.Fatalf("romper la tabla de eventos: %v", err)
	}
	if _, err := fixture.repo.ListTicketEvents(ctx, id); err == nil {
		t.Fatal("ListTicketEvents() debía fallar con la tabla ausente")
	}
	if _, err := fixture.repo.AppendTicketEvent(ctx, dto.NewTicketEvent{
		TicketID: id, Actor: fixture.client, Kind: dto.TicketEventKindNote,
	}); err == nil {
		t.Fatal("AppendTicketEvent() debía fallar con la tabla ausente")
	}

	if _, err := fixture.handle.Exec(`DROP TABLE tickets`); err != nil {
		t.Fatalf("romper la tabla de tickets: %v", err)
	}
	if _, _, err := fixture.repo.GetTicket(ctx, id); err == nil {
		t.Fatal("GetTicket() debía fallar con la tabla ausente")
	}
	if _, err := fixture.repo.ListTickets(ctx, dto.TicketQuery{Limit: 10}); err == nil {
		t.Fatal("ListTickets() debía fallar con la tabla ausente")
	}
	if _, err := fixture.repo.TransitionTicket(ctx, dto.TicketTransition{
		TicketID: id,
		From:     stateNew,
		To:       stateResolved,
		Event:    dto.NewTicketEvent{Actor: fixture.client, Kind: dto.TicketEventKindTransition},
	}); err == nil {
		t.Fatal("TransitionTicket() debía fallar con la tabla ausente")
	}
}

// ticketIDs projects the ids of a ticket page.
func ticketIDs(tickets []dto.TicketRecord) []string {
	values := make([]string, 0, len(tickets))
	for _, ticket := range tickets {
		values = append(values, ticket.ID)
	}
	return values
}

// hasTicketID reports whether the page contains want.
func hasTicketID(tickets []dto.TicketRecord, want string) bool {
	for _, ticket := range tickets {
		if ticket.ID == want {
			return true
		}
	}
	return false
}
