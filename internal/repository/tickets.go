package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/egkike/crm-ai-business/internal/application/dto"
)

// This file is the persistence adapter of RF5 (tickets con historial auditable):
// the ticket rows and the append-only ticket_events. It decides nothing — it
// stores the validated rows it is given, applies the guarded state change and
// reports what it found.

// The ticket statements. Each one is a constant, parameterized with '?', and no
// caller value ever reaches the SQL text.
const (
	insertTicket = `
INSERT INTO tickets (id, ticket_type_id, client_id, state) VALUES (?, ?, ?, ?)`

	// ticketColumns is the projection every ticket read shares, so the carrier is
	// filled by one scan helper and cannot drift between queries.
	ticketColumns = `id, ticket_type_id, client_id, state, assigned_to, created_at, updated_at`

	selectTicketByID = `SELECT ` + ticketColumns + ` FROM tickets WHERE id = ?`

	// selectTickets is the single list statement of both views. Its filters are
	// BOUND, not built: an empty client filter means "every client" and
	// includeClosed=false means "open conversations only", so one statement serves
	// the client view and the internal queue without a second dialect of SQL.
	//
	// The closed state travels as a parameter instead of a literal for the same
	// reason the initial state of a ticket does: F1's state vocabulary is Go
	// (dto.TicketState), and F2 replaces it with rows.
	selectTickets = `
SELECT ` + ticketColumns + `
FROM tickets
WHERE (? = 1 OR state <> ?)
ORDER BY updated_at DESC, id
LIMIT ? OFFSET ?`

	// selectTicketsByClient is the client view as its own statement. It is not a
	// second dialect of SQL for style's sake: the `(? = '' OR client_id = ?)`
	// disjunct of the old single statement made SQLite skip the
	// tickets_activity_idx seek, which at the declared ceiling (100k tickets)
	// measured 2.4s instrumented against a 200ms budget. Split, the client view
	// seeks by client_id and the business view walks the activity index.
	selectTicketsByClient = `
SELECT ` + ticketColumns + `
FROM tickets
WHERE client_id = ?
  AND (? = 1 OR state <> ?)
ORDER BY updated_at DESC, id
LIMIT ? OFFSET ?`

	// updateTicketStateGuarded is the single transition write. The `state = ?`
	// predicate is the guard: the row is only changed if it is still in the state
	// the use case read, so two concurrent actors cannot silently overwrite each
	// other's transition (the loser gets found=false and a semantic rejection).
	updateTicketStateGuarded = `
UPDATE tickets
SET state = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
WHERE id = ? AND state = ?`

	// touchTicket records activity on the ticket. It does not change the state:
	// the inactivity clock of the F2 scheduler reads updated_at, and a note has to
	// reset it.
	touchTicket = `
UPDATE tickets
SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
WHERE id = ?`

	// touchOpenTicket records activity ONLY while the ticket is open. The
	// `state <> ?` predicate is the guard of a note: the message of a
	// conversation lands on an open ticket or it lands nowhere (a closed
	// conversation is reopened by opening a new ticket, RF5). RowsAffected = 0 is
	// therefore "the guard refused", exactly like updateTicketStateGuarded.
	touchOpenTicket = `
UPDATE tickets
SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
WHERE id = ? AND state <> ?`

	insertTicketEvent = `
INSERT INTO ticket_events (ticket_id, actor, kind, payload) VALUES (?, ?, ?, ?)`

	ticketEventColumns = `id, ticket_id, actor, kind, payload, created_at`

	// selectTicketEvents orders by the AUTOINCREMENT id: insertion order IS the
	// chronological order of the history (the timestamps are millisecond-precision
	// and can tie).
	selectTicketEvents = `SELECT ` + ticketEventColumns + ` FROM ticket_events WHERE ticket_id = ? ORDER BY id`

	selectTicketEventByID = `SELECT ` + ticketEventColumns + ` FROM ticket_events WHERE id = ?`
)

// emptyEventPayload is the payload of an event that carries no data. The column
// defaults to '{}' and is CHECKed as valid JSON, so the adapter writes the empty
// object instead of an empty string.
const emptyEventPayload = "{}"

// TicketsRepo is the SQLite adapter of the tickets and their history (satisfied
// by usecase.TicketStore).
type TicketsRepo struct {
	db *sql.DB
}

