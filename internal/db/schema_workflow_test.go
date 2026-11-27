package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// This file pins the F2 half of the canonical schema (PRD §3.7, migration 5):
// the delete semantics of the configured workflow, the CHECK vocabularies that
// the Go validation mirrors, and the conversion of a database that already holds
// F1 data when the step is applied.

// openAtVersion opens the file at path with the product's connection contract and
// applies only the first upTo known migrations, then returns the handle.
//
// It exists so a test can put REAL F1-era data in a database and then let the
// production Open() apply the pending step, which is the only way to prove that a
// migration works on an existing file and not just on a fresh one.
func openAtVersion(t *testing.T, path string, upTo int) *sql.DB {
	t.Helper()

	dsn, err := buildDSN(path, connectionParams())
	if err != nil {
		t.Fatalf("buildDSN() error inesperado: %v", err)
	}
	handle, err := sql.Open(driverName, dsn)
	if err != nil {
		t.Fatalf("abrir la base de prueba: %v", err)
	}
	t.Cleanup(func() { _ = handle.Close() })

	if _, err := handle.Exec(migrationsTableDDL); err != nil {
		t.Fatalf("crear schema_migrations: %v", err)
	}
	for i := 0; i < upTo; i++ {
		if err := applyMigration(context.Background(), handle, migrations[i]); err != nil {
			t.Fatalf("aplicar la migración %d: %v", migrations[i].version, err)
		}
	}
	return handle
}

// TestMigrationV5ConvertsConsultaTickets is the RF6/RF5 boundary of the step: a
// database that already holds F1 tickets in the F1 vocabulary must end up with
// every ticket pointing at a state its type's config declares, without losing the
// history — and the renamed value must be the terminal state of the template.
func TestMigrationV5ConvertsConsultaTickets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "crm.db")
	f1 := openAtVersion(t, path, 4)

	// F1-era data: one client and one ticket per F1 state, plus the audited
	// history of the closed one.
	mustExec(t, f1, `INSERT INTO clients (id, phone) VALUES (?, ?)`, uuidLike(1), "+5491100000001")
	states := []string{"nuevo", "atendido", f1StateClosed}
	for i, state := range states {
		mustExec(t, f1, `INSERT INTO tickets (id, ticket_type_id, client_id, state) VALUES (?, ?, ?, ?)`,
			uuidLike(10+i), TicketTypeConsultaID, uuidLike(1), state)
	}
	mustExec(t, f1, `INSERT INTO ticket_events (ticket_id, actor, kind, payload) VALUES (?, ?, ?, ?)`,
		uuidLike(12), "system", "transicion", `{"to":"cerrado"}`)
	if err := f1.Close(); err != nil {
		t.Fatalf("cerrar la base en v4: %v", err)
	}

	// The production entry point applies the pending step over the existing file.
	upgraded, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open() sobre una base en v4 error inesperado: %v", err)
	}
	t.Cleanup(func() { _ = upgraded.Close() })

	if got := queryValue(t, upgraded, `SELECT MAX(version) FROM schema_migrations`); got != "5" {
		t.Fatalf("versión aplicada = %s, se esperaba 5", got)
	}

	// PRD §3.7 / S9: 'consulta' es nuevo → atendido → resuelto. La plantilla debe
	// declarar 'resuelto' para que el mapeo de 'cerrado' sea válido, y ningún
	// ticket puede quedar en un estado ajeno a su tipo.
	if got := queryValue(t, upgraded, `SELECT COUNT(*) FROM ticket_states WHERE ticket_type_id = ? AND name = ?`,
		TicketTypeConsultaID, consultaStateResolved); got != "1" {
		t.Fatalf("la plantilla 'consulta' no declara el estado %q (count=%s)", consultaStateResolved, got)
	}
	if got := queryValue(t, upgraded, `SELECT COUNT(*) FROM tickets WHERE state = ?`, f1StateClosed); got != "0" {
		t.Fatalf("quedaron %s tickets en el estado F1 %q tras la conversión", got, f1StateClosed)
	}
	if got := queryValue(t, upgraded, `SELECT state FROM tickets WHERE id = ?`, uuidLike(12)); got != consultaStateResolved {
		t.Fatalf("el ticket cerrado quedó en %q, se esperaba %q", got, consultaStateResolved)
	}
	orphans := queryValue(t, upgraded, `
SELECT COUNT(*) FROM tickets t
WHERE NOT EXISTS (
    SELECT 1 FROM ticket_states s
    WHERE s.ticket_type_id = t.ticket_type_id AND s.name = t.state
)`)
	if orphans != "0" {
		t.Fatalf("%s tickets quedaron en un estado que su tipo no declara", orphans)
	}

	// The states whose name did not change are untouched, and the audit history
	// survives the step.
	for i, state := range []string{"nuevo", "atendido"} {
		if got := queryValue(t, upgraded, `SELECT state FROM tickets WHERE id = ?`, uuidLike(10+i)); got != state {
			t.Fatalf("el ticket %s quedó en %q, se esperaba %q", uuidLike(10+i), got, state)
		}
	}
	if got := queryValue(t, upgraded, `SELECT COUNT(*) FROM ticket_events WHERE ticket_id = ?`, uuidLike(12)); got != "1" {
		t.Fatalf("eventos del ticket convertido = %s, se esperaba 1 (el historial no se toca)", got)
	}
}

