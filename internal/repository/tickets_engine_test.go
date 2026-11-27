package repository_test

import (
	"context"
	"testing"

	"github.com/egkike/crm-ai-business/internal/application/dto"
	"github.com/egkike/crm-ai-business/internal/db"
	"github.com/egkike/crm-ai-business/internal/domain"
)

// This file pins the F2 T3 half of the persistence adapter that replaced the F1
// fixed vocabulary (rider R3-001): the ticket read carries its type's NAME, the
// guarded transition writes the three columns computed from the destination
// state, and the "is this conversation over?" question is answered from the
// CONFIGURED transitions instead of from a hardcoded state.

// TestCreateTicketResolvesTheTypeByName pins the type resolution of the creation:
// the use case addresses a type by name (the engine's key) and the adapter
// resolves the row, so the ticket can be born in the 'venta' pipeline with its
// area and no fixed-UUID assumption.
func TestCreateTicketResolvesTheTypeByName(t *testing.T) {
	fixture := newTicketFixture(t)
	ctx := context.Background()
	id := uuid(750)

	err := fixture.repo.CreateTicket(ctx, dto.NewTicket{
		ID:         id,
		TicketType: db.TicketTypeVentaName,
		ClientID:   fixture.client,
		State:      "nuevo",
		AreaID:     db.AreaVentasID,
		DeadlineAt: "2026-01-03T10:00:00.000Z",
		OpeningEvent: dto.NewTicketEvent{
			Actor: fixture.client, Kind: dto.TicketEventKindCreated,
		},
	})
	if err != nil {
		t.Fatalf("CreateTicket() error inesperado: %v", err)
	}

	record, found, err := fixture.repo.GetTicket(ctx, id)
	if err != nil || !found {
		t.Fatalf("GetTicket() = %t, %v", found, err)
	}
	if record.TicketType != db.TicketTypeVentaName {
		t.Fatalf("ticket_type = %q, se esperaba %q", record.TicketType, db.TicketTypeVentaName)
	}
	if record.TicketTypeID != db.TicketTypeVentaID {
		t.Fatalf("ticket_type_id = %q, se esperaba %q", record.TicketTypeID, db.TicketTypeVentaID)
	}
	if record.AreaID != db.AreaVentasID {
		t.Fatalf("area_id = %q, se esperaba %q", record.AreaID, db.AreaVentasID)
	}
	if record.DeadlineAt != "2026-01-03T10:00:00.000Z" {
		t.Fatalf("deadline_at = %q, se esperaba el instante calculado", record.DeadlineAt)
	}
	if record.ClosedAt != "" {
		t.Fatalf("closed_at = %q, un ticket nace abierto", record.ClosedAt)
	}
}

// TestTransitionWritesTheComputedColumns pins the single transition write: the
// destination state's deadline, the closing instant when the destination is
// terminal, and the round-robin assignee all land in the SAME guarded statement
// as the state — a partial write would leave the scheduler reading a deadline
// that belongs to a state the ticket already left.
func TestTransitionWritesTheComputedColumns(t *testing.T) {
	fixture := newTicketFixture(t)
	ctx := context.Background()
	id := uuid(751)
	assignee := uuid(50)
	seedStaff(t, fixture, assignee, "ana")
	fixture.createTicket(t, openTicket{id: id, clientID: fixture.client})

	applied, err := fixture.repo.TransitionTicket(ctx, dto.TicketTransition{
		TicketID:   id,
		From:       stateNew,
		To:         stateAttended,
		DeadlineAt: "2026-01-05T12:00:00.000Z",
		AssignTo:   assignee,
		Event:      dto.NewTicketEvent{Actor: assignee, Kind: dto.TicketEventKindTransition},
	})
	if err != nil || !applied {
		t.Fatalf("TransitionTicket() = %t, %v", applied, err)
	}

	record, found, err := fixture.repo.GetTicket(ctx, id)
	if err != nil || !found {
		t.Fatalf("GetTicket() = %t, %v", found, err)
	}
	if record.DeadlineAt != "2026-01-05T12:00:00.000Z" {
		t.Fatalf("deadline_at = %q, se esperaba el deadline del estado destino", record.DeadlineAt)
	}
	if record.AssignedTo != assignee {
		t.Fatalf("assigned_to = %q, se esperaba el asignado del round-robin", record.AssignedTo)
	}
	if record.ClosedAt != "" {
		t.Fatalf("closed_at = %q, 'atendido' no es terminal", record.ClosedAt)
	}

	// Entering the terminal state stamps closed_at and CLEARS the deadline.
	applied, err = fixture.repo.TransitionTicket(ctx, dto.TicketTransition{
		TicketID: id,
		From:     stateAttended,
		To:       stateResolved,
		Closes:   true,
		Event:    dto.NewTicketEvent{Actor: assignee, Kind: dto.TicketEventKindTransition},
	})
	if err != nil || !applied {
		t.Fatalf("TransitionTicket() = %t, %v", applied, err)
	}

	closed, found, err := fixture.repo.GetTicket(ctx, id)
	if err != nil || !found {
		t.Fatalf("GetTicket() = %t, %v", found, err)
	}
	if closed.ClosedAt == "" {
		t.Fatal("closed_at vacío: el estado destino es terminal")
	}
	if closed.DeadlineAt != "" {
		t.Fatalf("deadline_at = %q, un estado sin deadline debe limpiar el anterior", closed.DeadlineAt)
	}
	if closed.AssignedTo != assignee {
		t.Fatalf("assigned_to = %q, una transición sin asignación no debe reasignar", closed.AssignedTo)
	}
}

