package db

import (
	"database/sql"
	"strings"
	"testing"
)

// mustExec runs a statement that is expected to succeed.
func mustExec(t *testing.T, handle *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := handle.Exec(query, args...); err != nil {
		t.Fatalf("ejecutar %q: %v", query, err)
	}
}

// mustFail runs a statement that the schema must reject, and asserts the
// rejection mentions want (so a different failure cannot masquerade as the
// invariant being tested).
func mustFail(t *testing.T, handle *sql.DB, want, query string, args ...any) {
	t.Helper()
	_, err := handle.Exec(query, args...)
	if err == nil {
		t.Fatalf("la sentencia %q debía ser rechazada por el esquema", query)
	}
	if !strings.Contains(strings.ToUpper(err.Error()), strings.ToUpper(want)) {
		t.Fatalf("error = %v, se esperaba una violación de %q", err, want)
	}
}

// TestSchemaTables pins the phase scope of the schema: the F1 and F2 tables
// exist and later-phase tables do not, so a phase's schema cannot be created by
// accident before its own migration (and its own ODD work unit).
//
// F2 (migration 5) flips the four entries the F1 version of this test asserted
// absent: areas, ticket_states, ticket_transitions and alerts are the workflow
// engine's tables and now part of the product.
func TestSchemaTables(t *testing.T) {
	handle := openTempDB(t)

	present := []string{
		"accounts",
		"clients",
		"business_profile",
		"catalog_items",
		"ticket_types",
		"tickets",
		"ticket_events",
		"schema_migrations",
		// F2 (migration 5): the configurable workflow and the alert queue.
		"areas",
		"areas_accounts",
		"ticket_states",
		"ticket_transitions",
		"alerts",
	}
	for _, table := range present {
		if got := queryValue(t, handle,
			`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table); got != "1" {
			t.Errorf("falta la tabla %q del alcance F1/F2", table)
		}
	}

	absent := []string{
		"schedules", "hours_exceptions", "bookings", "sessions",
	}
	for _, table := range absent {
		if got := queryValue(t, handle,
			`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table); got != "0" {
			t.Errorf("la tabla %q pertenece a una fase posterior y no debe existir todavía", table)
		}
	}
}

// TestTicketTypeSeed pins the seeded ticket types: the fixed F1 'consulta' with
// its documented identifier, the F2 'venta' template next to it, and their
// default area (PRD §3.7: 'venta' links the Ventas area, 'consulta' ships "sin
// área fija": NULL).
func TestTicketTypeSeed(t *testing.T) {
	handle := openTempDB(t)

	if got := queryValue(t, handle, `SELECT id FROM ticket_types WHERE name = ?`, TicketTypeConsultaName); got != TicketTypeConsultaID {
		t.Fatalf("id del tipo %q = %q, se esperaba %q", TicketTypeConsultaName, got, TicketTypeConsultaID)
	}
	if got := queryValue(t, handle, `SELECT id FROM ticket_types WHERE name = ?`, TicketTypeVentaName); got != TicketTypeVentaID {
		t.Fatalf("id del tipo %q = %q, se esperaba %q", TicketTypeVentaName, got, TicketTypeVentaID)
	}
	if got := queryValue(t, handle, `SELECT COUNT(*) FROM ticket_types`); got != "2" {
		t.Fatalf("tipos de ticket = %s, se esperaban los dos templates seed ('venta' y 'consulta')", got)
	}

	// PRD §3.7: la plantilla 'venta' declara el área Ventas; la 'consulta' no
	// tiene área fija, así que su columna queda NULL.
	if got := queryValue(t, handle, `SELECT default_area_id FROM ticket_types WHERE name = ?`, TicketTypeVentaName); got != AreaVentasID {
		t.Fatalf("área por defecto de 'venta' = %q, se esperaba el área Ventas %q", got, AreaVentasID)
	}
	if got := queryValue(t, handle, `SELECT COUNT(*) FROM ticket_types WHERE name = ? AND default_area_id IS NULL`, TicketTypeConsultaName); got != "1" {
		t.Fatalf("'consulta' debe quedar sin área fija (default_area_id NULL), count=%s", got)
	}
	if got := queryValue(t, handle, `SELECT name FROM areas WHERE id = ?`, AreaVentasID); got != AreaVentasName {
		t.Fatalf("área sembrada %q = %q, se esperaba %q", AreaVentasID, got, AreaVentasName)
	}

	// The owner account is NOT seeded: only F4's setup --create-owner creates
	// internal accounts, so a fresh database has none.
	if got := queryValue(t, handle, `SELECT COUNT(*) FROM accounts`); got != "0" {
		t.Fatalf("cuentas sembradas = %s, se esperaba 0 (setup --create-owner es F4)", got)
	}
}

