package db

// This file is the canonical schema of the product: the only place where DDL
// exists (AGENTS.md boundary contract 3). Everything here is append-only —
// a later phase adds a NEW numbered migration and never edits a step that
// production databases have already applied.

// The seeded, fixed F1 artifacts. They are constants rather than literals in
// the seed migration because later work (T8 creates the demo ticket) must be
// able to reference them without duplicating string literals, and because a
// stable identifier keeps independent databases comparable.
const (
	// TicketTypeConsultaID is the internal UUID of the single F1 ticket type.
	// It is a fixed synthetic UUID (not generated at seed time) so a row
	// reference stays valid across backups, restores and re-seeded databases.
	TicketTypeConsultaID = "00000000-0000-4000-8000-000000000001"

	// TicketTypeConsultaName is the fixed F1 ticket type: the demo contact
	// type ('consulta' of the PRD's seed templates).
	TicketTypeConsultaName = "consulta"

	// TicketStateInitial is the state a new ticket starts in. F1 has no
	// workflow engine (F2 owns ticket_states/ticket_transitions), so the
	// initial state is this column default.
	TicketStateInitial = "nuevo"
)

// migration is one numbered, append-only DDL step. statements run in order
// inside a single transaction together with the schema_migrations row that
// records the step, so a database is never left at a half-applied version.
type migration struct {
	version    int
	statements []string
}

// migrations is the ordered sequence of schema steps this binary knows. The
// runner validates that versions are exactly 1..N with no gaps and refuses any
// database that reports a version outside this list (migrate.go).
//
// To extend the schema: append a new entry with the next version number. Do
// not reorder, renumber, or rewrite an existing entry, and do not add
// down-migrations (see migrate.go for the rationale).
var migrations = []migration{
	{
		version: 1,
		statements: []string{
			ddlAccounts,
			ddlAccountsEmailIndex,
			ddlClients,
			ddlClientsEmailIndex,
			ddlBusinessProfile,
			ddlCatalogItems,
			ddlTicketTypes,
			ddlTickets,
			ddlTicketsClientTypeIndex,
			ddlTicketsAssigneeIndex,
			ddlTicketEvents,
			ddlTicketEventsTicketIndex,
		},
	},
	{
		// Seed step: the single fixed ticket type of F1. It is separated from
		// the DDL step so a future phase can add its seed templates without
		// touching an applied migration.
		version: 2,
		statements: []string{
			seedTicketTypeConsulta,
		},
	},
}

// Timestamps: ISO-8601 UTC with millisecond precision. Lexicographic order
// equals chronological order, and the explicit Z removes any ambiguity about
// the zone the value was written in.
const timestampDefault = `strftime('%Y-%m-%dT%H:%M:%fZ','now')`

// ddlAccounts is the single source of internal identity: panel login and MCP
// caller resolution (PRD §3.7, §3.8).
//
// Deferred columns, and why:
//   - is_bookable (INTEGER) and buffer_min (INTEGER) — F3 (bookings): they only
//     mean something once schedules and the slot calculation exist.
//   - telegram_pending — future Telegram binding by telegram_user_id with
//     Owner/Admin approval; F1 resolves the internal bot by phone.
//
// password_hash is nullable on purpose: the column exists in F1 so the schema
// is stable, but only F4's `setup --create-owner` writes an argon2id hash. A
// NULL hash means "cannot log in yet", never "no password required" — the
// panel (F4) must treat NULL as a failed credential, not as an empty one.
//
// The role CHECK mirrors auth.Role exactly (owner/admin/staff/client) so the
// database cannot drift from the Go enum. F1 only writes the internal roles;
// final clients are `clients` rows with the implicit role client.
const ddlAccounts = `
CREATE TABLE accounts (
    id            TEXT PRIMARY KEY NOT NULL CHECK (length(id) = 36),
    phone         TEXT UNIQUE,
    email         TEXT NOT NULL UNIQUE,
    password_hash TEXT,
    role          TEXT NOT NULL CHECK (role IN ('owner','admin','staff','client')),
    is_active     INTEGER NOT NULL DEFAULT 1 CHECK (is_active IN (0, 1)),
    display_name  TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL DEFAULT (` + timestampDefault + `),
    updated_at    TEXT NOT NULL DEFAULT (` + timestampDefault + `)
)`

