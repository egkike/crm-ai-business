package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/egkike/crm-ai-business/internal/application/dto"
	"github.com/egkike/crm-ai-business/internal/application/usecase"
	"github.com/egkike/crm-ai-business/internal/auth"
	"github.com/egkike/crm-ai-business/internal/domain"
	"github.com/egkike/crm-ai-business/internal/mcp"
)

// The ticket tool names, spelled here so the tests pin the wire names of RF5
// independently of the constants the package uses.
const (
	createTicketTool     = "create_ticket"
	listMyTicketsTool    = "list_my_tickets"
	addTicketNoteTool    = "add_ticket_note"
	listTicketsTool      = "list_tickets"
	transitionTicketTool = "transition_ticket"

	// registeredClientID is the internal UUID the fake registration seam assigns
	// to a newly admitted caller (see fakeRegisterClient).
	registeredClientID = "33333333-3333-4333-8333-333333333333"

	// demoCaller is the WhatsApp-style identity of the demo loop: unknown to the
	// resolver until it registers.
	demoCaller = "+5491100007777"

	// staffAccountID is the internal UUID of the staff caller (newFakeResolver).
	staffAccountID = "22222222-2222-4222-8222-222222222222"

	// ventasAreaID is the internal UUID of the seeded 'Ventas' area, mirrored from
	// internal/db like every other seeded identifier this package's fixtures use.
	ventasAreaID = "00000000-0000-4000-8000-000000000101"
)

// fakeTicketStore is the TicketStore double behind the REAL ticket use cases: the
// transport tests go through the real RBAC re-assertion, the real Spanish
// validation and the real audit payloads, and only the persistence is doubled (the
// SQL is pinned by the repository tests).
type fakeTicketStore struct {
	mu      sync.Mutex
	tickets map[string]dto.TicketRecord
	events  map[string][]dto.TicketEventRecord
	// workflows is the engine registry the use cases read, so this double answers
	// "is this state terminal?" the way the real adapter derives it from the
	// config rows.
	workflows *fakeWorkflows
	rotation  *fakeRotation
	nextID    int64
	clock     int
}

func newFakeTicketStore() *fakeTicketStore {
	return &fakeTicketStore{
		tickets:   map[string]dto.TicketRecord{},
		events:    map[string][]dto.TicketEventRecord{},
		workflows: newFakeWorkflows(domain.ConsultaTemplate(), ventaWorkflow()),
		rotation:  &fakeRotation{members: []string{staffAccountID}},
	}
}

// isClosed answers "is this ticket's conversation over?" from the configured
// transitions of its type, like the repository guard does.
func (f *fakeTicketStore) isClosed(ticket dto.TicketRecord) bool {
	config, found := f.workflows.configs[ticket.TicketType]
	if !found {
		return true
	}
	return config.IsTerminal(string(ticket.State))
}

// stamp returns the next fake timestamp.
func (f *fakeTicketStore) stamp() string {
	f.clock++
	return fmt.Sprintf("2026-08-01T10:00:%02d.000Z", f.clock)
}

// append stores one event, assuming the caller holds the lock.
func (f *fakeTicketStore) append(ticketID, actor, kind, payload string) dto.TicketEventRecord {
	f.nextID++
	record := dto.TicketEventRecord{
		ID:        f.nextID,
		TicketID:  ticketID,
		Actor:     actor,
		Kind:      kind,
		Payload:   payload,
		CreatedAt: f.stamp(),
	}
	f.events[ticketID] = append(f.events[ticketID], record)
	return record
}

func (f *fakeTicketStore) CreateTicket(_ context.Context, ticket dto.NewTicket) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	stamp := f.stamp()
	f.tickets[ticket.ID] = dto.TicketRecord{
		ID:           ticket.ID,
		TicketTypeID: dto.ConsultaTicketTypeID,
		TicketType:   ticket.TicketType,
		ClientID:     ticket.ClientID,
		State:        ticket.State,
		AreaID:       ticket.AreaID,
		DeadlineAt:   ticket.DeadlineAt,
		CreatedAt:    stamp,
		UpdatedAt:    stamp,
	}
	f.append(ticket.ID, ticket.OpeningEvent.Actor, ticket.OpeningEvent.Kind, ticket.OpeningEvent.Payload)
	return nil
}

func (f *fakeTicketStore) GetTicket(_ context.Context, id string) (dto.TicketRecord, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	record, ok := f.tickets[id]
	return record, ok, nil
}

func (f *fakeTicketStore) ListTicketsByClient(_ context.Context, clientID string, includeClosed bool, limit, offset int) ([]dto.TicketRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.page(clientID, includeClosed, limit, offset), nil
}

func (f *fakeTicketStore) ListTickets(_ context.Context, query dto.TicketQuery) ([]dto.TicketRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.page(query.ClientID, query.IncludeClosed, query.Limit, query.Offset), nil
}

