package usecase_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/egkike/crm-ai-business/internal/application/dto"
	"github.com/egkike/crm-ai-business/internal/application/usecase"
	"github.com/egkike/crm-ai-business/internal/auth"
	"github.com/egkike/crm-ai-business/internal/domain"
)

// This file is the F2 T3 engine contract (odd/tasks/workflow-engine.md, S1/S2):
// the creation and the transition EXECUTE the workflow configured for the
// ticket's type, and the closed/deadline/assignment semantics come from that same
// configuration instead of from the retired fixed vocabulary.
//
// The tests go through the use cases only — never through internal storage — and
// the workflows they run are the seeded templates of PRD §3.7 ('venta' with its
// round-robin into Ventas, 'consulta' nuevo → atendido → resuelto) plus one
// admin-gated config that exists only to pin the role comparison.

// ventasArea is the internal UUID of the seeded 'Ventas' area (RF7). The domain
// template ships DefaultAreaID empty on purpose; the seeding migration owns the
// link, and the tests mirror it.
const ventasArea = "00000000-0000-4000-8000-000000000101"

// hours is a pointer helper for optional deadline values.
func hours(value int) *int { return &value }

// ventaWorkflow is the 'venta' template with the Ventas link the seed applies.
func ventaWorkflow() domain.WorkflowConfig {
	config := domain.VentaTemplate()
	config.DefaultAreaID = ventasArea
	return config
}

// deadlineWorkflow is a two-step pipeline whose middle state declares a 48-hour
// deadline (the RF6 criterion shape: "un estado con deadline_hours=48") and whose
// end state is terminal.
func deadlineWorkflow() domain.WorkflowConfig {
	return domain.WorkflowConfig{
		TypeName:     "soporte",
		InitialState: "nuevo",
		States: []domain.StateConfig{
			{Name: "nuevo"},
			{Name: "en-proceso", DeadlineHours: hours(48)},
			{Name: "resuelto"},
		},
		Transitions: []domain.TransitionConfig{
			{From: "nuevo", To: "en-proceso", MinRole: domain.TransitionRoleStaff},
			{From: "en-proceso", To: "resuelto", MinRole: domain.TransitionRoleStaff},
		},
	}
}

// roleWorkflow is the escalation pipeline of the role gate: a staff transition, an
// admin one and an owner one, so "below the minimum role" is observable at every
// step instead of only for the client (which the transport-level gate already
// refuses).
func roleWorkflow() domain.WorkflowConfig {
	return domain.WorkflowConfig{
		TypeName:     "escalado",
		InitialState: "nuevo",
		States: []domain.StateConfig{
			{Name: "nuevo"},
			{Name: "revisado"},
			{Name: "escalado"},
			{Name: "cerrado"},
		},
		Transitions: []domain.TransitionConfig{
			{From: "nuevo", To: "revisado", MinRole: domain.TransitionRoleStaff},
			{From: "revisado", To: "escalado", MinRole: domain.TransitionRoleAdmin},
			{From: "escalado", To: "cerrado", MinRole: domain.TransitionRoleOwner},
		},
	}
}

// engineStore builds a store whose registry holds the given workflows and no
// other, so a test can prove the fail-closed answers too.
func engineStore(configs ...domain.WorkflowConfig) *fakeTickets {
	store := newFakeTickets()
	store.workflows = newFakeWorkflows(configs...)
	return store
}

// assertDeadlineCloseTo pins a computed deadline: the instant the state declares,
// measured from the transition, without pinning the wall clock.
func assertDeadlineCloseTo(t *testing.T, got string, want time.Duration) {
	t.Helper()
	if got == "" {
		t.Fatalf("deadline_at vacío, se esperaba el instante calculado (+%s)", want)
	}
	instant, err := time.Parse("2006-01-02T15:04:05.000Z", got)
	if err != nil {
		t.Fatalf("deadline_at %q no es ISO-8601 UTC con milisegundos: %v", got, err)
	}
	delta := time.Until(instant) - want
	if delta < -2*time.Minute || delta > 2*time.Minute {
		t.Fatalf("deadline_at = %s, se esperaba ~ahora%+s (desvío %s)", got, want, delta)
	}
}