// TestWorkflowSeedPipelines pins the seeded pipelines of PRD §3.7 through the
// tables: the state and transition counts of each template, in declaration order.
func TestWorkflowSeedPipelines(t *testing.T) {
	handle := openTempDB(t)

	cases := []struct {
		name        string
		typeID      string
		states      []string
		transitions int
	}{
		{
			name:        TicketTypeVentaName,
			typeID:      TicketTypeVentaID,
			states:      []string{"nuevo", "contactado", "presupuestado", "cerrado", "perdido"},
			transitions: 4,
		},
		{
			name:        TicketTypeConsultaName,
			typeID:      TicketTypeConsultaID,
			states:      []string{"nuevo", "atendido", "resuelto"},
			transitions: 2,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := handle.Query(`SELECT name FROM ticket_states WHERE ticket_type_id = ? ORDER BY position`, tc.typeID)
			if err != nil {
				t.Fatalf("leer estados: %v", err)
			}
			defer func() { _ = rows.Close() }()

			var got []string
			for rows.Next() {
				var name string
				if err := rows.Scan(&name); err != nil {
					t.Fatalf("leer estado: %v", err)
				}
				got = append(got, name)
			}
			if err := rows.Err(); err != nil {
				t.Fatalf("leer estados: %v", err)
			}
			if len(got) != len(tc.states) {
				t.Fatalf("estados de %q = %v, se esperaba %v", tc.name, got, tc.states)
			}
			for i, want := range tc.states {
				if got[i] != want {
					t.Fatalf("estado[%d] de %q = %q, se esperaba %q (orden de declaración)", i, tc.name, got[i], want)
				}
			}
			if count := queryValue(t, handle, `SELECT COUNT(*) FROM ticket_transitions WHERE ticket_type_id = ?`, tc.typeID); count != strconv.Itoa(tc.transitions) {
				t.Fatalf("transiciones de %q = %s, se esperaban %d", tc.name, count, tc.transitions)
			}
		})
	}

	// The round-robin rule rides the transition out of the initial state of
	// 'venta' (PRD §3.7: "área Ventas con round-robin").
	if got := queryValue(t, handle, `SELECT round_robin FROM ticket_transitions WHERE ticket_type_id = ? AND from_state = ? AND to_state = ?`,
		TicketTypeVentaID, "nuevo", "contactado"); got != "1" {
		t.Fatalf("round_robin de nuevo→contactado = %s, se esperaba 1", got)
	}
	if got := queryValue(t, handle, `SELECT COUNT(*) FROM ticket_transitions WHERE ticket_type_id = ? AND round_robin = 1`,
		TicketTypeConsultaID); got != "0" {
		t.Fatalf("'consulta' declaró round-robin (count=%s): la plantilla no auto-asigna", got)
	}
}