// NewTicketsRepo wires the adapter to its database handle. A nil handle is a
// fail-closed configuration error: an adapter without a database can only fail.
func NewTicketsRepo(handle *sql.DB) (*TicketsRepo, error) {
	if handle == nil {
		return nil, errors.New("repositorio de tickets: se requiere una conexión a la base")
	}
	return &TicketsRepo{db: handle}, nil
}

// CreateTicket inserts the ticket and its opening event inside one transaction.
//
// Both, or neither: RF5's first acceptance criterion is "queda en el estado
// inicial del tipo ... y un evento de creación auditado", so a ticket without its
// creation event would be a history hole, and an event without its ticket could
// not be stored at all (the FK forbids it).
func (r *TicketsRepo) CreateTicket(ctx context.Context, ticket dto.NewTicket) error {
	if err := validateNewTicket(ticket); err != nil {
		return err
	}
	payload, err := eventPayload(ticket.OpeningEvent.Payload)
	if err != nil {
		return err
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("repositorio de tickets: abrir la transacción: %w", err)
	}
	// Rollback after a failed statement and after a successful commit (where it
	// answers sql.ErrTxDone) — the deferred call is the leak guard, not the
	// control flow.
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, insertTicket,
		ticket.ID,
		ticket.TicketTypeID,
		ticket.ClientID,
		string(ticket.State),
	); err != nil {
		return fmt.Errorf("repositorio de tickets: insertar el ticket: %w", err)
	}

	// The event is written against THIS ticket's id: the carrier's own TicketID is
	// ignored, so an event can never be filed under the wrong ticket by a caller
	// mistake.
	if _, err := tx.ExecContext(ctx, insertTicketEvent,
		ticket.ID,
		ticket.OpeningEvent.Actor,
		ticket.OpeningEvent.Kind,
		payload,
	); err != nil {
		return fmt.Errorf("repositorio de tickets: insertar el evento de apertura: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("repositorio de tickets: confirmar el ticket: %w", err)
	}
	return nil
}