// TestTransitionTicketEnforcesTheConfiguredRole pins the RF6 criterion verbatim:
// "Dada una transición que exige rol `staff`+, cuando un `client` intenta
// ejecutarla por chat, entonces es rechazada con `FORBIDDEN` semántico" — and its
// generalisation, a caller whose role is below the transition's minimum.
func TestTransitionTicketEnforcesTheConfiguredRole(t *testing.T) {
	store := engineStore(roleWorkflow())
	// The ticket already sits in 'revisado', whose only way out requires admin.
	store.seed(dto.TicketRecord{ID: "ticket-a", ClientID: clientA, TicketType: "escalado", State: "revisado"})

	uc := transitionUC(t, store)

	t.Run("client: forbidden by the use case before the engine", func(t *testing.T) {
		_, err := uc.Execute(context.Background(), ticketCaller(clientA, auth.RoleClient), dto.TransitionCommand{
			TicketID: "ticket-a",
			State:    "escalado",
		})
		assertSemantic(t, err, usecase.CodeRoleNotAllowed, "tu rol no permite cambiar el estado de un ticket.")
	})

	t.Run("staff below an admin transition", func(t *testing.T) {
		_, err := uc.Execute(context.Background(), ticketCaller(staffID, auth.RoleStaff), dto.TransitionCommand{
			TicketID: "ticket-a",
			State:    "escalado",
		})
		assertSemantic(t, err, usecase.CodeRoleNotAllowed,
			"tu rol no permite pasar un ticket del estado 'revisado' al 'escalado': "+
				"el workflow del tipo 'escalado' reserva esa transición al rol admin o superior.")
	})

	t.Run("admin below an owner transition", func(t *testing.T) {
		store.seed(dto.TicketRecord{ID: "ticket-b", ClientID: clientA, TicketType: "escalado", State: "escalado"})
		_, err := uc.Execute(context.Background(), ticketCaller("dddddddd-dddd-4ddd-8ddd-0000000000dd", auth.RoleAdmin), dto.TransitionCommand{
			TicketID: "ticket-b",
			State:    "cerrado",
		})
		assertSemantic(t, err, usecase.CodeRoleNotAllowed,
			"tu rol no permite pasar un ticket del estado 'escalado' al 'cerrado': "+
				"el workflow del tipo 'escalado' reserva esa transición al rol owner o superior.")
	})

	// No refusal ever reached the guarded write.
	if len(store.transitions) != 0 {
		t.Fatalf("transiciones = %+v, ningún rechazo de rol debe escribir", store.transitions)
	}

	t.Run("admin runs the admin transition", func(t *testing.T) {
		result, err := uc.Execute(context.Background(), ticketCaller("dddddddd-dddd-4ddd-8ddd-0000000000dd", auth.RoleAdmin), dto.TransitionCommand{
			TicketID: "ticket-a",
			State:    "escalado",
		})
		if err != nil {
			t.Fatalf("Execute() error inesperado: %v", err)
		}
		if result.Ticket.State != "escalado" {
			t.Fatalf("state = %q, se esperaba 'escalado'", result.Ticket.State)
		}
	})

	t.Run("owner runs a staff transition", func(t *testing.T) {
		store.seed(dto.TicketRecord{ID: "ticket-c", ClientID: clientA, TicketType: "escalado", State: "nuevo"})
		result, err := uc.Execute(context.Background(), ticketCaller(staffID, auth.RoleOwner), dto.TransitionCommand{
			TicketID: "ticket-c",
			State:    "revisado",
		})
		if err != nil {
			t.Fatalf("Execute() error inesperado: %v", err)
		}
		if result.Ticket.State != "revisado" {
			t.Fatalf("state = %q, se esperaba 'revisado'", result.Ticket.State)
		}
	})
}