// ddlAccountsEmailIndex makes the login identifier UNIQUE case-insensitively.
// The plain UNIQUE on the column only folds at the BINARY collation, so
// 'Owner@Example.com' and 'owner@example.com' could both exist while the
// resolver matches e-mail case-insensitively — two rows would share one login.
// Case folding here is SQLite's built-in lower(), which folds ASCII only:
// non-ASCII local parts compare byte-exact after ASCII folding. That is
// deterministic (0 or 1 row, never ambiguous), which is the invariant that
// matters (PRD §3.7).
const ddlAccountsEmailIndex = `
CREATE UNIQUE INDEX accounts_email_lower_uniq ON accounts (lower(email))`

// ddlClients holds final clients. Internal UUID PK; natural identifiers are
// nullable UNIQUE columns, never foreign keys (PRD §3.7).
//
// phone is stored verbatim exactly as the WhatsApp channel delivered it: no
// trimming, no digit-only normalization, so the byte-exact resolver match is
// the same comparison the UNIQUE index enforces.
//
// Deferred columns, and why:
//   - notes — not read by any F1 consumer; arrives with the client profile
//     work of a later phase.
//   - default_address, default_lat, default_lng — later slices: they model a
//     location shared over WhatsApp for bookings/delivery (F3).
const ddlClients = `
CREATE TABLE clients (
    id           TEXT PRIMARY KEY NOT NULL CHECK (length(id) = 36),
    phone        TEXT UNIQUE,
    email        TEXT,
    display_name TEXT NOT NULL DEFAULT '',
    created_at   TEXT NOT NULL DEFAULT (` + timestampDefault + `),
    updated_at   TEXT NOT NULL DEFAULT (` + timestampDefault + `)
)`

// ddlClientsEmailIndex enforces UNIQUE over lower(email). NULL emails are
// allowed and do not collide (an e-mail-first registration and a phone-first
// registration are both valid clients). Same ASCII-only folding note as
// accounts.
const ddlClientsEmailIndex = `
CREATE UNIQUE INDEX clients_email_lower_uniq ON clients (lower(email))`

// ddlBusinessProfile is the single-row business configuration Hermes reads to
// answer contact/location questions (PRD §3.7). The primary key is pinned to 1
// by CHECK, so a second profile row is rejected by the file.
//
// F1 stores the shape but no F1 code reads it: the profile is configured from
// the panel (F4) and consumed by the profile/contact tools of later phases. A
// missing row means "not configured yet", which tools must report honestly
// instead of inventing data.
//
// social_links is a JSON object map ("instagram" → URL) so the contact matrix
// can grow without a migration; json_valid keeps a malformed document from
// ever landing.
//
// Deferred columns, and why:
//   - messenger_platform, messenger_id — channel pairing config lives in
//     Hermes' runtime configuration; the F6 install documentation owns it, and
//     its CHECK enum would be dead weight until then.
//   - default_delivery_min — F3 (delivery bookings): it is the duration window
//     of a delivery booking, which does not exist before bookings do.
const ddlBusinessProfile = `
CREATE TABLE business_profile (
    id                 INTEGER PRIMARY KEY CHECK (id = 1),
    name               TEXT NOT NULL,
    description        TEXT NOT NULL DEFAULT '',
    phone              TEXT,
    email              TEXT,
    website_url        TEXT,
    online_store_url   TEXT,
    social_links       TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(social_links)),
    address            TEXT,
    lat                REAL CHECK (lat IS NULL OR (lat >= -90 AND lat <= 90)),
    lng                REAL CHECK (lng IS NULL OR (lng >= -180 AND lng <= 180)),
    maps_url           TEXT,
    inactivity_days    INTEGER CHECK (inactivity_days IS NULL OR inactivity_days > 0),
    lead_followup_days INTEGER CHECK (lead_followup_days IS NULL OR lead_followup_days > 0),
    created_at         TEXT NOT NULL DEFAULT (` + timestampDefault + `),
    updated_at         TEXT NOT NULL DEFAULT (` + timestampDefault + `)
)`