// page filters and sorts the stored tickets, assuming the caller holds the lock.
func (f *fakeTicketStore) page(clientID string, includeClosed bool, limit, offset int) []dto.TicketRecord {
	matches := make([]dto.TicketRecord, 0, len(f.tickets))
	for _, ticket := range f.tickets {
		if clientID != "" && ticket.ClientID != clientID {
			continue
		}
		if !includeClosed && f.isClosed(ticket) {
			continue
		}
		matches = append(matches, ticket)
	}
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].UpdatedAt > matches[j].UpdatedAt
	})
	if offset >= len(matches) {
		return nil
	}
	matches = matches[offset:]
	if limit < len(matches) {
		matches = matches[:limit]
	}
	return matches
}

func (f *fakeTicketStore) TransitionTicket(_ context.Context, transition dto.TicketTransition) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	current, ok := f.tickets[transition.TicketID]
	if !ok || current.State != transition.From {
		return false, nil
	}
	current.State = transition.To
	current.DeadlineAt = transition.DeadlineAt
	if transition.Closes {
		current.ClosedAt = f.stamp()
	}
	if transition.AssignTo != "" {
		current.AssignedTo = transition.AssignTo
	}
	current.UpdatedAt = f.stamp()
	f.tickets[transition.TicketID] = current
	f.append(transition.TicketID, transition.Event.Actor, transition.Event.Kind, transition.Event.Payload)
	return true, nil
}

// AddTicketNote is the atomic note write: it refuses a ticket that is not open
// (returning found=false and writing nothing) and otherwise stores the event AND
// moves the activity, exactly like the repository transaction.
func (f *fakeTicketStore) AddTicketNote(_ context.Context, note dto.TicketNoteAppend) (dto.TicketRecord, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	current, ok := f.tickets[note.TicketID]
	if !ok || f.isClosed(current) {
		return dto.TicketRecord{}, false, nil
	}

	current.UpdatedAt = f.stamp()
	f.tickets[note.TicketID] = current
	f.append(note.TicketID, note.Event.Actor, note.Event.Kind, note.Event.Payload)
	return current, true, nil
}

func (f *fakeTicketStore) ListTicketEvents(_ context.Context, ticketID string) ([]dto.TicketEventRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]dto.TicketEventRecord(nil), f.events[ticketID]...), nil
}

// fakeWorkflows is the domain.WorkflowConfigStore double of this package: the
// seeded templates plus whatever a test registers.
type fakeWorkflows struct {
	configs map[string]domain.WorkflowConfig
}

func newFakeWorkflows(configs ...domain.WorkflowConfig) *fakeWorkflows {
	workflows := &fakeWorkflows{configs: map[string]domain.WorkflowConfig{}}
	for _, config := range configs {
		workflows.configs[config.TypeName] = config
	}
	return workflows
}

func (f *fakeWorkflows) LoadConfig(_ context.Context, typeName string) (domain.WorkflowConfig, bool, error) {
	config, found := f.configs[typeName]
	return config, found, nil
}

func (f *fakeWorkflows) SaveConfig(context.Context, domain.WorkflowConfig) (bool, error) {
	return false, errors.New("fakeWorkflows: SaveConfig no está implementado")
}

// fakeRotation is the AreaRotator double: a deterministic ring of members.
type fakeRotation struct {
	members []string
	next    int
}

func (f *fakeRotation) NextAreaAssignee(_ context.Context, _ string) (string, error) {
	if len(f.members) == 0 {
		return "", nil
	}
	member := f.members[f.next%len(f.members)]
	f.next++
	return member, nil
}

// ventaWorkflow is the 'venta' template linked to the seeded Ventas area (the
// seeding migration owns the link).
func ventaWorkflow() domain.WorkflowConfig {
	config := domain.VentaTemplate()
	config.DefaultAreaID = ventasAreaID
	return config
}

// fakeTicketDirectory is the ClientDirectory double: it knows the client ids the
// fixtures use.
type fakeTicketDirectory struct {
	known map[string]bool
}

func newFakeTicketDirectory() *fakeTicketDirectory {
	return &fakeTicketDirectory{known: map[string]bool{
		registeredClientID:                     true,
		"44444444-4444-4444-8444-444444444444": true,
	}}
}

func (f *fakeTicketDirectory) ClientExistsByID(_ context.Context, id string) (bool, error) {
	return f.known[id], nil
}

// ticketsFixture is the transport over the real RF5 (and RF4) use cases plus the F1
// registration seam, so both the wire and the layers underneath are observable.
type ticketsFixture struct {
	*ports
	handler http.Handler
	store   *fakeTicketStore
	catalog *fakeCatalogStore
}