// TestTransitionTicketComputesTheDeadline pins RF6's deadline rule end to end:
// entering a state with deadline_hours sets deadline_at from the transition
// instant, and entering a state without one clears it (the stored instant always
// belongs to the current state).
func TestTransitionTicketComputesTheDeadline(t *testing.T) {
	store := engineStore(deadlineWorkflow())
	store.seed(dto.TicketRecord{ID: "ticket-a", ClientID: clientA, TicketType: "soporte"})

	uc := transitionUC(t, store)
	staff := ticketCaller(staffID, auth.RoleStaff)

	entered, err := uc.Execute(context.Background(), staff, dto.TransitionCommand{TicketID: "ticket-a", State: "en-proceso"})
	if err != nil {
		t.Fatalf("Execute() error inesperado: %v", err)
	}
	assertDeadlineCloseTo(t, entered.Ticket.DeadlineAt, 48*time.Hour)
	if entered.Ticket.ClosedAt != "" {
		t.Fatalf("closed_at = %q, 'en-proceso' no es un estado terminal", entered.Ticket.ClosedAt)
	}

	// The guarded write received the computed deadline, not just the readback.
	if len(store.transitions) != 1 || store.transitions[0].DeadlineAt != entered.Ticket.DeadlineAt {
		t.Fatalf("transiciones = %+v, se esperaba el deadline calculado en la escritura", store.transitions)
	}

	resolved, err := uc.Execute(context.Background(), staff, dto.TransitionCommand{TicketID: "ticket-a", State: "resuelto"})
	if err != nil {
		t.Fatalf("Execute() error inesperado: %v", err)
	}
	if resolved.Ticket.DeadlineAt != "" {
		t.Fatalf("deadline_at = %q, 'resuelto' no declara deadline y debe limpiarlo", resolved.Ticket.DeadlineAt)
	}
	if resolved.Ticket.ClosedAt == "" {
		t.Fatal("closed_at vacío: 'resuelto' es terminal y el instante del cierre debe quedar guardado")
	}
	if !store.transitions[1].Closes {
		t.Fatal("la escritura de una transición a un estado terminal no marcó el cierre")
	}
}

// TestTransitionTicketRoundRobin pins the auto-assignment rule of RF6: a
// transition that declares round-robin assigns the ticket to the next member of
// its area, records it in the audit and keeps rotating across tickets.
func TestTransitionTicketRoundRobin(t *testing.T) {
	store := engineStore(ventaWorkflow())
	store.rotation.members = []string{"cuenta-a", "cuenta-b"}
	store.seed(dto.TicketRecord{ID: "ticket-a", ClientID: clientA, TicketType: "venta", AreaID: ventasArea})
	store.seed(dto.TicketRecord{ID: "ticket-b", ClientID: clientB, TicketType: "venta", AreaID: ventasArea})

	uc := transitionUC(t, store)
	staff := ticketCaller(staffID, auth.RoleStaff)

	first, err := uc.Execute(context.Background(), staff, dto.TransitionCommand{TicketID: "ticket-a", State: "contactado"})
	if err != nil {
		t.Fatalf("Execute() error inesperado: %v", err)
	}
	second, err := uc.Execute(context.Background(), staff, dto.TransitionCommand{TicketID: "ticket-b", State: "contactado"})
	if err != nil {
		t.Fatalf("Execute() error inesperado: %v", err)
	}

	if first.Ticket.AssignedTo != "cuenta-a" || second.Ticket.AssignedTo != "cuenta-b" {
		t.Fatalf("asignados = %q / %q, se esperaba la rotación determinista cuenta-a, cuenta-b",
			first.Ticket.AssignedTo, second.Ticket.AssignedTo)
	}
	// The rotation is asked about the ticket's area, once per assigned transition.
	if len(store.rotation.areas) != 2 || store.rotation.areas[0] != ventasArea {
		t.Fatalf("áreas consultadas = %v, se esperaba el área del ticket", store.rotation.areas)
	}
	// RF5 audits the assignment change: the transition event names the assignee.
	payload := payloadOf(t, first.Events[len(first.Events)-1].Payload)
	if payload["assigned_to"] != "cuenta-a" {
		t.Fatalf("payload = %v, se esperaba el asignado en el evento de la transición", payload)
	}

	// A transition of the template that does NOT declare round-robin leaves the
	// assignment alone.
	third, err := uc.Execute(context.Background(), staff, dto.TransitionCommand{TicketID: "ticket-a", State: "presupuestado"})
	if err != nil {
		t.Fatalf("Execute() error inesperado: %v", err)
	}
	if third.Ticket.AssignedTo != "cuenta-a" {
		t.Fatalf("assigned_to = %q, una transición sin round-robin no debe reasignar", third.Ticket.AssignedTo)
	}
	if len(store.rotation.areas) != 2 {
		t.Fatalf("áreas consultadas = %v, la rotación no debe consultarse sin la regla", store.rotation.areas)
	}
}