// ddlCatalogItems is the unified catalog (PRD §3.7, RF4): products and
// services share one table, and only services are bookable.
//
//   - kind is restricted to the PRD vocabulary ('producto','servicio').
//   - use_cases / limitations are the v1.11 fields: what problems the item
//     solves and what it explicitly does not cover. Both nullable because an
//     incomplete entry must be storable (Hermes simply omits what it does not
//     know rather than inventing it).
//   - price_cents is INTEGER minor units, never a float: floating-point money
//     rounds, and the catalogue is a purchase surface. NULL means "price not
//     published" (Hermes then says the price is to be confirmed instead of
//     inventing a number).
//   - duration_min only exists for bookable items: the CHECK
//     (kind = 'servicio' OR duration_min IS NULL) makes "a product with a
//     duration" impossible, which is the RF4 invariant that only services are
//     bookable (F3 consumes it; F1 only stores and searches).
//
// Deferred columns, and why:
//   - currency — v1 is single-currency by product decision; a currency column
//     now would be a per-row value nobody validates.
//   - FTS5 index over name/description/use_cases — T7 (search_catalog); it is
//     an additive migration on top of this table.
const ddlCatalogItems = `
CREATE TABLE catalog_items (
    id           TEXT PRIMARY KEY NOT NULL CHECK (length(id) = 36),
    kind         TEXT NOT NULL CHECK (kind IN ('producto','servicio')),
    name         TEXT NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    use_cases    TEXT,
    limitations  TEXT,
    price_cents  INTEGER CHECK (price_cents IS NULL OR price_cents >= 0),
    sales_url    TEXT,
    duration_min INTEGER CHECK (duration_min IS NULL OR duration_min > 0),
    is_active    INTEGER NOT NULL DEFAULT 1 CHECK (is_active IN (0, 1)),
    created_at   TEXT NOT NULL DEFAULT (` + timestampDefault + `),
    updated_at   TEXT NOT NULL DEFAULT (` + timestampDefault + `),
    CHECK (kind = 'servicio' OR duration_min IS NULL)
)`

// ddlTicketTypes is the ticket-type catalogue. F1 has exactly one seeded type
// (the seed migration below); F2 turns this into the configurable workflow
// catalogue and adds the 'venta' template.
//
// Deferred columns, and why:
//   - default_area_id — F2: areas do not exist yet. The column and its FK land
//     with the areas migration, not by editing this step.
const ddlTicketTypes = `
CREATE TABLE ticket_types (
    id         TEXT PRIMARY KEY NOT NULL CHECK (length(id) = 36),
    name       TEXT NOT NULL UNIQUE,
    is_active  INTEGER NOT NULL DEFAULT 1 CHECK (is_active IN (0, 1)),
    created_at TEXT NOT NULL DEFAULT (` + timestampDefault + `),
    updated_at TEXT NOT NULL DEFAULT (` + timestampDefault + `)
)`

