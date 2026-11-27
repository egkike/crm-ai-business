package repository

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"

	"github.com/egkike/crm-ai-business/internal/domain"
)

// This file is the persistence adapter of the configurable workflow (RF6): the
// three config tables (ticket_types, ticket_states, ticket_transitions) read and
// written as the single aggregate the domain declares.
//
// It decides nothing. The config it stores is the config it is given — the
// semantic validation (fail-closed, Spanish rejection) is the use case's — and
// its one real responsibility is the aggregate boundary: a config is read as a
// whole and written as a whole inside one transaction, so a mid-save failure
// leaves the previous config intact instead of a prefix of the new one (RF6:
// "se rechaza la config completa y nada queda parcialmente escrito").

// The workflow statements. Each one is a constant, parameterized with '?', and no
// caller value ever reaches the SQL text.
const (
	// selectTicketTypeForConfig reads the type row: its internal id (the key of
	// its states and transitions) and the two declarations that live on the type
	// instead of on a state row — the default area (RF7) and the initial state
	// (RF5: "el estado inicial del tipo").
	selectTicketTypeForConfig = `
SELECT id, name, default_area_id, initial_state FROM ticket_types WHERE name = ?`

	// updateTicketTypeConfig writes the type-level half of a config. It runs
	// inside the save transaction, so a rejected area (FK) or a rejected state
	// (CHECK) rolls this back with everything else.
	updateTicketTypeConfig = `
UPDATE ticket_types
SET default_area_id = ?, initial_state = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
WHERE id = ?`

	// deleteTicketStates and deleteTicketTransitions empty the type's workflow
	// before the new one is written. DELETE + INSERT (instead of per-row upserts)
	// keeps SaveConfig a single statement shape: a save is a replacement, and a
	// state the new config dropped has to disappear.
	deleteTicketStates      = `DELETE FROM ticket_states WHERE ticket_type_id = ?`
	deleteTicketTransitions = `DELETE FROM ticket_transitions WHERE ticket_type_id = ?`

	insertTicketState = `
INSERT INTO ticket_states (id, ticket_type_id, name, position, deadline_hours, on_enter_alert, on_exit_alert)
VALUES (?, ?, ?, ?, ?, ?, ?)`

	insertTicketTransition = `
INSERT INTO ticket_transitions (id, ticket_type_id, from_state, to_state, position, min_role, round_robin)
VALUES (?, ?, ?, ?, ?, ?, ?)`

	// selectTicketStates and selectTicketTransitions order by position: the
	// declaration order the Owner configured is the order the engine and the panel
	// render. The name is the tiebreaker so a hand-edited row with a repeated
	// position still reads deterministically.
	selectTicketStates = `
SELECT name, deadline_hours, on_enter_alert, on_exit_alert
FROM ticket_states
WHERE ticket_type_id = ?
ORDER BY position, name`

	selectTicketTransitions = `
SELECT from_state, to_state, min_role, round_robin
FROM ticket_transitions
WHERE ticket_type_id = ?
ORDER BY position, from_state, to_state`
)

// WorkflowConfigRepo is the SQLite adapter of the workflow configuration
// (satisfied by domain.WorkflowConfigStore).
type WorkflowConfigRepo struct {
	db *sql.DB
}

// NewWorkflowConfigRepo wires the adapter to its database handle. A nil handle is
// a fail-closed configuration error: an adapter without a database can only fail.
func NewWorkflowConfigRepo(handle *sql.DB) (*WorkflowConfigRepo, error) {
	if handle == nil {
		return nil, errors.New("repositorio del workflow: se requiere una conexión a la base")
	}
	return &WorkflowConfigRepo{db: handle}, nil
}