// The canonical kind vocabulary of the DDL (PRD §3.7), kept as raw strings so
// every fixture uses the byte-identical value the CHECK constraint carries.
const (
	// Valor canónico del PRD — incluido en misspell ignore-rules del
	// .golangci.yml (no es una palabra inglesa mal escrita).
	kindProducto = `producto`
	kindServicio = `servicio`
)

// TestCatalogInvariants pins the RF4 rule at the file level: only services are
// bookable, so only services can carry a duration; the kind vocabulary and the
// price range are CHECK-constrained.
func TestCatalogInvariants(t *testing.T) {
	handle := openTempDB(t)
	const insert = `INSERT INTO catalog_items (id, kind, name, description, use_cases, limitations, price_cents, sales_url, duration_min) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`

	t.Run("ítem de venta con duración es rechazado", func(t *testing.T) {
		mustFail(t, handle, "CHECK", insert,
			uuidLike(1), kindProducto, "Crema", "Pote de 100 g", "Piel seca", "No cubre psoriasis",
			int64(150000), "https://tienda.example/crema", int64(30))
	})

	t.Run("servicio con duración es aceptado", func(t *testing.T) {
		mustExec(t, handle, insert,
			uuidLike(2), kindServicio, "Consulta", "Una sesión", "Dolor lumbar", "No cirugías",
			int64(500000), nil, int64(45))
	})

	t.Run("servicio sin duración es aceptado (nullable)", func(t *testing.T) {
		mustExec(t, handle, insert,
			uuidLike(3), kindServicio, "Presupuesto", "Visita a domicilio", nil, nil, nil, nil, nil)
	})

	t.Run("ítem de venta sin precio es aceptado (precio a consultar)", func(t *testing.T) {
		mustExec(t, handle, insert,
			uuidLike(4), kindProducto, "Repuesto", "", nil, nil, nil, nil, nil)
	})

	t.Run("kind fuera del vocabulario es rechazado", func(t *testing.T) {
		mustFail(t, handle, "CHECK", insert,
			uuidLike(5), "otro", "Algo", "", nil, nil, nil, nil, nil)
	})

	t.Run("precio negativo es rechazado", func(t *testing.T) {
		mustFail(t, handle, "CHECK", insert,
			uuidLike(6), kindProducto, "Algo", "", nil, nil, int64(-1), nil, nil)
	})
}

// TestBusinessProfileIsSingleRow pins the pinned primary key: a second profile
// row cannot exist, and the JSON contact matrix must be a valid document.
func TestBusinessProfileIsSingleRow(t *testing.T) {
	handle := openTempDB(t)
	const insert = `INSERT INTO business_profile (id, name) VALUES (?, ?)`

	mustExec(t, handle, insert, 1, "Taller Kike")
	mustFail(t, handle, "CHECK", insert, 2, "Otra sucursal")
	mustFail(t, handle, "CHECK", `UPDATE business_profile SET social_links = ? WHERE id = 1`, "no-es-json")
	mustFail(t, handle, "CHECK", `UPDATE business_profile SET lat = ? WHERE id = 1`, 91.0)
	mustExec(t, handle, `UPDATE business_profile SET social_links = ?, lat = ?, lng = ? WHERE id = 1`,
		`{"instagram":"https://instagram.com/taller"}`, -34.6, -58.4)
}