// TestTransitionTicketRoundRobinInAnEmptyArea pins the documented no-op: an area
// with no members assigns nobody, and the transition still applies — the ticket
// stays in the area's queue.
func TestTransitionTicketRoundRobinInAnEmptyArea(t *testing.T) {
	store := engineStore(ventaWorkflow())
	store.seed(dto.TicketRecord{ID: "ticket-a", ClientID: clientA, TicketType: "venta", AreaID: ventasArea})

	result, err := transitionUC(t, store).Execute(context.Background(), ticketCaller(staffID, auth.RoleStaff), dto.TransitionCommand{
		TicketID: "ticket-a",
		State:    "contactado",
	})
	if err != nil {
		t.Fatalf("Execute() error inesperado: %v", err)
	}
	if result.Ticket.State != "contactado" {
		t.Fatalf("state = %q, se esperaba la transición aplicada", result.Ticket.State)
	}
	if result.Ticket.AssignedTo != "" {
		t.Fatalf("assigned_to = %q, un área sin staff no asigna a nadie", result.Ticket.AssignedTo)
	}
	payload := payloadOf(t, result.Events[len(result.Events)-1].Payload)
	if _, present := payload["assigned_to"]; present {
		t.Fatalf("payload = %v, sin asignación no debe declararse assigned_to", payload)
	}
}

// TestCreateTicketUsesTheConfiguredType pins the creation side of S1 ("estados/
// deadlines computan desde la config del tipo"): the ticket is born in the type's
// initial state, in its default area, with the deadline the initial state
// declares — and the opening event names the configured type.
func TestCreateTicketUsesTheConfiguredType(t *testing.T) {
	config := domain.WorkflowConfig{
		TypeName:      "instalacion",
		DefaultAreaID: ventasArea,
		InitialState:  "pedido",
		States: []domain.StateConfig{
			{Name: "pedido", DeadlineHours: hours(24)},
			{Name: "instalado"},
		},
		Transitions: []domain.TransitionConfig{
			{From: "pedido", To: "instalado", MinRole: domain.TransitionRoleStaff},
		},
	}
	store := engineStore(config)
	uc := createTicketUC(t, store, newFakeClientDirectory(clientA))

	result, err := uc.Execute(context.Background(), ticketCaller(clientA, auth.RoleClient), dto.CreateTicketCommand{
		Motive:     "quiero instalar el equipo",
		TicketType: "instalacion",
	})
	if err != nil {
		t.Fatalf("Execute() error inesperado: %v", err)
	}

	if result.Ticket.TicketType != "instalacion" || result.Ticket.State != "pedido" {
		t.Fatalf("ticket = %+v, se esperaba el tipo y el estado inicial configurados", result.Ticket)
	}
	if result.Ticket.AreaID != ventasArea {
		t.Fatalf("area_id = %q, se esperaba el área por defecto del tipo", result.Ticket.AreaID)
	}
	assertDeadlineCloseTo(t, result.Ticket.DeadlineAt, 24*time.Hour)

	payload := payloadOf(t, result.Events[0].Payload)
	if payload["ticket_type"] != "instalacion" || payload["state"] != "pedido" {
		t.Fatalf("payload = %v, se esperaba el tipo y el estado configurados", payload)
	}
}

// TestCreateTicketFailsClosedOnTheType pins the fail-closed direction of S1/S2: a
// type nobody configured, and a configured type whose stored workflow does not
// validate, both refuse the creation with a semantic error — never a fallback to
// the retired fixed pipeline.
func TestCreateTicketFailsClosedOnTheType(t *testing.T) {
	broken := domain.WorkflowConfig{
		TypeName:     "roto",
		InitialState: "no-existe",
		States:       []domain.StateConfig{{Name: "nuevo"}},
	}

	t.Run("tipo inexistente", func(t *testing.T) {
		store := engineStore(domain.ConsultaTemplate())
		uc := createTicketUC(t, store, newFakeClientDirectory(clientA))

		_, err := uc.Execute(context.Background(), ticketCaller(clientA, auth.RoleClient), dto.CreateTicketCommand{
			Motive:     "consulta",
			TicketType: "inexistente",
		})
		assertSemantic(t, err, usecase.CodeUnknownTicketType, "no existe el tipo de ticket 'inexistente'.")
		if len(store.created) != 0 {
			t.Fatalf("tickets creados = %+v, un tipo inexistente no crea nada", store.created)
		}
	})

	t.Run("workflow que no valida", func(t *testing.T) {
		store := engineStore(broken)
		uc := createTicketUC(t, store, newFakeClientDirectory(clientA))

		_, err := uc.Execute(context.Background(), ticketCaller(clientA, auth.RoleClient), dto.CreateTicketCommand{
			Motive:     "consulta",
			TicketType: "roto",
		})
		assertSemantic(t, err, usecase.CodeUnusableWorkflow,
			"el workflow del tipo 'roto' no se puede ejecutar: el estado inicial 'no-existe' no existe en el workflow.")
		if len(store.created) != 0 {
			t.Fatalf("tickets creados = %+v, un workflow inválido no crea nada", store.created)
		}
	})
}