// LoadConfig assembles the workflow of the ticket type named typeName.
//
// The three reads run inside one deferred transaction, so the config is a
// snapshot: without it, a SaveConfig committing between the states read and the
// transitions read would produce a config assembled from two different versions —
// exactly the mixed state the aggregate boundary exists to prevent.
func (r *WorkflowConfigRepo) LoadConfig(ctx context.Context, typeName string) (domain.WorkflowConfig, bool, error) {
	if typeName == "" {
		return domain.WorkflowConfig{}, false, errors.New("repositorio del workflow: se requiere el nombre del tipo")
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.WorkflowConfig{}, false, fmt.Errorf("repositorio del workflow: abrir la lectura: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var (
		typeID      string
		name        string
		defaultArea sql.NullString
		initial     sql.NullString
	)
	if err := tx.QueryRowContext(ctx, selectTicketTypeForConfig, typeName).
		Scan(&typeID, &name, &defaultArea, &initial); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.WorkflowConfig{}, false, nil
		}
		return domain.WorkflowConfig{}, false, fmt.Errorf("repositorio del workflow: leer el tipo: %w", err)
	}

	config := domain.WorkflowConfig{
		TypeName:      name,
		DefaultAreaID: defaultArea.String,
		InitialState:  initial.String,
	}

	if config.States, err = loadWorkflowStates(ctx, tx, typeID); err != nil {
		return domain.WorkflowConfig{}, false, err
	}
	if config.Transitions, err = loadWorkflowTransitions(ctx, tx, typeID); err != nil {
		return domain.WorkflowConfig{}, false, err
	}

	// A read transaction is closed by committing (there is nothing to write) or by
	// the deferred rollback; both release the snapshot, and neither changes a row.
	if err := tx.Commit(); err != nil {
		return domain.WorkflowConfig{}, false, fmt.Errorf("repositorio del workflow: cerrar la lectura: %w", err)
	}
	return config, true, nil
}

// loadWorkflowStates reads the states of one type in declaration order.
func loadWorkflowStates(ctx context.Context, tx *sql.Tx, typeID string) ([]domain.StateConfig, error) {
	rows, err := tx.QueryContext(ctx, selectTicketStates, typeID)
	if err != nil {
		return nil, fmt.Errorf("repositorio del workflow: leer los estados: %w", err)
	}
	defer func() { _ = rows.Close() }()

	states := make([]domain.StateConfig, 0, 8)
	for rows.Next() {
		var (
			state    domain.StateConfig
			deadline sql.NullInt64
			onEnter  int
			onExit   int
		)
		if err := rows.Scan(&state.Name, &deadline, &onEnter, &onExit); err != nil {
			return nil, fmt.Errorf("repositorio del workflow: leer un estado: %w", err)
		}
		if deadline.Valid {
			// The int is allocated per row: a shared pointer would make every state
			// with a deadline point at the last value read.
			hours := int(deadline.Int64)
			state.DeadlineHours = &hours
		}
		state.OnEnterAlert = onEnter == 1
		state.OnExitAlert = onExit == 1
		states = append(states, state)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("repositorio del workflow: leer los estados: %w", err)
	}
	return states, nil
}

// loadWorkflowTransitions reads the transitions of one type in declaration order.
func loadWorkflowTransitions(ctx context.Context, tx *sql.Tx, typeID string) ([]domain.TransitionConfig, error) {
	rows, err := tx.QueryContext(ctx, selectTicketTransitions, typeID)
	if err != nil {
		return nil, fmt.Errorf("repositorio del workflow: leer las transiciones: %w", err)
	}
	defer func() { _ = rows.Close() }()

	transitions := make([]domain.TransitionConfig, 0, 8)
	for rows.Next() {
		var (
			transition domain.TransitionConfig
			minRole    string
			roundRobin int
		)
		if err := rows.Scan(&transition.From, &transition.To, &minRole, &roundRobin); err != nil {
			return nil, fmt.Errorf("repositorio del workflow: leer una transición: %w", err)
		}
		transition.MinRole = domain.TransitionRole(minRole)
		transition.RoundRobin = roundRobin == 1
		transitions = append(transitions, transition)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("repositorio del workflow: leer las transiciones: %w", err)
	}
	return transitions, nil
}