// newTicketsFixture wires the catalog and ticket tools into the handler's default
// set (the same way the composition root does) and adds a client-role identity,
// because the client channel tools have to be reachable by the end client.
func newTicketsFixture(t *testing.T) *ticketsFixture {
	t.Helper()

	ports := newPorts()
	ports.resolver.add(emailCaller, auth.Caller{
		ID:   "44444444-4444-4444-8444-444444444444",
		Name: "Cliente",
		Role: auth.RoleClient,
	})

	catalog := newFakeCatalogStore()
	search, err := usecase.NewSearchCatalog(catalog)
	if err != nil {
		t.Fatalf("NewSearchCatalog() error inesperado: %v", err)
	}
	create, err := usecase.NewCreateCatalogItem(catalog)
	if err != nil {
		t.Fatalf("NewCreateCatalogItem() error inesperado: %v", err)
	}
	update, err := usecase.NewUpdateCatalogItem(catalog)
	if err != nil {
		t.Fatalf("NewUpdateCatalogItem() error inesperado: %v", err)
	}

	store := newFakeTicketStore()
	directory := newFakeTicketDirectory()

	createTicket, err := usecase.NewCreateTicket(store, directory, store.workflows)
	if err != nil {
		t.Fatalf("NewCreateTicket() error inesperado: %v", err)
	}
	listMine, err := usecase.NewListMyTickets(store)
	if err != nil {
		t.Fatalf("NewListMyTickets() error inesperado: %v", err)
	}
	listAll, err := usecase.NewListTickets(store)
	if err != nil {
		t.Fatalf("NewListTickets() error inesperado: %v", err)
	}
	addNote, err := usecase.NewAddTicketNote(store, store.workflows)
	if err != nil {
		t.Fatalf("NewAddTicketNote() error inesperado: %v", err)
	}
	transition, err := usecase.NewTransitionTicket(store, store.workflows, store.rotation)
	if err != nil {
		t.Fatalf("NewTransitionTicket() error inesperado: %v", err)
	}

	handler, err := mcp.NewHandler(mcp.Dependencies{
		CallerResolver: ports.resolver,
		RegisterClient: ports.register,
		Catalog:        &mcp.CatalogDependencies{Search: search, Create: create, Update: update},
		Tickets: &mcp.TicketDependencies{
			Create:     createTicket,
			ListMine:   listMine,
			ListAll:    listAll,
			AddNote:    addNote,
			Transition: transition,
		},
	})
	if err != nil {
		t.Fatalf("NewHandler() = %v, se espera un handler", err)
	}

	return &ticketsFixture{ports: ports, handler: handler, store: store, catalog: catalog}
}