// TestSeedPipelinesThroughTheUseCases runs the two seed templates of PRD §3.7
// end to end through the use cases: 'venta' (nuevo → contactado → presupuestado →
// cerrado, with the round-robin out of 'nuevo' into Ventas) and 'consulta'
// (nuevo → atendido → resuelto).
func TestSeedPipelinesThroughTheUseCases(t *testing.T) {
	t.Run("venta", func(t *testing.T) {
		store := engineStore(ventaWorkflow())
		store.rotation.members = []string{"vendedor-a", "vendedor-b"}
		create := createTicketUC(t, store, newFakeClientDirectory(clientA))
		transition := transitionUC(t, store)
		staff := ticketCaller(staffID, auth.RoleStaff)

		created, err := create.Execute(context.Background(), ticketCaller(clientA, auth.RoleClient), dto.CreateTicketCommand{
			Motive:     "quiero un presupuesto",
			TicketType: "venta",
		})
		if err != nil {
			t.Fatalf("CreateTicket() error inesperado: %v", err)
		}
		if created.Ticket.State != "nuevo" || created.Ticket.AreaID != ventasArea {
			t.Fatalf("ticket = %+v, se esperaba 'nuevo' en el área Ventas", created.Ticket)
		}
		id := created.Ticket.ID

		steps := []struct {
			state    dto.TicketState
			assignee string
			closed   bool
		}{
			{state: "contactado", assignee: "vendedor-a"},
			{state: "presupuestado", assignee: "vendedor-a"},
			{state: "cerrado", assignee: "vendedor-a", closed: true},
		}
		for _, step := range steps {
			result, err := transition.Execute(context.Background(), staff, dto.TransitionCommand{TicketID: id, State: step.state})
			if err != nil {
				t.Fatalf("TransitionTicket(%q) error inesperado: %v", step.state, err)
			}
			if result.Ticket.State != step.state {
				t.Fatalf("state = %q, se esperaba %q", result.Ticket.State, step.state)
			}
			if result.Ticket.AssignedTo != step.assignee {
				t.Fatalf("assigned_to = %q, se esperaba %q en el estado %q", result.Ticket.AssignedTo, step.assignee, step.state)
			}
			if (result.Ticket.ClosedAt != "") != step.closed {
				t.Fatalf("closed_at = %q en el estado %q, se esperaba terminal=%t", result.Ticket.ClosedAt, step.state, step.closed)
			}
			last := result.Events[len(result.Events)-1]
			if last.Actor != staffID || last.Kind != dto.TicketEventKindTransition {
				t.Fatalf("evento = %+v, se esperaba la transición auditada con el actor", last)
			}
		}

		// The closed conversation refuses a note, and the active list hides it.
		note := noteUC(t, store)
		_, err = note.Execute(context.Background(), ticketCaller(clientA, auth.RoleClient), dto.TicketNote{TicketID: id, Text: "sigo interesado"})
		assertSemantic(t, err, usecase.CodeTicketClosed,
			"el ticket está cerrado: una conversación cerrada no se reabre con una nota. Si el cliente vuelve, se abre un ticket nuevo.")

		list, err := usecase.NewListTickets(store)
		if err != nil {
			t.Fatalf("NewListTickets() error inesperado: %v", err)
		}
		active, err := list.Execute(context.Background(), staff, dto.TicketQuery{})
		if err != nil {
			t.Fatalf("ListTickets() error inesperado: %v", err)
		}
		if len(active.Tickets) != 0 {
			t.Fatalf("tickets = %+v, el cerrado no debe aparecer en la lista activa", active.Tickets)
		}
		all, err := list.Execute(context.Background(), staff, dto.TicketQuery{IncludeClosed: true})
		if err != nil {
			t.Fatalf("ListTickets(include_closed) error inesperado: %v", err)
		}
		if len(all.Tickets) != 1 || all.Tickets[0].Ticket.ID != id {
			t.Fatalf("tickets = %+v, se esperaba el cerrado con include_closed", all.Tickets)
		}
	})

	t.Run("consulta", func(t *testing.T) {
		store := engineStore(domain.ConsultaTemplate())
		create := createTicketUC(t, store, newFakeClientDirectory(clientA))
		transition := transitionUC(t, store)
		staff := ticketCaller(staffID, auth.RoleStaff)

		created, err := create.Execute(context.Background(), ticketCaller(clientA, auth.RoleClient), dto.CreateTicketCommand{
			Motive: "consulta por corte de pelo",
		})
		if err != nil {
			t.Fatalf("CreateTicket() error inesperado: %v", err)
		}
		if created.Ticket.TicketType != dto.ConsultaTicketTypeName || created.Ticket.State != "nuevo" {
			t.Fatalf("ticket = %+v, se esperaba la consulta en su estado inicial", created.Ticket)
		}
		if created.Ticket.AreaID != "" {
			t.Fatalf("area_id = %q, la consulta no declara área fija", created.Ticket.AreaID)
		}

		for _, state := range []dto.TicketState{"atendido", "resuelto"} {
			if _, err := transition.Execute(context.Background(), staff, dto.TransitionCommand{TicketID: created.Ticket.ID, State: state}); err != nil {
				t.Fatalf("TransitionTicket(%q) error inesperado: %v", state, err)
			}
		}

		// 'resuelto' is the terminal state of the template: the conversation is
		// over even though the F1 vocabulary named the ending 'cerrado'.
		stored, found, err := store.GetTicket(context.Background(), created.Ticket.ID)
		if err != nil || !found {
			t.Fatalf("GetTicket() = %t, %v", found, err)
		}
		if stored.ClosedAt == "" {
			t.Fatalf("closed_at vacío: 'resuelto' es terminal en la plantilla 'consulta'")
		}

		// The round-robin rule is absent from this template: no rotation was asked.
		if len(store.rotation.areas) != 0 {
			t.Fatalf("áreas consultadas = %v, la consulta no declara round-robin", store.rotation.areas)
		}
	})
}