// SaveConfig replaces the whole workflow of an existing ticket type inside one
// transaction: the type's area and initial state, every state, every transition.
//
// All of it or none of it. The order is the one that fails earliest: the type row
// (a default area that does not exist is an FK violation) before the rows it owns,
// then the delete/insert pair. Any failure — at the first statement, in the middle
// of the states or on the last transition — rolls the transaction back, so the
// caller observes the previous config, never a prefix of the new one.
//
// It reports found=false when no type carries cfg.TypeName; nothing is written in
// that case, because creating a type is a separate decision (RF6's admin tools own
// it), never a side effect of saving a config.
func (r *WorkflowConfigRepo) SaveConfig(ctx context.Context, cfg domain.WorkflowConfig) (bool, error) {
	if cfg.TypeName == "" {
		return false, errors.New("repositorio del workflow: no se puede guardar la config de un tipo sin nombre")
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("repositorio del workflow: abrir el guardado: %w", err)
	}
	// The deferred rollback is the atomicity of the whole config: whatever was
	// written before the failure is discarded here.
	defer func() { _ = tx.Rollback() }()

	var typeID string
	if err := tx.QueryRowContext(ctx, selectTicketTypeIDByName, cfg.TypeName).Scan(&typeID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("repositorio del workflow: resolver el tipo: %w", err)
	}

	if _, err := tx.ExecContext(ctx, updateTicketTypeConfig,
		nullText(cfg.DefaultAreaID), nullText(cfg.InitialState), typeID,
	); err != nil {
		return false, fmt.Errorf("repositorio del workflow: actualizar el tipo: %w", err)
	}

	if _, err := tx.ExecContext(ctx, deleteTicketStates, typeID); err != nil {
		return false, fmt.Errorf("repositorio del workflow: limpiar los estados: %w", err)
	}
	if _, err := tx.ExecContext(ctx, deleteTicketTransitions, typeID); err != nil {
		return false, fmt.Errorf("repositorio del workflow: limpiar las transiciones: %w", err)
	}

	for position, state := range cfg.States {
		stateID, err := newConfigRowID()
		if err != nil {
			return false, err
		}
		if _, err := tx.ExecContext(ctx, insertTicketState,
			stateID,
			typeID,
			state.Name,
			position,
			optionalInt(state.DeadlineHours),
			boolFlag(state.OnEnterAlert),
			boolFlag(state.OnExitAlert),
		); err != nil {
			return false, fmt.Errorf("repositorio del workflow: insertar el estado %q: %w", state.Name, err)
		}
	}

	for position, transition := range cfg.Transitions {
		transitionID, err := newConfigRowID()
		if err != nil {
			return false, err
		}
		if _, err := tx.ExecContext(ctx, insertTicketTransition,
			transitionID,
			typeID,
			transition.From,
			transition.To,
			position,
			string(transition.MinRole),
			boolFlag(transition.RoundRobin),
		); err != nil {
			return false, fmt.Errorf("repositorio del workflow: insertar la transición %q→%q: %w",
				transition.From, transition.To, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("repositorio del workflow: confirmar el guardado: %w", err)
	}
	return true, nil
}

// selectTicketTypeIDByName resolves the internal id of the type a config belongs
// to. It is deliberately its own statement: the save needs the id, not the whole
// row, and a single-column read keeps the transaction's first statement cheap.
const selectTicketTypeIDByName = `SELECT id FROM ticket_types WHERE name = ?`

// newConfigRowID generates the internal id of one configuration row (a state or a
// transition).
//
// These rows are the only entities of the product whose id never crosses a layer
// boundary: no tool, no panel form and no DTO names a ticket_states or
// ticket_transitions row — configurations are addressed by ticket type and by
// state NAME. The id therefore exists to be a primary key and nothing else — and
// it is deliberately NOT stable across saves: a config save replaces the rows of
// the type, so an id a caller cached is a dangling reference after the next edit.
// That is why it is generated here instead of by the application layer (the ticket
// creation, by contrast, takes its id from the use case). The layout is RFC 4122
// version 4, the same one the seeded rows and usecase.newUUID produce.
func newConfigRowID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("repositorio del workflow: no se pudo generar el id de la fila: %w", err)
	}
	raw[6] = (raw[6] & 0x0f) | 0x40 // version 4
	raw[8] = (raw[8] & 0x3f) | 0x80 // RFC 4122 variant

	return fmt.Sprintf("%x-%x-%x-%x-%x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16]), nil
}

// nullText maps the empty string of a carrier to SQL NULL. The schema's two
// nullable declarations mean "not configured" (no fixed area, no initial state)
// and ” would be a value with a different meaning.
func nullText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// optionalInt maps an optional deadline to its column: nil is NULL (no deadline),
// never 0 (rejected by the CHECK and read as "expires immediately").
func optionalInt(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

// boolFlag maps a declarative rule flag to the integer the DDL stores.
func boolFlag(flag bool) any {
	if flag {
		return 1
	}
	return 0
}