// GetTicket returns one stored ticket, or found=false. It is the read that carries
// the state, which is what every use case branches on ("is this ticket closed?").
func (r *TicketsRepo) GetTicket(ctx context.Context, id string) (dto.TicketRecord, bool, error) {
	if id == "" {
		return dto.TicketRecord{}, false, errors.New("repositorio de tickets: se requiere el id del ticket")
	}

	record, err := scanTicket(r.db.QueryRowContext(ctx, selectTicketByID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return dto.TicketRecord{}, false, nil
	}
	if err != nil {
		return dto.TicketRecord{}, false, fmt.Errorf("repositorio de tickets: leer el ticket: %w", err)
	}
	return record, true, nil
}

// ListTicketsByClient returns the tickets of one client, most recently active
// first.
func (r *TicketsRepo) ListTicketsByClient(ctx context.Context, clientID string, includeClosed bool, limit, offset int) ([]dto.TicketRecord, error) {
	if clientID == "" {
		return nil, errors.New("repositorio de tickets: se requiere el id del cliente")
	}
	return r.queryTickets(ctx, clientID, includeClosed, limit, offset)
}

// ListTickets returns the tickets of every client (the internal views).
func (r *TicketsRepo) ListTickets(ctx context.Context, query dto.TicketQuery) ([]dto.TicketRecord, error) {
	return r.queryTickets(ctx, query.ClientID, query.IncludeClosed, query.Limit, query.Offset)
}

// queryTickets runs the shared list statement.
func (r *TicketsRepo) queryTickets(ctx context.Context, clientID string, includeClosed bool, limit, offset int) ([]dto.TicketRecord, error) {
	if limit <= 0 {
		return nil, errors.New("repositorio de tickets: la lista requiere un límite positivo")
	}
	if offset < 0 {
		return nil, errors.New("repositorio de tickets: la lista requiere un desplazamiento no negativo")
	}

	includeClosedFlag := 0
	if includeClosed {
		includeClosedFlag = 1
	}

	var (
		rows *sql.Rows
		err  error
	)
	if clientID == "" {
		rows, err = r.db.QueryContext(ctx, selectTickets,
			includeClosedFlag, string(dto.TicketStateClosed),
			limit, offset,
		)
	} else {
		rows, err = r.db.QueryContext(ctx, selectTicketsByClient,
			clientID,
			includeClosedFlag, string(dto.TicketStateClosed),
			limit, offset,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("repositorio de tickets: listar: %w", err)
	}
	defer func() { _ = rows.Close() }()

	tickets := make([]dto.TicketRecord, 0, limit)
	for rows.Next() {
		record, err := scanTicket(rows)
		if err != nil {
			return nil, fmt.Errorf("repositorio de tickets: leer resultados: %w", err)
		}
		tickets = append(tickets, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("repositorio de tickets: leer resultados: %w", err)
	}
	return tickets, nil
}

// TransitionTicket applies the guarded state change and appends its event inside
// one transaction, reporting whether the row was still in transition.From.
//
// The guard is what makes the audit honest under concurrency: a transition that
// no longer applies (another actor moved the ticket) writes nothing at all — no
// state change and no event — instead of recording a from-state that was not the
// real one.
func (r *TicketsRepo) TransitionTicket(ctx context.Context, transition dto.TicketTransition) (bool, error) {
	if err := validateTransition(transition); err != nil {
		return false, err
	}
	payload, err := eventPayload(transition.Event.Payload)
	if err != nil {
		return false, err
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("repositorio de tickets: abrir la transición: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	result, err := tx.ExecContext(ctx, updateTicketStateGuarded,
		string(transition.To),
		transition.TicketID,
		string(transition.From),
	)
	if err != nil {
		return false, fmt.Errorf("repositorio de tickets: cambiar el estado: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("repositorio de tickets: confirmar el cambio de estado: %w", err)
	}
	if affected == 0 {
		// The ticket is gone or is no longer in From. Either way the use case owns
		// the Spanish rejection; the adapter only reports the fact, and the
		// deferred rollback discards nothing because nothing was written.
		return false, nil
	}

	if _, err := tx.ExecContext(ctx, insertTicketEvent,
		transition.TicketID,
		transition.Event.Actor,
		transition.Event.Kind,
		payload,
	); err != nil {
		return false, fmt.Errorf("repositorio de tickets: insertar el evento de la transición: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("repositorio de tickets: confirmar la transición: %w", err)
	}
	return true, nil
}

// AppendTicketEvent appends one event and reads it back, so the caller gets the
// database-assigned id and timestamp instead of having to guess them.
//
// There is no update and no delete: the history is append-only (AUTOINCREMENT ids
// guarantee a deleted event's id is never reused, and nothing here ever deletes).
func (r *TicketsRepo) AppendTicketEvent(ctx context.Context, event dto.NewTicketEvent) (dto.TicketEventRecord, error) {
	if err := validateNewEvent(event); err != nil {
		return dto.TicketEventRecord{}, err
	}
	payload, err := eventPayload(event.Payload)
	if err != nil {
		return dto.TicketEventRecord{}, err
	}

	result, err := r.db.ExecContext(ctx, insertTicketEvent,
		event.TicketID,
		event.Actor,
		event.Kind,
		payload,
	)
	if err != nil {
		return dto.TicketEventRecord{}, fmt.Errorf("repositorio de tickets: insertar el evento: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return dto.TicketEventRecord{}, fmt.Errorf("repositorio de tickets: leer el id del evento: %w", err)
	}

	record, err := scanTicketEvent(r.db.QueryRowContext(ctx, selectTicketEventByID, id))
	if err != nil {
		return dto.TicketEventRecord{}, fmt.Errorf("repositorio de tickets: leer el evento insertado: %w", err)
	}
	return record, nil
}

// ListTicketEvents returns a ticket's history, oldest first.
func (r *TicketsRepo) ListTicketEvents(ctx context.Context, ticketID string) ([]dto.TicketEventRecord, error) {
	if ticketID == "" {
		return nil, errors.New("repositorio de tickets: se requiere el id del ticket para leer su historial")
	}

	rows, err := r.db.QueryContext(ctx, selectTicketEvents, ticketID)
	if err != nil {
		return nil, fmt.Errorf("repositorio de tickets: leer el historial: %w", err)
	}
	defer func() { _ = rows.Close() }()

	events := make([]dto.TicketEventRecord, 0, 8)
	for rows.Next() {
		record, err := scanTicketEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("repositorio de tickets: leer el historial: %w", err)
		}
		events = append(events, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("repositorio de tickets: leer el historial: %w", err)
	}
	return events, nil
}

// AddTicketNote appends a note to the ticket's history, moves its activity and
// reads the ticket back, all inside ONE transaction guarded by the ticket still
// being open (review R1-NOTE-RACE / R3-note-nonatomic).
//
// The three steps are one write group for the same reason the creation is: a
// note that is audited while updated_at stays put leaves the F2 inactivity clock
// believing the conversation is idle, and a note written after somebody closed
// the ticket reopens a conversation RF5 closed. The guard is decided by the
// database (`state <> closed`), not by the read the use case already did, so the
// decision and the write cannot interleave with another actor.
//
// It reports found=false when the ticket is gone or is closed: either way nothing
// was written, and the use case owns the Spanish rejection.
func (r *TicketsRepo) AddTicketNote(ctx context.Context, note dto.TicketNoteAppend) (dto.TicketRecord, bool, error) {
	if err := validateNoteAppend(note); err != nil {
		return dto.TicketRecord{}, false, err
	}
	payload, err := eventPayload(note.Event.Payload)
	if err != nil {
		return dto.TicketRecord{}, false, err
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return dto.TicketRecord{}, false, fmt.Errorf("repositorio de tickets: abrir la nota: %w", err)
	}
	// The deferred rollback is the leak guard and the atomicity of the failed
	// append: if the event insert fails, the activity touch above it is discarded
	// with the transaction.
	defer func() { _ = tx.Rollback() }()

	activity, err := tx.ExecContext(ctx, touchOpenTicket, note.TicketID, string(note.ClosedState))
	if err != nil {
		return dto.TicketRecord{}, false, fmt.Errorf("repositorio de tickets: marcar la actividad de la nota: %w", err)
	}
	opened, err := activity.RowsAffected()
	if err != nil {
		return dto.TicketRecord{}, false, fmt.Errorf("repositorio de tickets: confirmar la actividad de la nota: %w", err)
	}
	if opened == 0 {
		// Closed or gone: nothing was written, so the rollback has nothing to undo.
		return dto.TicketRecord{}, false, nil
	}

	// The event is filed against THIS ticket's id (the carrier's own TicketID is
	// ignored), so a note can never be attached to the wrong history.
	if _, err := tx.ExecContext(ctx, insertTicketEvent,
		note.TicketID,
		note.Event.Actor,
		note.Event.Kind,
		payload,
	); err != nil {
		return dto.TicketRecord{}, false, fmt.Errorf("repositorio de tickets: insertar la nota: %w", err)
	}

	// The readback happens in the same transaction, so the caller gets the ticket
	// as it is after its own note and not as some later writer left it.
	record, err := scanTicket(tx.QueryRowContext(ctx, selectTicketByID, note.TicketID))
	if err != nil {
		return dto.TicketRecord{}, false, fmt.Errorf("repositorio de tickets: leer el ticket anotado: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return dto.TicketRecord{}, false, fmt.Errorf("repositorio de tickets: confirmar la nota: %w", err)
	}
	return record, true, nil
}

// TouchTicket records activity on the ticket and reports whether the row existed.
func (r *TicketsRepo) TouchTicket(ctx context.Context, id string) (bool, error) {
	if id == "" {
		return false, errors.New("repositorio de tickets: se requiere el id del ticket a marcar")
	}

	result, err := r.db.ExecContext(ctx, touchTicket, id)
	if err != nil {
		return false, fmt.Errorf("repositorio de tickets: marcar la actividad: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("repositorio de tickets: confirmar la actividad: %w", err)
	}
	return affected > 0, nil
}

// scanTicket fills the carrier from one row. assigned_to is nullable (the
// unassigned pool of RF5) and becomes the empty string, so no consumer ever sees
// a sql.Null* type.
func scanTicket(row scanner) (dto.TicketRecord, error) {
	var (
		record     dto.TicketRecord
		state      string
		assignedTo sql.NullString
	)
	if err := row.Scan(
		&record.ID,
		&record.TicketTypeID,
		&record.ClientID,
		&state,
		&assignedTo,
		&record.CreatedAt,
		&record.UpdatedAt,
	); err != nil {
		return dto.TicketRecord{}, err
	}

	record.State = dto.TicketState(state)
	record.AssignedTo = assignedTo.String
	return record, nil
}

// scanTicketEvent fills the carrier from one history row.
func scanTicketEvent(row scanner) (dto.TicketEventRecord, error) {
	var record dto.TicketEventRecord
	if err := row.Scan(
		&record.ID,
		&record.TicketID,
		&record.Actor,
		&record.Kind,
		&record.Payload,
		&record.CreatedAt,
	); err != nil {
		return dto.TicketEventRecord{}, err
	}
	return record, nil
}

// validateNewTicket rejects an incomplete row before it reaches the database.
// These are programming errors (the application layer builds the row), so they are
// internal and stay untranslated.
func validateNewTicket(ticket dto.NewTicket) error {
	if ticket.ID == "" {
		return errors.New("repositorio de tickets: el ticket a crear no tiene id interno")
	}
	if ticket.TicketTypeID == "" {
		return errors.New("repositorio de tickets: el ticket a crear no tiene tipo")
	}
	if ticket.ClientID == "" {
		return errors.New("repositorio de tickets: el ticket a crear no tiene cliente")
	}
	if !ticket.State.Valid() {
		return fmt.Errorf("repositorio de tickets: estado inicial desconocido %q", string(ticket.State))
	}
	return validateNewEvent(dto.NewTicketEvent{
		TicketID: ticket.ID,
		Actor:    ticket.OpeningEvent.Actor,
		Kind:     ticket.OpeningEvent.Kind,
		Payload:  ticket.OpeningEvent.Payload,
	})
}

// validateTransition rejects an incomplete transition before it reaches the
// database.
func validateTransition(transition dto.TicketTransition) error {
	if transition.TicketID == "" {
		return errors.New("repositorio de tickets: la transición no tiene ticket")
	}
	if transition.From == "" || transition.To == "" {
		return errors.New("repositorio de tickets: la transición requiere estado de origen y destino")
	}
	if transition.From == transition.To {
		return errors.New("repositorio de tickets: la transición no cambia el estado")
	}
	return validateNewEvent(dto.NewTicketEvent{
		TicketID: transition.TicketID,
		Actor:    transition.Event.Actor,
		Kind:     transition.Event.Kind,
		Payload:  transition.Event.Payload,
	})
}

// validateNoteAppend rejects a note that cannot be written before a transaction
// exists: the same rules the transition enforces, plus the state the guard
// refuses (an unknown state would make `state <> ?` silently accept a closed
// ticket).
func validateNoteAppend(note dto.TicketNoteAppend) error {
	if note.TicketID == "" {
		return errors.New("repositorio de tickets: la nota no tiene ticket")
	}
	if !note.ClosedState.Valid() {
		return fmt.Errorf("repositorio de tickets: el estado que cierra la conversación es desconocido %q", string(note.ClosedState))
	}
	return validateNewEvent(dto.NewTicketEvent{
		TicketID: note.TicketID,
		Actor:    note.Event.Actor,
		Kind:     note.Event.Kind,
		Payload:  note.Event.Payload,
	})
}

// validateNewEvent rejects an event the schema would refuse, so the caller gets
// the error before a transaction rather than after it.
func validateNewEvent(event dto.NewTicketEvent) error {
	if event.TicketID == "" {
		return errors.New("repositorio de tickets: el evento no tiene ticket")
	}
	// actor and kind are CHECK (length > 0) in the schema: an empty one is a
	// programming error, not a business rejection.
	if strings.TrimSpace(event.Actor) == "" {
		return errors.New("repositorio de tickets: el evento no tiene actor")
	}
	if strings.TrimSpace(event.Kind) == "" {
		return errors.New("repositorio de tickets: el evento no tiene tipo")
	}
	return nil
}

// eventPayload normalizes and validates the payload document: the schema CHECKs
// json_valid, and an invalid document must be an internal error here instead of a
// driver failure in the middle of a transaction.
func eventPayload(payload string) (string, error) {
	if strings.TrimSpace(payload) == "" {
		return emptyEventPayload, nil
	}
	if !json.Valid([]byte(payload)) {
		return "", errors.New("repositorio de tickets: el payload del evento no es un documento JSON válido")
	}
	return payload, nil
}
