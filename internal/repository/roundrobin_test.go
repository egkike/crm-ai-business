package repository_test

import (
	"context"
	"testing"

	"github.com/egkike/crm-ai-business/internal/application/dto"
	"github.com/egkike/crm-ai-business/internal/db"
	"github.com/egkike/crm-ai-business/internal/domain"
	"github.com/egkike/crm-ai-business/internal/repository"
)

// This file pins the round-robin rotation of RF6 ("auto-asignación round-robin
// del área") as the adapter implements it: a STATELESS rotation derived from the
// tickets the area already holds, so it survives a restart and needs no table the
// schema does not declare.

// newRoundRobinOf wires the adapter over the fixture's handle.
func newRoundRobinOf(t *testing.T, fixture *ticketFixture) *repository.RoundRobinRepo {
	t.Helper()
	rotation, err := repository.NewRoundRobinRepo(fixture.handle)
	if err != nil {
		t.Fatalf("NewRoundRobinRepo() error inesperado: %v", err)
	}
	return rotation
}

// TestNewRoundRobinRepoRejectsNilHandle pins the fail-closed construction rule.
func TestNewRoundRobinRepoRejectsNilHandle(t *testing.T) {
	repo, err := repository.NewRoundRobinRepo(nil)
	if err == nil {
		t.Fatal("NewRoundRobinRepo(nil) debía fallar (fail-closed)")
	}
	if repo != nil {
		t.Fatalf("NewRoundRobinRepo(nil) devolvió un adaptador utilizable: %+v", repo)
	}
}

// seedStaff inserts an internal account of the given role.
func seedStaff(t *testing.T, fixture *ticketFixture, id, name string) {
	t.Helper()
	seedAccount(t, fixture.handle, account{
		id: id, phone: "+5491100000" + name, email: name + "@negocio.example",
		role: "staff", active: true, name: name,
	})
}

// assignTicketToArea inserts a ticket already assigned to accountID inside the
// area, with a fixed activity instant so the rotation order is deterministic.
func assignTicketToArea(t *testing.T, fixture *ticketFixture, id, accountID, activity string) {
	t.Helper()

	if err := fixture.repo.CreateTicket(context.Background(), dto.NewTicket{
		ID:         id,
		TicketType: db.TicketTypeVentaName,
		ClientID:   fixture.client,
		State:      "nuevo",
		AreaID:     db.AreaVentasID,
		OpeningEvent: dto.NewTicketEvent{
			Actor: fixture.client, Kind: dto.TicketEventKindCreated,
		},
	}); err != nil {
		t.Fatalf("CreateTicket(%s) error inesperado: %v", id, err)
	}

	// The rotation orders by the activity clock, so the fixture writes it
	// explicitly instead of racing two inserts inside the same millisecond.
	if _, err := fixture.handle.Exec(
		`UPDATE tickets SET assigned_to = ?, updated_at = ? WHERE id = ?`,
		accountID, activity, id,
	); err != nil {
		t.Fatalf("asignar el ticket %s: %v", id, err)
	}
}

// TestRoundRobinRotatesDeterministically pins the rule: never-assigned members
// first, then the member who has gone longest without a ticket of the area, with
// the account id as the deterministic tiebreaker.
func TestRoundRobinRotatesDeterministically(t *testing.T) {
	fixture := newTicketFixture(t)
	rotation := newRoundRobinOf(t, fixture)
	ctx := context.Background()

	first, second := uuid(50), uuid(51)
	seedStaff(t, fixture, first, "ana")
	seedStaff(t, fixture, second, "bruno")
	if err := newAreasOf(t, fixture.handle).AssignStaff(ctx, db.AreaVentasID, first); err != nil {
		t.Fatalf("AssignStaff(%s) error inesperado: %v", first, err)
	}
	if err := newAreasOf(t, fixture.handle).AssignStaff(ctx, db.AreaVentasID, second); err != nil {
		t.Fatalf("AssignStaff(%s) error inesperado: %v", second, err)
	}

	// Two members, nobody assigned yet: the tiebreaker (account id) decides, so
	// the answer is stable across restarts instead of being planner-dependent.
	assignee, err := rotation.NextAreaAssignee(ctx, db.AreaVentasID)
	if err != nil {
		t.Fatalf("NextAreaAssignee() error inesperado: %v", err)
	}
	if assignee != first {
		t.Fatalf("primer asignado = %q, se esperaba el desempate determinista %q", assignee, first)
	}

	// The ticket the rotation just handed out becomes the most recent assignment:
	// the next call goes to the other member.
	assignTicketToArea(t, fixture, uuid(800), first, "2026-01-10T10:00:00.000Z")
	assignee, err = rotation.NextAreaAssignee(ctx, db.AreaVentasID)
	if err != nil {
		t.Fatalf("NextAreaAssignee() error inesperado: %v", err)
	}
	if assignee != second {
		t.Fatalf("segundo asignado = %q, se esperaba %q (rotación)", assignee, second)
	}

	// And back: the older assignment is the next in line.
	assignTicketToArea(t, fixture, uuid(801), second, "2026-01-11T10:00:00.000Z")
	assignee, err = rotation.NextAreaAssignee(ctx, db.AreaVentasID)
	if err != nil {
		t.Fatalf("NextAreaAssignee() error inesperado: %v", err)
	}
	if assignee != first {
		t.Fatalf("tercer asignado = %q, se esperaba %q (volvió el turno)", assignee, first)
	}

	// A member without any ticket of the area goes ahead of everyone who already
	// has one, whatever their ids are.
	third := uuid(52)
	seedStaff(t, fixture, third, "carla")
	if err := newAreasOf(t, fixture.handle).AssignStaff(ctx, db.AreaVentasID, third); err != nil {
		t.Fatalf("AssignStaff(%s) error inesperado: %v", third, err)
	}
	assignee, err = rotation.NextAreaAssignee(ctx, db.AreaVentasID)
	if err != nil {
		t.Fatalf("NextAreaAssignee() error inesperado: %v", err)
	}
	if assignee != third {
		t.Fatalf("asignado = %q, se esperaba el miembro que nunca recibió un ticket (%q)", assignee, third)
	}
}