// ticketWire is the ticket object as it appears on the wire.
type ticketWire struct {
	ID           string `json:"id"`
	TicketTypeID string `json:"ticket_type_id"`
	TicketType   string `json:"ticket_type"`
	ClientID     string `json:"client_id"`
	State        string `json:"state"`
	AssignedTo   string `json:"assigned_to"`
	AreaID       string `json:"area_id"`
	DeadlineAt   string `json:"deadline_at"`
	ClosedAt     string `json:"closed_at"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
	Events       []struct {
		ID        int64           `json:"id"`
		Actor     string          `json:"actor"`
		Kind      string          `json:"kind"`
		Payload   json.RawMessage `json:"payload"`
		CreatedAt string          `json:"created_at"`
	} `json:"events"`
}

// ticketCallResult decodes a successful ticket tool call.
func ticketCallResult(t *testing.T, response rpcResponse) ticketWire {
	t.Helper()

	if response.Error != nil {
		t.Fatalf("la herramienta falló: %+v", response.Error)
	}

	var result callToolResult
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatalf("tools/call result ilegible: %v (%s)", err, response.Result)
	}
	if result.IsError {
		t.Fatalf("la herramienta devolvió isError: %+v", result.Content)
	}

	var ticket ticketWire
	if err := json.Unmarshal(result.StructuredContent, &ticket); err != nil {
		t.Fatalf("structuredContent ilegible: %v (%s)", err, result.StructuredContent)
	}
	return ticket
}

// ticketListCallResult decodes a successful list tool call into its tickets.
func ticketListCallResult(t *testing.T, response rpcResponse) []ticketWire {
	t.Helper()

	if response.Error != nil {
		t.Fatalf("la herramienta falló: %+v", response.Error)
	}

	var result callToolResult
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatalf("tools/call result ilegible: %v (%s)", err, response.Result)
	}

	var page struct {
		Tickets []ticketWire `json:"tickets"`
		Limit   int          `json:"limit"`
		Offset  int          `json:"offset"`
	}
	if err := json.Unmarshal(result.StructuredContent, &page); err != nil {
		t.Fatalf("structuredContent ilegible: %v (%s)", err, result.StructuredContent)
	}
	return page.Tickets
}

// TestTicketToolsListFiltersByRole pins the RF2 half of RF5: the client channel sees
// the three client tools and never the internal queue or the state machine; staff
// and owner see the whole ticket surface; an unknown sender still sees only the
// registration seam.
func TestTicketToolsListFiltersByRole(t *testing.T) {
	fixture := newTicketsFixture(t)

	clientSet := []string{registerClientToolName, searchCatalogTool, createTicketTool, listMyTicketsTool, addTicketNoteTool}
	staffSet := append(append([]string{}, clientSet...), listTicketsTool, transitionTicketTool)

	cases := []struct {
		name     string
		callerID string
		want     []string
		notWant  []string
	}{
		{
			name:     "cliente",
			callerID: emailCaller,
			want:     clientSet,
			notWant:  []string{listTicketsTool, transitionTicketTool, createCatalogItemTool, updateCatalogItemTool},
		},
		{
			name:     "staff",
			callerID: staffCaller,
			want:     staffSet,
			notWant:  []string{createCatalogItemTool, updateCatalogItemTool},
		},
		{
			name:     "owner",
			callerID: ownerCaller,
			want:     append(append([]string{}, staffSet...), createCatalogItemTool, updateCatalogItemTool),
		},
		{
			name:     "desconocido",
			callerID: "+5491100009999",
			want:     []string{registerClientToolName},
			notWant:  []string{searchCatalogTool, createTicketTool, listMyTicketsTool, addTicketNoteTool, listTicketsTool, transitionTicketTool},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			names := listTools(t, fixture.handler, tc.callerID)
			for _, want := range tc.want {
				if !contains(names, want) {
					t.Fatalf("tools/list = %v, falta %q", names, want)
				}
			}
			for _, notWant := range tc.notWant {
				if contains(names, notWant) {
					t.Fatalf("tools/list = %v, no debe anunciar %q", names, notWant)
				}
			}
		})
	}

	// The client's set is exactly the five client tools: no internal tool can leak
	// into it by accident.
	clientNames := listTools(t, fixture.handler, emailCaller)
	sort.Strings(clientNames)
	if len(clientNames) != len(clientSet) {
		t.Fatalf("tools/list del cliente = %v, se esperaban exactamente %v", clientNames, clientSet)
	}
}

// TestInternalTicketToolsAreRefusedForClients pins the other half of RF2: the tools
// are not only hidden, they are refused before any handler runs, with the Spanish
// role denial of the registry (-32000 / role_not_allowed).
func TestInternalTicketToolsAreRefusedForClients(t *testing.T) {
	fixture := newTicketsFixture(t)
	created := ticketCallResult(t, callTool(t, fixture.handler, emailCaller, createTicketTool, `{"motive":"consulta por corte de pelo"}`))

	for _, tool := range []string{listTicketsTool, transitionTicketTool} {
		t.Run(tool, func(t *testing.T) {
			response := callTool(t, fixture.handler, emailCaller, tool,
				`{"ticket_id":"`+created.ID+`","state":"atendido"}`)

			if response.Error == nil {
				t.Fatalf("se esperaba error JSON-RPC, result: %s", response.Result)
			}
			if response.Error.Code != -32000 {
				t.Fatalf("code = %d, se espera -32000", response.Error.Code)
			}
			if got := response.dataCode(t); got != "role_not_allowed" {
				t.Fatalf("data.code = %q, se esperaba role_not_allowed", got)
			}
		})
	}

	// Nothing was executed: the ticket is still in its initial state and its history
	// has only the creation event.
	stored, found, err := fixture.store.GetTicket(context.Background(), created.ID)
	if err != nil || !found {
		t.Fatalf("GetTicket() = %t, %v", found, err)
	}
	if stored.State != dto.TicketStateNew {
		t.Fatalf("state = %q, la transición se ejecutó pese al rechazo", stored.State)
	}
	if events, _ := fixture.store.ListTicketEvents(context.Background(), created.ID); len(events) != 1 {
		t.Fatalf("eventos = %d, no debe haber eventos de una llamada rechazada", len(events))
	}
}

// TestTicketDemoValueLoopOverHTTP pins the F1 demo end to end through the real HTTP
// surface: client registers, searches the catalog, opens a ticket; staff lists it
// and moves it; the client re-engages with a note; and every step is audited with
// its actor.
func TestTicketDemoValueLoopOverHTTP(t *testing.T) {
	fixture := newTicketsFixture(t)
	ctx := context.Background()

	// 1. The unknown sender sees only the registration seam.
	if names := listTools(t, fixture.handler, demoCaller); !contains(names, registerClientToolName) || contains(names, createTicketTool) {
		t.Fatalf("tools/list del desconocido = %v, se esperaba solo %q", names, registerClientToolName)
	}

	// 2. It registers, and the identity chain now resolves it as a client.
	var registration struct {
		ClientID string `json:"client_id"`
		Created  bool   `json:"created"`
	}
	registrationResponse := callTool(t, fixture.handler, demoCaller, registerClientToolName, `{}`)
	var registeredResult callToolResult
	if err := json.Unmarshal(registrationResponse.Result, &registeredResult); err != nil {
		t.Fatalf("register_client result ilegible: %v", err)
	}
	if err := json.Unmarshal(registeredResult.StructuredContent, &registration); err != nil {
		t.Fatalf("register_client structuredContent ilegible: %v", err)
	}
	if !registration.Created || registration.ClientID != registeredClientID {
		t.Fatalf("registro = %+v, se esperaba el alta del cliente", registration)
	}
	if names := listTools(t, fixture.handler, demoCaller); !contains(names, createTicketTool) {
		t.Fatalf("tools/list tras el registro = %v, se esperaban las herramientas del cliente", names)
	}

	// 3. The catalog answers the client's own words.
	searched := catalogSearchItems(t, callTool(t, fixture.handler, demoCaller, searchCatalogTool, `{"query":"dolor lumbar"}`))
	if len(searched) != 1 || searched[0].UseCases != "Dolor lumbar" {
		t.Fatalf("resultados = %+v, se esperaba el servicio con su caso de uso", searched)
	}

	// 4. The contact with a business motive opens a ticket, audited with the actor.
	created := ticketCallResult(t, callTool(t, fixture.handler, demoCaller, createTicketTool,
		`{"motive":"me duele la espalda desde el lunes"}`))
	if created.ClientID != registeredClientID {
		t.Fatalf("client_id = %q, se esperaba el del caller registrado", created.ClientID)
	}
	if created.State != string(dto.TicketStateNew) {
		t.Fatalf("state = %q, se esperaba %q", created.State, dto.TicketStateNew)
	}
	if len(created.Events) != 1 || created.Events[0].Kind != dto.TicketEventKindCreated {
		t.Fatalf("eventos = %+v, se esperaba el evento de creación", created.Events)
	}
	if created.Events[0].Actor != registeredClientID {
		t.Fatalf("actor = %q, se esperaba el del cliente", created.Events[0].Actor)
	}

	// 5. The staff sees it in the internal queue.
	listed := ticketListCallResult(t, callTool(t, fixture.handler, staffCaller, listTicketsTool, `{}`))
	if len(listed) != 1 || listed[0].ID != created.ID {
		t.Fatalf("tickets = %+v, se esperaba el ticket del cliente", listed)
	}

	// 6. The staff attends it; the transition is audited with the staff identity.
	attended := ticketCallResult(t, callTool(t, fixture.handler, staffCaller, transitionTicketTool,
		`{"ticket_id":"`+created.ID+`","state":"atendido"}`))
	if attended.State != string(dto.TicketStateAttended) {
		t.Fatalf("state = %q, se esperaba %q", attended.State, dto.TicketStateAttended)
	}
	if len(attended.Events) != 2 {
		t.Fatalf("eventos = %d, se esperaba la creación y la transición", len(attended.Events))
	}
	transition := attended.Events[1]
	if transition.Kind != dto.TicketEventKindTransition {
		t.Fatalf("kind = %q, se esperaba %q", transition.Kind, dto.TicketEventKindTransition)
	}
	if transition.Actor != "22222222-2222-4222-8222-222222222222" {
		t.Fatalf("actor = %q, se esperaba el del staff", transition.Actor)
	}
	var transitionPayload struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if err := json.Unmarshal(transition.Payload, &transitionPayload); err != nil {
		t.Fatalf("payload de la transición ilegible: %v (%s)", err, transition.Payload)
	}
	if transitionPayload.From != string(dto.TicketStateNew) || transitionPayload.To != string(dto.TicketStateAttended) {
		t.Fatalf("payload = %+v, se esperaba el estado de origen y destino", transitionPayload)
	}

	// 7. The client comes back about the same thing: the message joins the open
	// ticket as an event, and no second ticket is created.
	reengaged := ticketCallResult(t, callTool(t, fixture.handler, demoCaller, addTicketNoteTool,
		`{"ticket_id":"`+created.ID+`","text":"sigo con el dolor, mi número es 1155550000"}`))
	if len(reengaged.Events) != 3 {
		t.Fatalf("eventos = %d, se esperaba la nota de re-engagement", len(reengaged.Events))
	}
	note := reengaged.Events[2]
	if note.Kind != dto.TicketEventKindNote || note.Actor != registeredClientID {
		t.Fatalf("nota = %+v, se esperaba la nota con el actor del cliente", note)
	}
	var notePayload struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(note.Payload, &notePayload); err != nil {
		t.Fatalf("payload de la nota ilegible: %v", err)
	}
	if notePayload.Text == "" || notePayload.Text == "sigo con el dolor, mi número es 1155550000" {
		t.Fatalf("text = %q, se esperaba el mensaje con el teléfono enmascarado", notePayload.Text)
	}

	// 8. The client's own list sees that single ticket with its full history, and the
	// staff's read-back shows both actors: the audit survives the whole loop.
	own := ticketListCallResult(t, callTool(t, fixture.handler, demoCaller, listMyTicketsTool, `{}`))
	if len(own) != 1 || own[0].ID != created.ID {
		t.Fatalf("tickets = %+v, se esperaba el ticket del cliente y ninguno más", own)
	}
	if len(own[0].Events) != 3 {
		t.Fatalf("eventos = %d, se esperaba el historial completo", len(own[0].Events))
	}

	actors := make([]string, 0, len(own[0].Events))
	for _, event := range own[0].Events {
		actors = append(actors, event.Actor)
	}
	if actors[0] != registeredClientID || actors[1] != "22222222-2222-4222-8222-222222222222" || actors[2] != registeredClientID {
		t.Fatalf("actores = %v, se esperaba cliente, staff y cliente", actors)
	}

	// Only one ticket exists: the re-engagement did not open a second one.
	stored, err := fixture.store.ListTickets(ctx, dto.TicketQuery{Limit: 10, IncludeClosed: true})
	if err != nil {
		t.Fatalf("ListTickets() error inesperado: %v", err)
	}
	if len(stored) != 1 {
		t.Fatalf("tickets = %d, se esperaba uno solo (el re-engagement no crea otro)", len(stored))
	}
}

// catalogSearchItems decodes a search_catalog answer into the fields this test reads.
type catalogSearchItem struct {
	ID       string `json:"id"`
	UseCases string `json:"use_cases"`
}

func catalogSearchItems(t *testing.T, response rpcResponse) []catalogSearchItem {
	t.Helper()

	if response.Error != nil {
		t.Fatalf("search_catalog falló: %+v", response.Error)
	}

	var result callToolResult
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatalf("tools/call result ilegible: %v", err)
	}
	var output struct {
		Items []catalogSearchItem `json:"items"`
	}
	if err := json.Unmarshal(result.StructuredContent, &output); err != nil {
		t.Fatalf("structuredContent ilegible: %v", err)
	}
	return output.Items
}

// TestStaffOpensATicketOnBehalf pins the internal creation path over the wire: the
// staff names the client, and the ticket is attributed to that client while the
// creation event carries the staff actor.
func TestStaffOpensATicketOnBehalf(t *testing.T) {
	fixture := newTicketsFixture(t)

	created := ticketCallResult(t, callTool(t, fixture.handler, staffCaller, createTicketTool,
		`{"motive":"reclamo recibido por teléfono","client_id":"44444444-4444-4444-8444-444444444444"}`))
	if created.ClientID != "44444444-4444-4444-8444-444444444444" {
		t.Fatalf("client_id = %q, se esperaba el cliente indicado", created.ClientID)
	}
	if len(created.Events) != 1 || created.Events[0].Actor != "22222222-2222-4222-8222-222222222222" {
		t.Fatalf("eventos = %+v, se esperaba la creación con el actor del staff", created.Events)
	}

	// An unknown client is the semantic rejection of the use case, not a driver error.
	response := callTool(t, fixture.handler, staffCaller, createTicketTool,
		`{"motive":"reclamo","client_id":"00000000-0000-4000-8000-0000000000ff"}`)
	assertSemanticWire(t, response, usecase.CodeClientNotFound, "no existe un cliente con ese identificador.")
}

// TestClientCannotNameTheTicketOwner pins review R1-ticket-clientid-trust: the
// owner of a ticket is never an argument. A client sending client_id is refused as
// a protocol error BEFORE the use case runs (defense in depth over the use case
// that overwrites the field), and an internal caller keeps the argument because the
// internal path needs it.
func TestClientCannotNameTheTicketOwner(t *testing.T) {
	fixture := newTicketsFixture(t)

	response := callTool(t, fixture.handler, emailCaller, createTicketTool,
		`{"motive":"consulta","client_id":"55555555-5555-4555-8555-555555555555"}`)
	assertInvalidArguments(t, response)

	// The refusal happens before the use case: nothing was created.
	stored, err := fixture.store.ListTickets(context.Background(), dto.TicketQuery{IncludeClosed: true, Limit: 50})
	if err != nil {
		t.Fatalf("ListTickets() error inesperado: %v", err)
	}
	if len(stored) != 0 {
		t.Fatalf("tickets = %v, el rechazo no debe crear nada", stored)
	}

	// The internal side keeps the same argument: it is how a call produces a ticket
	// for the client it was about.
	created := ticketCallResult(t, callTool(t, fixture.handler, staffCaller, createTicketTool,
		`{"motive":"reclamo recibido por teléfono","client_id":"44444444-4444-4444-8444-444444444444"}`))
	if created.ClientID != "44444444-4444-4444-8444-444444444444" {
		t.Fatalf("client_id = %q, se esperaba el cliente indicado por el staff", created.ClientID)
	}
}

// TestTicketDecodersEnforceSchemaBounds pins review R3-schema-decoder-bounds: every
// bound the input schemas declare is enforced by the decoder, so an oversized value
// is a protocol rejection (-32602 / invalid_arguments) at the transport instead of a
// business rejection one layer down.
func TestTicketDecodersEnforceSchemaBounds(t *testing.T) {
	fixture := newTicketsFixture(t)
	created := ticketCallResult(t, callTool(t, fixture.handler, emailCaller, createTicketTool, `{"motive":"consulta"}`))

	cases := []struct {
		name      string
		tool      string
		arguments string
	}{
		{
			name:      "limit sobre el máximo",
			tool:      listMyTicketsTool,
			arguments: `{"limit":51}`,
		},
		{
			name:      "motivo sobre el máximo",
			tool:      createTicketTool,
			arguments: `{"motive":"` + strings.Repeat("a", 121) + `"}`,
		},
		{
			name:      "texto de nota sobre el máximo",
			tool:      addTicketNoteTool,
			arguments: `{"ticket_id":"` + created.ID + `","text":"` + strings.Repeat("a", 2001) + `"}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertInvalidArguments(t, callTool(t, fixture.handler, emailCaller, tc.tool, tc.arguments))
		})
	}

	// Nothing was written by any of the refusals.
	if events, err := fixture.store.ListTicketEvents(context.Background(), created.ID); err != nil || len(events) != 1 {
		t.Fatalf("eventos = %d, %v; los rechazos de argumentos no deben escribir", len(events), err)
	}
}