// TestAreasInvariants pins the RF7 table: a name is required, bounded and unique,
// and the N:M membership is a set whose links die with either endpoint.
func TestAreasInvariants(t *testing.T) {
	handle := openTempDB(t)
	const insertArea = `INSERT INTO areas (id, name) VALUES (?, ?)`

	t.Run("nombre vacío es rechazado", func(t *testing.T) {
		mustFail(t, handle, "CHECK", insertArea, uuidLike(200), "")
	})

	t.Run("nombre de más de 50 caracteres es rechazado", func(t *testing.T) {
		mustFail(t, handle, "CHECK", insertArea, uuidLike(201), strings.Repeat("a", 51))
	})

	t.Run("nombre duplicado es rechazado", func(t *testing.T) {
		mustExec(t, handle, insertArea, uuidLike(202), "Soporte")
		mustFail(t, handle, "UNIQUE", insertArea, uuidLike(203), "Soporte")
	})

	t.Run("membresía duplicada es rechazada (el par es la clave)", func(t *testing.T) {
		mustExec(t, handle, `INSERT INTO accounts (id, email, role) VALUES (?, ?, ?)`, uuidLike(210), "staff@example.com", "staff")
		mustExec(t, handle, `INSERT INTO areas_accounts (area_id, account_id) VALUES (?, ?)`, uuidLike(202), uuidLike(210))
		mustFail(t, handle, "UNIQUE", `INSERT INTO areas_accounts (area_id, account_id) VALUES (?, ?)`,
			uuidLike(202), uuidLike(210))
	})

	t.Run("cuenta inexistente es rechazada (FOREIGN KEY)", func(t *testing.T) {
		mustFail(t, handle, "FOREIGN KEY", `INSERT INTO areas_accounts (area_id, account_id) VALUES (?, ?)`,
			uuidLike(202), uuidLike(211))
	})

	t.Run("borrar el área borra sus vínculos y no toca la cuenta", func(t *testing.T) {
		mustExec(t, handle, `DELETE FROM areas WHERE id = ?`, uuidLike(202))
		if got := queryValue(t, handle, `SELECT COUNT(*) FROM areas_accounts`); got != "0" {
			t.Fatalf("vínculos tras borrar el área = %s, se esperaba 0 (ON DELETE CASCADE)", got)
		}
		if got := queryValue(t, handle, `SELECT COUNT(*) FROM accounts WHERE id = ?`, uuidLike(210)); got != "1" {
			t.Fatalf("la cuenta desapareció con el área (count=%s)", got)
		}
	})
}

// TestTicketTypeAreaLink pins the ON DELETE SET NULL rule of
// ticket_types.default_area_id: removing an area must never remove a workflow.
func TestTicketTypeAreaLink(t *testing.T) {
	handle := openTempDB(t)

	mustExec(t, handle, `DELETE FROM areas WHERE id = ?`, AreaVentasID)

	if got := queryValue(t, handle, `SELECT COUNT(*) FROM ticket_types WHERE id = ?`, TicketTypeVentaID); got != "1" {
		t.Fatalf("el tipo 'venta' desapareció al borrar su área (count=%s)", got)
	}
	if got := queryValue(t, handle, `SELECT COUNT(*) FROM ticket_types WHERE id = ? AND default_area_id IS NULL`, TicketTypeVentaID); got != "1" {
		t.Fatalf("el área por defecto de 'venta' no quedó en NULL tras borrar el área (count=%s)", got)
	}
	// The type's workflow belongs to the type, not to the area.
	if got := queryValue(t, handle, `SELECT COUNT(*) FROM ticket_states WHERE ticket_type_id = ?`, TicketTypeVentaID); got != "5" {
		t.Fatalf("estados de 'venta' tras borrar el área = %s, se esperaban 5", got)
	}
}