// TestNoteGuardFollowsTheConfiguredTerminalState pins the death of the fixed
// "closed" value (rider R3-001): the guard of a note accepts activity on a state
// with an outgoing transition and refuses it on a state without one — derived at
// write time from the CONFIGURED transitions of the ticket's type.
func TestNoteGuardFollowsTheConfiguredTerminalState(t *testing.T) {
	fixture := newTicketFixture(t)
	ctx := context.Background()

	cases := []struct {
		name    string
		state   dto.TicketState
		applied bool
	}{
		{name: "nuevo (abierto)", state: stateNew, applied: true},
		{name: "atendido (abierto)", state: stateAttended, applied: true},
		{name: "resuelto (terminal)", state: stateResolved, applied: false},
		// A state no configured transition starts from is terminal too: the
		// product fails closed instead of guessing that it is open.
		{name: "estado no declarado", state: "archivado", applied: false},
	}

	for index, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := uuid(760 + index)
			fixture.createTicket(t, openTicket{id: id, clientID: fixture.client, state: tc.state})

			record, applied, err := fixture.repo.AddTicketNote(ctx, dto.TicketNoteAppend{
				TicketID: id,
				Event:    dto.NewTicketEvent{Actor: fixture.client, Kind: dto.TicketEventKindNote},
			})
			if err != nil {
				t.Fatalf("AddTicketNote() error inesperado: %v", err)
			}
			if applied != tc.applied {
				t.Fatalf("AddTicketNote(%q) applied = %t, se esperaba %t", tc.state, applied, tc.applied)
			}

			events, err := fixture.repo.ListTicketEvents(ctx, id)
			if err != nil {
				t.Fatalf("ListTicketEvents() error inesperado: %v", err)
			}
			if tc.applied {
				if record.UpdatedAt == "" {
					t.Fatal("la nota aceptada no devolvió el ticket")
				}
				if len(events) != 2 {
					t.Fatalf("eventos = %d, se esperaba la creación y la nota", len(events))
				}
				return
			}
			if len(events) != 1 {
				t.Fatalf("eventos = %d, la nota rechazada no debe escribir", len(events))
			}
		})
	}
}

// TestListFilterFollowsTheConfiguredTerminalState pins the list half of the same
// rule: the active views exclude the states the configuration declares terminal,
// per ticket type, without naming a state in SQL.
func TestListFilterFollowsTheConfiguredTerminalState(t *testing.T) {
	fixture := newTicketFixture(t)
	ctx := context.Background()

	openNew := uuid(770)
	openAttended := uuid(771)
	terminal := uuid(772)
	fixture.createTicket(t, openTicket{id: openNew, clientID: fixture.client})
	fixture.createTicket(t, openTicket{id: openAttended, clientID: fixture.client, state: stateAttended})
	fixture.createTicket(t, openTicket{id: terminal, clientID: fixture.client, state: stateResolved})

	active, err := fixture.repo.ListTickets(ctx, dto.TicketQuery{Limit: 10})
	if err != nil {
		t.Fatalf("ListTickets() error inesperado: %v", err)
	}
	if len(active) != 2 || hasTicketID(active, terminal) {
		t.Fatalf("tickets activos = %v, se esperaban los dos abiertos", ticketIDs(active))
	}

	all, err := fixture.repo.ListTickets(ctx, dto.TicketQuery{IncludeClosed: true, Limit: 10})
	if err != nil {
		t.Fatalf("ListTickets(include_closed) error inesperado: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("tickets = %v, se esperaban los tres con include_closed", ticketIDs(all))
	}

	// The rule is PER TYPE: a 'venta' ticket in 'cerrado' is terminal for the
	// 'venta' config, and a 'consulta' ticket in a state the venta config calls
	// terminal is not affected by it.
	ventaID := uuid(773)
	if err := fixture.repo.CreateTicket(ctx, dto.NewTicket{
		ID: ventaID, TicketType: db.TicketTypeVentaName, ClientID: fixture.client, State: "cerrado",
		OpeningEvent: dto.NewTicketEvent{Actor: fixture.client, Kind: dto.TicketEventKindCreated},
	}); err != nil {
		t.Fatalf("CreateTicket(venta) error inesperado: %v", err)
	}

	active, err = fixture.repo.ListTickets(ctx, dto.TicketQuery{Limit: 10})
	if err != nil {
		t.Fatalf("ListTickets() error inesperado: %v", err)
	}
	if hasTicketID(active, ventaID) {
		t.Fatalf("tickets activos = %v, 'cerrado' es terminal en la config de 'venta'", ticketIDs(active))
	}
}

// TestSQLTerminalityMatchesTheDomainRule is the cross-check between the two
// expressions of one rule: the adapter derives "open" in SQL (an EXISTS over the
// configured transitions) and the engine derives it in Go
// (domain.WorkflowConfig.IsTerminal). A drift between them would let the engine
// accept a note the guard refuses and vice versa.
func TestSQLTerminalityMatchesTheDomainRule(t *testing.T) {
	fixture := newTicketFixture(t)

	for _, config := range []domain.WorkflowConfig{domain.ConsultaTemplate(), domain.VentaTemplate()} {
		for _, state := range config.States {
			// The SQL form of "has an outgoing transition" for this state.
			open := queryScalar(t, fixture.handle, `
SELECT COUNT(*) FROM ticket_transitions tr
JOIN ticket_types tt ON tt.id = tr.ticket_type_id
WHERE tt.name = ? AND tr.from_state = ?`, config.TypeName, state.Name) != "0"

			if want := !config.IsTerminal(state.Name); open != want {
				t.Fatalf("%s/%s: SQL dice abierto=%t, el dominio dice abierto=%t", config.TypeName, state.Name, open, want)
			}
		}
	}
}