// TestTicketDecodersAcceptTheBoundary pins the other half of the bounds: exactly
// 50 tickets per page, 120 runes of motive and 2000 runes of note are INSIDE the
// declared schema and must be accepted, so the enforcement is a bound and not an
// off-by-one rejection.
func TestTicketDecodersAcceptTheBoundary(t *testing.T) {
	fixture := newTicketsFixture(t)

	created := ticketCallResult(t, callTool(t, fixture.handler, emailCaller, createTicketTool,
		`{"motive":"`+strings.Repeat("a", 120)+`"}`))
	if len(created.Events) != 1 {
		t.Fatalf("eventos = %d, se esperaba la creación del ticket con el motivo límite", len(created.Events))
	}

	page := ticketListCallResult(t, callTool(t, fixture.handler, emailCaller, listMyTicketsTool, `{"limit":50}`))
	if len(page) != 1 {
		t.Fatalf("tickets = %d, se esperaba el ticket con el límite máximo de página", len(page))
	}

	noted := ticketCallResult(t, callTool(t, fixture.handler, emailCaller, addTicketNoteTool,
		`{"ticket_id":"`+created.ID+`","text":"`+strings.Repeat("a", 2000)+`"}`))
	if len(noted.Events) != 2 {
		t.Fatalf("eventos = %d, se esperaba la nota con el texto límite", len(noted.Events))
	}
}