// TestRoundRobinEmptyAreaAssignsNobody pins the documented no-op: an area with no
// members (and a ticket with no area at all) answers "" instead of failing — the
// ticket stays in the queue.
func TestRoundRobinEmptyAreaAssignsNobody(t *testing.T) {
	fixture := newTicketFixture(t)
	rotation := newRoundRobinOf(t, fixture)
	ctx := context.Background()

	assignee, err := rotation.NextAreaAssignee(ctx, db.AreaVentasID)
	if err != nil {
		t.Fatalf("NextAreaAssignee(área sin staff) error inesperado: %v", err)
	}
	if assignee != "" {
		t.Fatalf("asignado = %q, un área sin staff no asigna a nadie", assignee)
	}

	assignee, err = rotation.NextAreaAssignee(ctx, "")
	if err != nil {
		t.Fatalf("NextAreaAssignee(\"\") error inesperado: %v", err)
	}
	if assignee != "" {
		t.Fatalf("asignado = %q, un ticket sin área no tiene rotación", assignee)
	}
}

// TestRoundRobinIgnoresOtherAreas pins the scope of the rotation: a member's load
// in another area does not make them "recently assigned" here.
func TestRoundRobinIgnoresOtherAreas(t *testing.T) {
	fixture := newTicketFixture(t)
	rotation := newRoundRobinOf(t, fixture)
	ctx := context.Background()

	areas := newAreasOf(t, fixture.handle)
	soporte := uuid(900)
	if err := areas.CreateArea(ctx, domain.Area{ID: soporte, Name: "Soporte"}); err != nil {
		t.Fatalf("CreateArea() error inesperado: %v", err)
	}

	first, second := uuid(50), uuid(51)
	seedStaff(t, fixture, first, "ana")
	seedStaff(t, fixture, second, "bruno")
	for _, areaID := range []string{db.AreaVentasID, soporte} {
		if err := areas.AssignStaff(ctx, areaID, first); err != nil {
			t.Fatalf("AssignStaff(%s) error inesperado: %v", areaID, err)
		}
	}
	if err := areas.AssignStaff(ctx, db.AreaVentasID, second); err != nil {
		t.Fatalf("AssignStaff(%s) error inesperado: %v", second, err)
	}

	// second holds the oldest assignment in Ventas; first has just been handed
	// the newest ticket of Soporte. If the rotation looked at the business as a
	// whole it would skip first and answer second; scoped to the area, first is
	// still never-assigned in Ventas and goes ahead.
	assignTicketToArea(t, fixture, uuid(802), second, "2026-01-02T10:00:00.000Z")
	assignTicketToArea(t, fixture, uuid(803), first, "2026-01-12T10:00:00.000Z")
	if _, err := fixture.handle.Exec(`UPDATE tickets SET area_id = ? WHERE id = ?`, soporte, uuid(803)); err != nil {
		t.Fatalf("mover el ticket de área: %v", err)
	}

	assignee, err := rotation.NextAreaAssignee(ctx, db.AreaVentasID)
	if err != nil {
		t.Fatalf("NextAreaAssignee() error inesperado: %v", err)
	}
	if assignee != first {
		t.Fatalf("asignado = %q, se esperaba %q (el trabajo en otra área no cuenta)", assignee, first)
	}
}
