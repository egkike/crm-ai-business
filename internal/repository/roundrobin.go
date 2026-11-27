package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// This file is the persistence adapter of the round-robin auto-assignment of RF6
// ("auto-asignación round-robin del área"): the one read that answers "which
// member of this area takes the next ticket?".
//
// WHY THERE IS NO ROTATION STATE. The rotation is derived from the tickets
// themselves: the member who has gone longest without receiving a ticket of the
// area is next, and a member who never received one goes first. Nothing is
// stored — no cursor row, no counter, no in-memory ring — because the product's
// rule is that scheduler/engine state lives in SQLite (PRD: the scheduler keeps
// its state in the database so a restart resumes instead of forgetting). A
// derived rotation satisfies that rule with the data the database already holds:
// it survives restarts and process replicas for free, it cannot drift from the
// assignments it is supposed to follow, and it needs no table the PRD does not
// declare.
//
// The honest limit, stated once here instead of being discovered later: the
// signal for "when was this member last assigned" is the ticket's activity clock
// (tickets.updated_at, the same column the F2 scheduler reads), not a dedicated
// assignment timestamp — the schema declares no such column. A member whose last
// ticket was worked on recently therefore reads as "recently assigned", which is
// the behaviour a queue wants anyway (the person who is actively handling the
// area's work is not the next one to dump a new ticket on).

// nextAreaAssignee is the rotation query: members of the area ordered by how long
// they have gone without a ticket of that area, never-assigned first.
//
//   - The LEFT JOIN keeps members with no ticket at all: their last_assigned is
//     NULL, and `(last_assigned IS NULL) DESC` sorts them ahead of everyone.
//   - The tiebreaker is account_id, so two members with the same instant (or two
//     never-assigned members) always resolve in the same order: the rotation is
//     deterministic, not "whatever the query planner returns".
//   - The sub-select is scoped to the area (`t.area_id = ?`), so a member's load
//     in another area does not affect this area's rotation.
const nextAreaAssignee = `
SELECT aa.account_id
FROM areas_accounts aa
LEFT JOIN (
    SELECT t.assigned_to AS account_id, MAX(t.updated_at) AS last_assigned
    FROM tickets t
    WHERE t.area_id = ? AND t.assigned_to IS NOT NULL
    GROUP BY t.assigned_to
) rotation ON rotation.account_id = aa.account_id
WHERE aa.area_id = ?
ORDER BY (rotation.last_assigned IS NULL) DESC, rotation.last_assigned ASC, aa.account_id ASC
LIMIT 1`

// RoundRobinRepo is the SQLite adapter of the area rotation (satisfied by
// usecase.AreaRotator).
type RoundRobinRepo struct {
	db *sql.DB
}

// NewRoundRobinRepo wires the adapter to its database handle. A nil handle is a
// fail-closed configuration error.
func NewRoundRobinRepo(handle *sql.DB) (*RoundRobinRepo, error) {
	if handle == nil {
		return nil, errors.New("repositorio del round-robin: se requiere una conexión a la base")
	}
	return &RoundRobinRepo{db: handle}, nil
}

// NextAreaAssignee returns the account the next ticket of the area belongs to, or
// "" when the area has no members.
//
// An empty answer is NOT an error and not a failure: RF6 lets a transition run its
// round-robin rule against an area nobody is assigned to, in which case the ticket
// simply stays in the area's queue (assigned_to NULL) for anyone to take. The
// caller writes the assignment only when this returns a value.
func (r *RoundRobinRepo) NextAreaAssignee(ctx context.Context, areaID string) (string, error) {
	if areaID == "" {
		return "", nil
	}

	var accountID string
	if err := r.db.QueryRowContext(ctx, nextAreaAssignee, areaID, areaID).Scan(&accountID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("repositorio del round-robin: resolver el asignado del área: %w", err)
	}
	return accountID, nil
}