// assertInvalidArguments pins a protocol-level argument rejection: -32602 with the
// transport's own data.code.
func assertInvalidArguments(t *testing.T, response rpcResponse) {
	t.Helper()

	if response.Error == nil {
		t.Fatalf("se esperaba error JSON-RPC, result: %s", response.Result)
	}
	if response.Error.Code != -32602 {
		t.Fatalf("code = %d, se espera -32602", response.Error.Code)
	}
	if got := response.dataCode(t); got != "invalid_arguments" {
		t.Fatalf("data.code = %q, se esperaba invalid_arguments", got)
	}
}

// TestClientCannotReadAnotherClientsTickets pins the anti-oracle rule over the wire:
// a client naming another client's id in the body still receives only its own
// tickets (the use case overwrites the filter), and naming somebody else's ticket in
// a note answers exactly like a ticket that does not exist.
func TestClientCannotReadAnotherClientsTickets(t *testing.T) {
	fixture := newTicketsFixture(t)
	const otherClient = "55555555-5555-4555-8555-555555555555"

	fixture.store.mu.Lock()
	fixture.store.tickets["foreign-ticket"] = dto.TicketRecord{
		ID:           "foreign-ticket",
		TicketTypeID: dto.ConsultaTicketTypeID,
		ClientID:     otherClient,
		State:        dto.TicketStateNew,
		CreatedAt:    "2026-08-01T09:00:00.000Z",
		UpdatedAt:    "2026-08-01T09:00:00.000Z",
	}
	fixture.store.mu.Unlock()

	listed := ticketListCallResult(t, callTool(t, fixture.handler, emailCaller, listMyTicketsTool,
		`{"client_id":"`+otherClient+`"}`))
	if len(listed) != 0 {
		t.Fatalf("tickets = %+v, el cliente no debe ver los de otro", listed)
	}

	response := callTool(t, fixture.handler, emailCaller, addTicketNoteTool,
		`{"ticket_id":"foreign-ticket","text":"hola"}`)
	assertSemanticWire(t, response, usecase.CodeTicketNotFound, "no existe un ticket con ese identificador.")
}