// TestTransitionTicketEventSpellsThePair pins the audit shape of a transition:
// actor, from and to, with the configured state names of the ticket's type.
func TestTransitionTicketEventSpellsThePair(t *testing.T) {
	store := engineStore(ventaWorkflow())
	store.seed(dto.TicketRecord{ID: "ticket-a", ClientID: clientA, TicketType: "venta", State: "contactado", AreaID: ventasArea})

	result, err := transitionUC(t, store).Execute(context.Background(), ticketCaller(staffID, auth.RoleAdmin), dto.TransitionCommand{
		TicketID: "ticket-a",
		State:    "presupuestado",
	})
	if err != nil {
		t.Fatalf("Execute() error inesperado: %v", err)
	}

	last := result.Events[len(result.Events)-1]
	if last.Actor != staffID || last.Kind != dto.TicketEventKindTransition {
		t.Fatalf("evento = %+v, se esperaba la transición con el actor", last)
	}
	payload := payloadOf(t, last.Payload)
	if payload["from"] != "contactado" || payload["to"] != "presupuestado" {
		t.Fatalf("payload = %v, se esperaba el par from→to configurado", payload)
	}
	if strings.Contains(last.Payload, "%!") {
		t.Fatalf("payload = %q, el evento no debe llevar formato roto", last.Payload)
	}
}

// TestTicketsOfAnUnconfiguredTypeFailClosed pins the last fail-closed edge: a
// ticket whose type has no configuration at all (a row that outlived its workflow,
// or one written by a hand migration) cannot be moved and cannot receive notes.
// Falling back to the retired fixed vocabulary would be exactly the R3-001 orphan
// window this task closes.
func TestTicketsOfAnUnconfiguredTypeFailClosed(t *testing.T) {
	store := engineStore(domain.ConsultaTemplate())
	store.seed(dto.TicketRecord{ID: "ticket-a", ClientID: clientA, TicketType: "fantasma"})

	_, err := transitionUC(t, store).Execute(context.Background(), ticketCaller(staffID, auth.RoleStaff), dto.TransitionCommand{
		TicketID: "ticket-a",
		State:    "atendido",
	})
	assertSemantic(t, err, usecase.CodeUnknownTicketType, "no existe el tipo de ticket 'fantasma'.")

	_, err = noteUC(t, store).Execute(context.Background(), ticketCaller(clientA, auth.RoleClient), dto.TicketNote{
		TicketID: "ticket-a",
		Text:     "hola",
	})
	assertSemantic(t, err, usecase.CodeUnknownTicketType, "no existe el tipo de ticket 'fantasma'.")

	if len(store.transitions) != 0 || len(store.appended) != 0 {
		t.Fatalf("transiciones = %+v, notas = %+v; un tipo sin workflow no escribe nada", store.transitions, store.appended)
	}
}