// TestWorkflowConfigInvariants pins the CHECK vocabularies of the config tables:
// the deadline lower bound, the rule flags, the transition role vocabulary and
// the uniqueness of states and transition pairs — plus the cascade that removes a
// type's workflow with the type.
func TestWorkflowConfigInvariants(t *testing.T) {
	handle := openTempDB(t)
	const insertState = `
INSERT INTO ticket_states (id, ticket_type_id, name, position, deadline_hours, on_enter_alert, on_exit_alert)
VALUES (?, ?, ?, ?, ?, ?, ?)`
	const insertTransition = `
INSERT INTO ticket_transitions (id, ticket_type_id, from_state, to_state, position, min_role, round_robin)
VALUES (?, ?, ?, ?, ?, ?, ?)`

	t.Run("deadline de cero horas es rechazado", func(t *testing.T) {
		mustFail(t, handle, "CHECK", insertState, uuidLike(300), TicketTypeVentaID, "estado-cero", 9, 0, 0, 0)
	})

	t.Run("deadline de una hora es aceptado (cota inferior inclusiva)", func(t *testing.T) {
		mustExec(t, handle, insertState, uuidLike(301), TicketTypeVentaID, "estado-una-hora", 9, 1, 0, 0)
	})

	t.Run("flag on-enter fuera de 0/1 es rechazado", func(t *testing.T) {
		mustFail(t, handle, "CHECK", insertState, uuidLike(302), TicketTypeVentaID, "estado-flag", 9, nil, 2, 0)
	})

	t.Run("estado repetido en el mismo tipo es rechazado", func(t *testing.T) {
		mustFail(t, handle, "UNIQUE", insertState, uuidLike(303), TicketTypeVentaID, "nuevo", 9, nil, 0, 0)
	})

	t.Run("estado de un tipo inexistente es rechazado (FOREIGN KEY)", func(t *testing.T) {
		mustFail(t, handle, "FOREIGN KEY", insertState, uuidLike(304), uuidLike(305), "nuevo", 0, nil, 0, 0)
	})

	t.Run("rol 'client' no es un rol de transición", func(t *testing.T) {
		mustFail(t, handle, "CHECK", insertTransition, uuidLike(310), TicketTypeVentaID, "nuevo", "atendido", 9, "client", 0)
	})

	t.Run("auto-transición es rechazada", func(t *testing.T) {
		mustFail(t, handle, "CHECK", insertTransition, uuidLike(311), TicketTypeVentaID, "nuevo", "nuevo", 9, "staff", 0)
	})

	t.Run("par origen-destino repetido es rechazado", func(t *testing.T) {
		mustFail(t, handle, "UNIQUE", insertTransition, uuidLike(312), TicketTypeVentaID, "nuevo", "contactado", 9, "staff", 0)
	})

	t.Run("borrar el tipo borra su workflow", func(t *testing.T) {
		// The type has tickets in the F2 product (FK RESTRICT) but none in this
		// fixture, so the delete is allowed and the workflow must go with it.
		mustExec(t, handle, `DELETE FROM ticket_types WHERE id = ?`, TicketTypeVentaID)
		if got := queryValue(t, handle, `SELECT COUNT(*) FROM ticket_states WHERE ticket_type_id = ?`, TicketTypeVentaID); got != "0" {
			t.Fatalf("estados que sobrevivieron a su tipo = %s, se esperaba 0 (ON DELETE CASCADE)", got)
		}
		if got := queryValue(t, handle, `SELECT COUNT(*) FROM ticket_transitions WHERE ticket_type_id = ?`, TicketTypeVentaID); got != "0" {
			t.Fatalf("transiciones que sobrevivieron a su tipo = %s, se esperaba 0 (ON DELETE CASCADE)", got)
		}
	})
}