// TestTicketToolsRejectBadArguments pins the validation boundary: unknown arguments
// are refused as protocol errors, and the business rejections the use cases phrase
// travel as -32002 with their code and Spanish prose.
func TestTicketToolsRejectBadArguments(t *testing.T) {
	fixture := newTicketsFixture(t)

	t.Run("argumento adicional", func(t *testing.T) {
		response := callTool(t, fixture.handler, emailCaller, createTicketTool, `{"motive":"consulta","role":"owner"}`)
		if response.Error == nil || response.Error.Code != -32602 {
			t.Fatalf("response = %+v, se esperaba -32602", response.Error)
		}
		if got := response.dataCode(t); got != "invalid_arguments" {
			t.Fatalf("data.code = %q, se esperaba invalid_arguments", got)
		}
	})

	t.Run("motivo ausente", func(t *testing.T) {
		response := callTool(t, fixture.handler, emailCaller, createTicketTool, `{}`)
		assertSemanticWire(t, response, usecase.CodeInvalidTicket,
			"el motivo del ticket es obligatorio: un mensaje sin motivo de negocio no genera ticket.")
	})

	t.Run("nota sin texto", func(t *testing.T) {
		created := ticketCallResult(t, callTool(t, fixture.handler, emailCaller, createTicketTool, `{"motive":"consulta"}`))
		response := callTool(t, fixture.handler, emailCaller, addTicketNoteTool, `{"ticket_id":"`+created.ID+`"}`)
		assertSemanticWire(t, response, usecase.CodeInvalidTicket, "el texto de la nota es obligatorio.")
	})

	t.Run("estado fuera del workflow", func(t *testing.T) {
		created := ticketCallResult(t, callTool(t, fixture.handler, emailCaller, createTicketTool, `{"motive":"consulta"}`))
		response := callTool(t, fixture.handler, staffCaller, transitionTicketTool,
			`{"ticket_id":"`+created.ID+`","state":"en-proceso"}`)
		// The vocabulary is the configured one, so the rejection is the engine's
		// (RF6), not a decoder enum's (rider R1-transition-state-decode).
		assertSemanticWire(t, response, usecase.CodeInvalidTicketState,
			"el estado 'en-proceso' no existe en el workflow del tipo 'consulta'.")
	})

	t.Run("transición no declarada", func(t *testing.T) {
		created := ticketCallResult(t, callTool(t, fixture.handler, emailCaller, createTicketTool, `{"motive":"consulta"}`))
		response := callTool(t, fixture.handler, staffCaller, transitionTicketTool,
			`{"ticket_id":"`+created.ID+`","state":"resuelto"}`)
		assertSemanticWire(t, response, usecase.CodeInvalidTicketState,
			"no se puede pasar el ticket del estado 'nuevo' al 'resuelto': esa transición no está declarada en el workflow del tipo 'consulta'.")
	})

	t.Run("ticket inexistente", func(t *testing.T) {
		response := callTool(t, fixture.handler, staffCaller, transitionTicketTool,
			`{"ticket_id":"00000000-0000-4000-8000-0000000000ff","state":"atendido"}`)
		assertSemanticWire(t, response, usecase.CodeTicketNotFound, "no existe un ticket con ese identificador.")
	})

	t.Run("argumentos que no son objeto", func(t *testing.T) {
		response := callTool(t, fixture.handler, emailCaller, createTicketTool, `"consulta"`)
		if response.Error == nil || response.Error.Code != -32602 {
			t.Fatalf("response = %+v, se esperaba -32602", response.Error)
		}
	})
}