// ddlTickets is one contact instance (PRD §3.7, RF5).
//
// state has no CHECK on purpose: F1's fixed pipeline is 'nuevo' → 'atendido' →
// 'resuelto', but the states become rows in ticket_states (F2) and a CHECK
// here would force a migration per configured state. The DEFAULT carries the
// seeded initial state instead.
//
// Receiving rows treat a ticket as the audit anchor, so deletions of the rows
// it references are restricted: a client is merged (merge_clients), never
// deleted, and a ticket type that has tickets cannot vanish. assigned_to is
// nullable (unassigned pool of RF5) and clears if the account row is removed —
// the ticket survives its assignee.
//
// Deferred columns, and why:
//   - area_id — F2 (areas).
//   - deadline_at / computed deadlines — F2 (the scheduler computes them).
//   - closed_at — F2, once a state can be terminal by configuration.
const ddlTickets = `
CREATE TABLE tickets (
    id             TEXT PRIMARY KEY NOT NULL CHECK (length(id) = 36),
    ticket_type_id TEXT NOT NULL REFERENCES ticket_types (id) ON DELETE RESTRICT,
    client_id      TEXT NOT NULL REFERENCES clients (id) ON DELETE RESTRICT,
    state          TEXT NOT NULL DEFAULT '` + TicketStateInitial + `',
    assigned_to    TEXT REFERENCES accounts (id) ON DELETE SET NULL,
    created_at     TEXT NOT NULL DEFAULT (` + timestampDefault + `),
    updated_at     TEXT NOT NULL DEFAULT (` + timestampDefault + `)
)`

// ddlTicketsClientTypeIndex serves the RF5 re-engagement lookup ("does this
// client already have an open ticket of this type?") without a table scan.
const ddlTicketsClientTypeIndex = `
CREATE INDEX tickets_client_type_idx ON tickets (client_id, ticket_type_id)`

// ddlTicketsAssigneeIndex serves the staff visibility rule of PRD §3.8 (Q30):
// "my tickets" and "unassigned tickets of my areas" are filtered by assignee
// and state.
const ddlTicketsAssigneeIndex = `
CREATE INDEX tickets_assignee_idx ON tickets (assigned_to, state)`

// ddlTicketEvents is the append-only audit history of RF5: every transition,
// note, re-engagement message and assignment change is a row with actor and
// timestamp.
//
//   - id is an INTEGER AUTOINCREMENT primary key rather than a UUID: the
//     history is ordered by insertion, and AUTOINCREMENT guarantees a deleted
//     event's id is never reused, so a reference to an event stays valid.
//   - actor is the verbatim caller-id of whoever acted, or 'system' for the
//     backend (scheduler, engine). It is a text value, not an FK, because the
//     historical actor must survive account deletion.
//   - kind has no CHECK: the vocabulary grows with the workflow engine (F2
//     adds on-enter/on-exit and template events like 'link enviado'), and a
//     CHECK would demand a migration per new kind.
//   - payload is a JSON object (possibly '{}'); json_valid rejects a malformed
//     document at write time so every reader can parse it.
//
// Deferred: PII masking at write time for the payload (AGENTS.md mandates
// masking phone numbers in audit logs) is the responsibility of the use case
// that writes the event (T8), which owns the masking helper; the column does
// not change.
const ddlTicketEvents = `
CREATE TABLE ticket_events (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    ticket_id  TEXT NOT NULL REFERENCES tickets (id) ON DELETE RESTRICT,
    actor      TEXT NOT NULL CHECK (length(actor) > 0),
    kind       TEXT NOT NULL CHECK (length(kind) > 0),
    payload    TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(payload)),
    created_at TEXT NOT NULL DEFAULT (` + timestampDefault + `)
)`

// ddlTicketEventsTicketIndex orders a ticket's history (ticket_id, id), which
// is the only read pattern F1 and the panel need.
const ddlTicketEventsTicketIndex = `
CREATE INDEX ticket_events_ticket_idx ON ticket_events (ticket_id, id)`

// seedTicketTypeConsulta inserts the single F1 ticket type. The demo owner
// account is deliberately NOT seeded here: internal accounts are created by
// the Owner through `setup --create-owner` (F4), and a seeded account would be
// a shipped credential.
const seedTicketTypeConsulta = `
INSERT INTO ticket_types (id, name) VALUES ('` + TicketTypeConsultaID + `', '` + TicketTypeConsultaName + `')`