// TestAlertsInvariants pins the RF11 queue: the closed kind vocabulary, the
// pending/sent lifecycle, the JSON payload, the target rule (client or account)
// and the pending index the scheduler read relies on.
func TestAlertsInvariants(t *testing.T) {
	handle := openTempDB(t)
	mustExec(t, handle, `INSERT INTO clients (id, phone) VALUES (?, ?)`, uuidLike(400), "+5491100000400")
	mustExec(t, handle, `INSERT INTO accounts (id, email, role) VALUES (?, ?, ?)`, uuidLike(401), "owner@example.com", "owner")

	const insertAlert = `INSERT INTO alerts (id, kind, client_id, account_id, payload) VALUES (?, ?, ?, ?, ?)`

	t.Run("alerta de cliente es aceptada y nace pendiente", func(t *testing.T) {
		mustExec(t, handle, insertAlert, uuidLike(410), "overdue", uuidLike(400), nil, `{"ticket":"1"}`)
		if got := queryValue(t, handle, `SELECT state FROM alerts WHERE id = ?`, uuidLike(410)); got != "pending" {
			t.Fatalf("estado inicial = %q, se esperaba pending", got)
		}
		if got := queryValue(t, handle, `SELECT COUNT(*) FROM alerts WHERE id = ? AND sent_at IS NULL`, uuidLike(410)); got != "1" {
			t.Fatalf("una alerta pendiente no debe tener sent_at (count=%s)", got)
		}
	})

	t.Run("alerta de cuenta (staff) es aceptada", func(t *testing.T) {
		mustExec(t, handle, insertAlert, uuidLike(411), "nuevo-ticket", nil, uuidLike(401), "{}")
	})

	t.Run("alerta de área es aceptada (RF6: overdue para el área asignada)", func(t *testing.T) {
		mustExec(t, handle, `INSERT INTO areas (id, name) VALUES (?, ?)`, uuidLike(418), "Soporte")
		mustExec(t, handle, `INSERT INTO alerts (id, kind, area_id, payload) VALUES (?, ?, ?, ?)`,
			uuidLike(419), "overdue", uuidLike(418), `{}`)
	})

	t.Run("kind fuera del vocabulario del PRD es rechazado", func(t *testing.T) {
		mustFail(t, handle, "CHECK", insertAlert, uuidLike(412), "recordatorio", uuidLike(400), nil, "{}")
	})

	t.Run("estado fuera de pending/sent es rechazado", func(t *testing.T) {
		mustFail(t, handle, "CHECK", `UPDATE alerts SET state = ? WHERE id = ?`, "olvidada", uuidLike(410))
	})

	t.Run("payload inválido es rechazado", func(t *testing.T) {
		mustFail(t, handle, "CHECK", insertAlert, uuidLike(413), "overdue", uuidLike(400), nil, "{")
	})

	t.Run("alerta sin target es rechazada", func(t *testing.T) {
		mustFail(t, handle, "CHECK", insertAlert, uuidLike(414), "overdue", nil, nil, "{}")
	})

	t.Run("cliente inexistente es rechazado (FOREIGN KEY)", func(t *testing.T) {
		mustFail(t, handle, "FOREIGN KEY", insertAlert, uuidLike(415), "overdue", uuidLike(416), nil, "{}")
	})

	t.Run("el índice de pendientes existe", func(t *testing.T) {
		if got := queryValue(t, handle, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, "alerts_pending_idx"); got != "1" {
			t.Fatalf("falta el índice de listado de pendientes (count=%s)", got)
		}
	})

	t.Run("marcar como enviada no la borra", func(t *testing.T) {
		mustExec(t, handle, `UPDATE alerts SET state = ?, sent_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = ?`,
			"sent", uuidLike(410))
		// Quedan pendientes la de cuenta (uuidLike 411) y la de área (uuidLike 419).
		if got := queryValue(t, handle, `SELECT COUNT(*) FROM alerts WHERE state = ?`, "pending"); got != "2" {
			t.Fatalf("pendientes tras marcar una = %s, se esperaban las otras dos", got)
		}
	})
}

// TestTicketsWorkflowColumns pins the three columns migration 5 adds to tickets,
// including their ON DELETE SET NULL rule: an area disappearing must never delete
// a ticket.
func TestTicketsWorkflowColumns(t *testing.T) {
	handle := openTempDB(t)

	mustExec(t, handle, `INSERT INTO clients (id, phone) VALUES (?, ?)`, uuidLike(500), "+5491100000500")
	mustExec(t, handle, `INSERT INTO tickets (id, ticket_type_id, client_id, area_id, deadline_at, closed_at) VALUES (?, ?, ?, ?, ?, ?)`,
		uuidLike(501), TicketTypeVentaID, uuidLike(500), AreaVentasID,
		"2026-12-01T10:00:00.000Z", nil)

	if got := queryValue(t, handle, `SELECT COUNT(*) FROM tickets WHERE id = ? AND deadline_at IS NOT NULL AND closed_at IS NULL`, uuidLike(501)); got != "1" {
		t.Fatalf("las columnas nuevas del ticket no quedaron como se escribieron (count=%s)", got)
	}

	mustExec(t, handle, `DELETE FROM areas WHERE id = ?`, AreaVentasID)
	if got := queryValue(t, handle, `SELECT COUNT(*) FROM tickets WHERE id = ? AND area_id IS NULL`, uuidLike(501)); got != "1" {
		t.Fatalf("el ticket no sobrevivió al borrado de su área con area_id NULL (count=%s)", got)
	}
}