// TestTicketToolsRefusePartialWiring pins the construction rule: a partially wired
// ticket surface refuses to build instead of announcing tools it cannot serve.
func TestTicketToolsRefusePartialWiring(t *testing.T) {
	ports := newPorts()
	store := newFakeTicketStore()
	create, err := usecase.NewCreateTicket(store, newFakeTicketDirectory(), store.workflows)
	if err != nil {
		t.Fatalf("NewCreateTicket() error inesperado: %v", err)
	}

	handler, err := mcp.NewHandler(mcp.Dependencies{
		CallerResolver: ports.resolver,
		RegisterClient: ports.register,
		Tickets:        &mcp.TicketDependencies{Create: create},
	})
	if err == nil {
		t.Fatal("NewHandler() con los tickets a medio cablear debía fallar")
	}
	if handler != nil {
		t.Fatalf("NewHandler() devolvió un handler utilizable: %+v", handler)
	}

	// Nil Tickets means "no ticket tools", which is a construction fact, not an
	// error.
	withoutTickets, err := mcp.NewHandler(mcp.Dependencies{
		CallerResolver: ports.resolver,
		RegisterClient: ports.register,
	})
	if err != nil {
		t.Fatalf("NewHandler() sin tickets = %v, se espera un handler", err)
	}
	if names := listTools(t, withoutTickets, ownerCaller); contains(names, createTicketTool) {
		t.Fatalf("tools/list = %v, no debe anunciar herramientas de ticket sin cablear", names)
	}
}

// assertSemanticWire pins a use-case rejection as it reaches the wire: -32002, the
// application code in data.code and the exact Spanish prose.
func assertSemanticWire(t *testing.T, response rpcResponse, code usecase.ErrorCode, message string) {
	t.Helper()

	if response.Error == nil {
		t.Fatalf("se esperaba error JSON-RPC, result: %s", response.Result)
	}
	if response.Error.Code != -32002 {
		t.Fatalf("code = %d, se espera -32002", response.Error.Code)
	}
	if got := response.dataCode(t); got != string(code) {
		t.Fatalf("data.code = %q, se esperaba %q", got, code)
	}
	if response.Error.Message != message {
		t.Fatalf("message = %q, se esperaba %q", response.Error.Message, message)
	}
}