// TestTicketEventsInvariants pins the audit row: a ticket event always carries
// a non-empty actor and kind and a parseable JSON payload.
func TestTicketEventsInvariants(t *testing.T) {
	handle := openTempDB(t)

	clientID, ticketID := uuidLike(10), uuidLike(11)
	mustExec(t, handle, `INSERT INTO clients (id, phone) VALUES (?, ?)`, clientID, "+5491100000010")
	mustExec(t, handle, `INSERT INTO tickets (id, ticket_type_id, client_id) VALUES (?, ?, ?)`,
		ticketID, TicketTypeConsultaID, clientID)

	// The initial state of a ticket is the seeded one, without an explicit value.
	if got := queryValue(t, handle, `SELECT state FROM tickets WHERE id = ?`, ticketID); got != TicketStateInitial {
		t.Fatalf("estado inicial = %q, se esperaba %q", got, TicketStateInitial)
	}

	mustExec(t, handle, `INSERT INTO ticket_events (ticket_id, actor, kind, payload) VALUES (?, ?, ?, ?)`,
		ticketID, "system", "creado", `{"motivo":"consulta"}`)

	mustFail(t, handle, "CHECK",
		`INSERT INTO ticket_events (ticket_id, actor, kind) VALUES (?, ?, ?)`, "revisar", ticketID, "", "nota")
	mustFail(t, handle, "CHECK",
		`INSERT INTO ticket_events (ticket_id, actor, kind, payload) VALUES (?, ?, ?, ?)`, "json_valid", ticketID, "+5491100000010", "nota", "{")
	mustFail(t, handle, "FOREIGN KEY",
		`INSERT INTO ticket_events (ticket_id, actor, kind) VALUES (?, ?, ?)`, "FOREIGN KEY", uuidLike(12), "system", "nota")

	// PII masking is the writer's job (T8); the column stores what it is given.
	mustExec(t, handle, `INSERT INTO ticket_events (ticket_id, actor, kind, payload) VALUES (?, ?, ?, ?)`,
		ticketID, "+5491100000010", "nota", `{"texto":"consulta"}`)
	if got := queryValue(t, handle, `SELECT COUNT(*) FROM ticket_events`); got != "2" {
		t.Fatalf("eventos = %s, se esperaban 2", got)
	}
}

// TestDefaultTimestamps pins the timestamp representation: ISO-8601 UTC with a
// trailing Z, so ordering in SQL and parsing in Go agree.
func TestDefaultTimestamps(t *testing.T) {
	handle := openTempDB(t)

	mustExec(t, handle, `INSERT INTO clients (id) VALUES (?)`, uuidLike(20))
	row := handle.QueryRow(`SELECT created_at, updated_at FROM clients WHERE id = ?`, uuidLike(20))

	var createdAt, updatedAt string
	if err := row.Scan(&createdAt, &updatedAt); err != nil {
		t.Fatalf("leer timestamps: %v", err)
	}
	for name, value := range map[string]string{"created_at": createdAt, "updated_at": updatedAt} {
		if !looksLikeTimestamp(value) {
			t.Errorf("%s = %q, se esperaba ISO-8601 UTC con 'Z'", name, value)
		}
	}
}

// TestIdentifierUniqueness pins the two uniqueness promises the resolver
// relies on to answer "0 o 1, nunca ambiguo" (PRD §3.7): e-mail is UNIQUE
// case-insensitively in both tables, phone is UNIQUE verbatim, and a NULL
// natural identifier never collides with another NULL (an e-mail-first and a
// phone-first client must both be storable).
func TestIdentifierUniqueness(t *testing.T) {
	handle := openTempDB(t)

	mustExec(t, handle, `INSERT INTO accounts (id, email, role, phone) VALUES (?, ?, ?, ?)`,
		uuidLike(40), "Owner@Example.com", "owner", "+5491100000040")
	mustFail(t, handle, "UNIQUE", `INSERT INTO accounts (id, email, role) VALUES (?, ?, ?)`,
		uuidLike(41), "owner@example.com", "admin")
	mustFail(t, handle, "UNIQUE", `INSERT INTO accounts (id, email, role, phone) VALUES (?, ?, ?, ?)`,
		uuidLike(42), "otro@example.com", "staff", "+5491100000040")

	mustExec(t, handle, `INSERT INTO clients (id, email) VALUES (?, ?)`, uuidLike(43), "Cliente@Example.com")
	mustFail(t, handle, "UNIQUE", `INSERT INTO clients (id, email) VALUES (?, ?)`,
		uuidLike(44), "cliente@example.com")
	mustFail(t, handle, "UNIQUE", `INSERT INTO clients (id, email) VALUES (?, ?)`,
		uuidLike(45), "CLIENTE@EXAMPLE.COM")

	// NULL identifiers are distinct from each other: both registration paths of
	// RF3 must be able to coexist.
	mustExec(t, handle, `INSERT INTO clients (id) VALUES (?)`, uuidLike(46))
	mustExec(t, handle, `INSERT INTO clients (id, phone) VALUES (?, ?)`, uuidLike(47), "+5491100000047")
	mustFail(t, handle, "UNIQUE", `INSERT INTO clients (id, phone) VALUES (?, ?)`,
		uuidLike(48), "+5491100000047")
}
