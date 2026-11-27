package db

import (
	"fmt"

	"github.com/egkike/crm-ai-business/internal/domain"
)

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

	// TicketTypeVentaID is the internal UUID of the F2 'venta' ticket type
	// (PRD §3.7: "'venta': nuevo → contactado → presupuestado →
	// cerrado/perdido, área Ventas con round-robin"). Like the F1 type, it is a
	// fixed synthetic UUID so a row reference stays valid across backups and
	// restores.
	TicketTypeVentaID = "00000000-0000-4000-8000-000000000002"

	// TicketTypeVentaName is the name of the F2 'venta' ticket type.
	TicketTypeVentaName = "venta"

	// AreaVentasID is the internal UUID of the seeded 'Ventas' area (RF7), the
	// area the 'venta' ticket type declares as its default. Fixed for the same
	// reason the seeded types are.
	AreaVentasID = "00000000-0000-4000-8000-000000000101"

	// AreaVentasName is the name of the seeded area.
	AreaVentasName = "Ventas"
)

// The F1 state vocabulary and its mapped F2 vocabulary, as used by the
// conversion of the existing 'consulta' tickets in migration 5.
//
// F1 had no workflow engine: `tickets.state` was free text carrying one of three
// values (dto.TicketState*), and the DDL never constrained them. F2 turns states
// into rows per type, so every stored value must name a declared state of its
// type's config. The 'consulta' template of PRD §3.7 ("'consulta': nuevo →
// atendido → resuelto") keeps two of the three F1 names and renames the
// terminal one, and that is the only data change the conversion needs:
//
//	nuevo    → nuevo     (same name, no row to touch)
//	atendido → atendido  (same name, no row to touch)
//	cerrado  → resuelto  (the terminal state of the template; one UPDATE)
//
// 'resuelto' is the template's terminal state and 'cerrado' was F1's, so the
// mapped ticket keeps its meaning: the conversation ended. Nothing else in the
// product keys on the old value — the note guard travels the closed state as a
// parameter (dto.TicketNoteAppend.ClosedState), and T3 replaces that fixed
// vocabulary with the configured one.
const (
	// f1StateClosed is the F1 terminal state, the only F1 value with no
	// same-name state in the 'consulta' template.
	f1StateClosed = "cerrado"

	// consultaStateResolved is the terminal state of the 'consulta' template.
	consultaStateResolved = "resuelto"
)

// migration is one numbered, append-only step. Its DDL (statements) and its
// data writes (seeds) run in order inside a single transaction together with
// the schema_migrations row that records the step, so a database is never left
// at a half-applied version.
//
// The two lists exist so that product data can NEVER be interpolated into SQL
// text: a DDL statement is static (constants composed at compile time), while
// a seed carries its values as bound arguments. Rider R2-004 (review
// 31ae9e99067acc33) is the reason this split is structural instead of a
// convention — the F1 seed used to concatenate its identifiers straight into
// the INSERT, and the next migration author would have copied the pattern with
// genuinely dynamic data.
type migration struct {
	version int

	// statements are the static DDL statements of the step, run first.
	statements []string

	// seeds are the data statements of the step, run after the DDL: a seed may
	// reference a table created in the same step.
	seeds []seedStatement
}

// seedStatement is one data statement of a migration, with its values bound by
// placeholder. The values live in args, never in sql: there is no seam through
// which a future author could concatenate a dynamic value into the text.
type seedStatement struct {
	sql  string
	args []any
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
		seeds: []seedStatement{
			{sql: seedTicketTypeConsulta, args: []any{TicketTypeConsultaID, TicketTypeConsultaName}},
		},
	},
	{
		// RF4 catalog search (T7): the FTS5 index over every field Hermes needs
		// to answer "¿tenés X?" from a description of the problem rather than
		// from the item's name alone, plus the triggers that keep it in sync
		// with catalog_items.
		//
		// Additive only: the base table landed in step 1, the search surface is
		// an index on top of it, and catalog data is never seeded (the Owner
		// creates it through the RF4 tools).
		version: 3,
		statements: []string{
			ddlCatalogItemsFTS,
			ddlCatalogItemsFTSInsert,
			ddlCatalogItemsFTSUpdate,
			ddlCatalogItemsFTSDelete,
		},
	},
	{
		// Migration 4: the ticket activity index. The list views order by
		// updated_at DESC, id; without this index that order is a full scan plus a
		// sort over every ticket, which is fine at a few thousand rows and wrong at
		// the declared ceiling (100k). Measured at the ceiling with the index: 59ms
		// for either view, instrumented. Additive only.
		//
		// Known limit (R3-coverage-scope follow-up): the single list statement keeps
		// its `(? = '' OR client_id = ?)` disjunct, so SQLite cannot use the index
		// for the client view's seek — splitting the two views into two statements
		// measured 14ms/2.4ms. Deferred deliberately: one statement is the current
		// documented contract, and the index alone already clears the budget.
		version: 4,
		statements: []string{
			ddlTicketsActivityIndex,
		},
	},
	{
		// Migration 5: the configurable workflow of F2 (PRD §3.7, RF6/RF7) and the
		// alert queue (RF11).
		//
		// Additive only, in the order the foreign keys demand: the areas exist
		// before anything references them, the config tables accompany the columns
		// the F1 DDL reserved for them (ticket_types.default_area_id,
		// tickets.area_id/deadline_at/closed_at), and the alert queue arrives with
		// the scheduler skeleton of T6.
		//
		// The seeds are generated from the domain templates (domain.VentaTemplate,
		// domain.ConsultaTemplate) instead of restating the pipelines here: the seed
		// and the engine that executes the config are then the same data, so a
		// template edit cannot leave a shipped database behind. The 'consulta' type
		// already exists (migration 2) and is CONVERTED, not re-created; see
		// f1StateClosed for the state mapping of its existing tickets.
		version: 5,
		statements: []string{
			ddlAreas,
			ddlAreasAccounts,
			ddlTicketTypesDefaultArea,
			ddlTicketTypesInitialState,
			ddlTicketStates,
			ddlTicketTransitions,
			ddlAlerts,
			ddlAlertsPendingIndex,
			ddlTicketsAreaID,
			ddlTicketsDeadlineAt,
			ddlTicketsClosedAt,
		},
		seeds: workflowSeedStatements(),
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
//
// The FTS5 index over name/description/use_cases/limitations is migration 3
// (T7, search_catalog); it is an additive step on top of this table.
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

// ddlCatalogItemsFTS is the RF4 search index (T7) over the four fields that
// carry the item's meaning: name, description, use_cases and limitations.
//
// Shape choice: an EXTERNAL CONTENT FTS5 table (content='catalog_items'), not a
// standalone copy of the text. Rationale, in the order that decided it:
//
//   - One source of truth. catalog_items stays the only place the text lives,
//     so a panel edit and a search result can never diverge, and the index costs
//     only the inverted lists (no duplicate row per item).
//   - The rowid of catalog_items is stable: it is a TEXT-primary-key table, so
//     the implicit rowid survives name/price edits, which is exactly the handle
//     the 'delete' commands of the triggers need.
//   - Ranking with bm25() works identically on an external content table.
//
// tokenize: unicode61 with remove_diacritics 2. Hermes' callers write Spanish
// with or without accents ("corte" vs "corté"), and folding them to one token
// is the difference between a hit and a false negative; unicode61 also handles
// the words as words instead of as bytes (no Porter stemmer, because product
// names are nouns the stemming rules would mangle).
//
// The triggers below are the whole synchronization contract. They are AFTER
// triggers on the base table, so every writer — the RF4 tools today, the panel
// tomorrow — keeps the index correct without knowing the index exists. The
// update trigger fires only when indexed text changed: an is_active or price
// edit must not pay for a reindex of text that did not move.
const ddlCatalogItemsFTS = `
CREATE VIRTUAL TABLE catalog_items_fts USING fts5(
    name,
    description,
    use_cases,
    limitations,
    content='catalog_items',
    content_rowid='rowid',
    tokenize='unicode61 remove_diacritics 2'
)`

// ddlCatalogItemsFTSInsert indexes a new item. NULL use_cases/limitations are
// allowed by the base table and index as empty text, which is what they mean:
// the item says nothing about them.
const ddlCatalogItemsFTSInsert = `
CREATE TRIGGER catalog_items_fts_ai AFTER INSERT ON catalog_items BEGIN
    INSERT INTO catalog_items_fts (rowid, name, description, use_cases, limitations)
    VALUES (new.rowid, new.name, new.description, new.use_cases, new.limitations);
END`

// ddlCatalogItemsFTSUpdate replaces the indexed text of a row. For an external
// content table a re-index is a 'delete' command carrying the OLD values
// followed by the new row: FTS5 needs the old text to remove its tokens from the
// inverted lists, which is why the old values travel in the command instead of
// being read back from the (already overwritten) content table.
const ddlCatalogItemsFTSUpdate = `
CREATE TRIGGER catalog_items_fts_au AFTER UPDATE OF name, description, use_cases, limitations ON catalog_items BEGIN
    INSERT INTO catalog_items_fts (catalog_items_fts, rowid, name, description, use_cases, limitations)
    VALUES ('delete', old.rowid, old.name, old.description, old.use_cases, old.limitations);
    INSERT INTO catalog_items_fts (rowid, name, description, use_cases, limitations)
    VALUES (new.rowid, new.name, new.description, new.use_cases, new.limitations);
END`

// ddlCatalogItemsFTSDelete removes a row's tokens. Same 'delete' command as the
// update trigger: the values of the removed row are the only way FTS5 can drop
// its postings from an external content index.
const ddlCatalogItemsFTSDelete = `
CREATE TRIGGER catalog_items_fts_ad AFTER DELETE ON catalog_items BEGIN
    INSERT INTO catalog_items_fts (catalog_items_fts, rowid, name, description, use_cases, limitations)
    VALUES ('delete', old.rowid, old.name, old.description, old.use_cases, old.limitations);
END`

// ddlTicketTypes is the ticket-type catalogue. F1 seeds exactly one type (the
// seed migration below); F2 turns this into the configurable workflow catalogue
// and adds the 'venta' template.
//
// Columns added later (never by editing this step): default_area_id and
// initial_state, both in migration 5 (F2).
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
// Deferred columns, and why (all three arrived in migration 5, F2):
//   - area_id — the configurable area of the ticket, nullable and cleared when
//     the area is deleted.
//   - deadline_at — the computed deadline of the current state (the scheduler
//     compares one stored instant instead of joining the config per ticket).
//   - closed_at — when the ticket reached a terminal state, which F1 could not
//     know because the vocabulary was fixed.
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

// ddlTicketsActivityIndex serves the list views: both order by
// (updated_at DESC, id), so the index carries exactly that pair. Without it the
// order is a full scan plus a sort, which the declared ceiling (100k tickets)
// turns into a measured regression — see migration 4.
const ddlTicketsActivityIndex = `
CREATE INDEX tickets_activity_idx ON tickets (updated_at, id)`

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
//
// Values are bound, not interpolated (R2-004): the step carries them in
// migration.seeds, so this constant is a plain placeholder statement.
const seedTicketTypeConsulta = `
INSERT INTO ticket_types (id, name) VALUES (?, ?)`

// --- F2: the configurable workflow (migration 5) ---------------------------
//
// The five tables of PRD §3.7 that turn the fixed F1 pipeline into configuration:
// areas (RF7), ticket_states and ticket_transitions (RF6) and the alert queue
// (RF11), plus the four columns F1's DDL reserved for them.
//
// The delete semantics are the §3.7 invariant, not a convenience: an area is
// referenced by links (areas_accounts) and by optional pointers
// (ticket_types.default_area_id, tickets.area_id), so removing an area removes
// its membership rows and clears the pointers — no ticket and no workflow is
// deleted because an area disappeared. The workflow rows (states, transitions)
// belong to their type and die with it: a type without states cannot be operated,
// so keeping orphan states would be a config that fails validation on every read.

// ddlAreas is the RF7 area catalogue: "Áreas de atención configurables —
// nombre, staff miembros (N:M con `accounts`)".
//
// name is UNIQUE because the Owner names areas in conversation ("pasá el ticket
// al área de Soporte") and two areas with one name make that instruction
// ambiguous — the resolver would have to guess, which the product never does.
// The 1..50 bound is length() over the TEXT, which for SQLite counts characters
// and not bytes, so it is the same bound domain.MaxWorkflowNameLen expresses in
// runes.
const ddlAreas = `
CREATE TABLE areas (
    id         TEXT PRIMARY KEY NOT NULL CHECK (length(id) = 36),
    name       TEXT NOT NULL UNIQUE CHECK (length(name) BETWEEN 1 AND 50),
    created_at TEXT NOT NULL DEFAULT (` + timestampDefault + `),
    updated_at TEXT NOT NULL DEFAULT (` + timestampDefault + `)
)`

// ddlAreasAccounts is the N:M membership of RF7 (staff belongs to many areas;
// an area has many staff). The composite primary key IS the set: a duplicate
// assignment cannot be stored, so the "assign" operation is idempotent without a
// read-then-write race.
//
// Both sides cascade: the link is meaningless once either endpoint is gone, and
// it is the only row whose whole content is the pair.
const ddlAreasAccounts = `
CREATE TABLE areas_accounts (
    area_id    TEXT NOT NULL REFERENCES areas (id) ON DELETE CASCADE,
    account_id TEXT NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    PRIMARY KEY (area_id, account_id)
)`

// ddlTicketTypesDefaultArea adds the column F1 deferred to F2 (RF7: "cada tipo de
// ticket declara su área por defecto"). It is nullable because the 'consulta'
// template ships "sin área fija": every area is a queue someone must watch, and
// spreading general consultations over all of them is the documented seed
// behaviour, not a missing value.
//
// ON DELETE SET NULL: deleting an area must not delete the workflow of the type
// that pointed at it — the type simply has no fixed area again.
const ddlTicketTypesDefaultArea = `
ALTER TABLE ticket_types ADD COLUMN default_area_id TEXT REFERENCES areas (id) ON DELETE SET NULL`

// ddlTicketTypesInitialState adds the state a new ticket of the type is born in
// (RF5: "queda en el estado inicial del tipo").
//
// The §3.7 canon rows of ticket_types and ticket_states do not name this column,
// but the workflow entity does: `domain.WorkflowConfig.InitialState` must name a
// declared state, and a config read back from the file has to answer "where does a
// new ticket start?" without inferring it from the transition graph (a type may
// have several roots, and "the one with no incoming transition" is a convention
// nobody configured). It lives on the type, next to the default area, because it is
// a declaration OF THE TYPE — the very phrasing RF5 uses.
//
// Nullable: a type whose config was never saved has no initial state, and a NULL
// reads back as an empty InitialState, which Validate rejects (fail-closed) instead
// of silently starting tickets in a guessed state.
const ddlTicketTypesInitialState = `
ALTER TABLE ticket_types ADD COLUMN initial_state TEXT`

// ddlTicketStates is one state of a ticket type's pipeline (RF6: "Estados por
// tipo — nombre, `deadline_hours` opcional, reglas on-enter/on-exit
// declarativas").
//
//   - position is the declaration order of the state inside the workflow. The
//     domain carries States as an ordered slice and the seed templates are
//     ordered pipelines, so the row needs an explicit order column: relying on
//     rowid would make "the order the Owner declared" an implicit property of the
//     insertion history.
//   - deadline_hours is nullable (a state may have no deadline) and CHECKed as
//     NULL or >= 1, the inclusive lower bound of domain.Validate. The upper bound
//     (one year) is deliberately NOT duplicated here: it is
//     domain.MaxDeadlineHours, a rule with a Spanish rejection message, and a
//     second numeric copy in the DDL would drift silently the day the cap moves.
//   - on_enter_alert / on_exit_alert are the declarative notification rules as
//     integer flags: the engine turns them into pending alerts, it never sends
//     messages (RF11).
//   - UNIQUE (ticket_type_id, name) is the identity the Owner uses: transitions
//     name states by their name inside their type, so two states of one type
//     cannot share it.
const ddlTicketStates = `
CREATE TABLE ticket_states (
    id             TEXT PRIMARY KEY NOT NULL CHECK (length(id) = 36),
    ticket_type_id TEXT NOT NULL REFERENCES ticket_types (id) ON DELETE CASCADE,
    name           TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 50),
    position       INTEGER NOT NULL DEFAULT 0 CHECK (position >= 0),
    deadline_hours INTEGER CHECK (deadline_hours IS NULL OR deadline_hours >= 1),
    on_enter_alert INTEGER NOT NULL DEFAULT 0 CHECK (on_enter_alert IN (0, 1)),
    on_exit_alert  INTEGER NOT NULL DEFAULT 0 CHECK (on_exit_alert IN (0, 1)),
    UNIQUE (ticket_type_id, name)
)`

// ddlTicketTransitions is one valid state change (RF6: "Transiciones válidas —
// estado origen → destino, rol autorizado, auto-assign round-robin opcional").
//
// from_state and to_state carry STATE NAMES, not foreign keys to ticket_states.id:
// the workflow is validated as a whole (WorkflowConfig.Validate rejects an origin
// or destination that is not declared), and a composite foreign key here would
// turn renaming a state into an implicit cascade decision the Owner never made.
// The pair is unique per type, which is what makes "the transition from A to B"
// a single answer, and from_state <> to_state rejects a self-loop at the file
// level because a self-loop is never a transition (Validate rejects it too).
//
// min_role mirrors domain.TransitionRole exactly (staff/admin/owner, ascending).
// 'client' is absent on purpose: a client never executes a transition (RF6
// criterion: a client attempt is rejected FORBIDDEN).
const ddlTicketTransitions = `
CREATE TABLE ticket_transitions (
    id             TEXT PRIMARY KEY NOT NULL CHECK (length(id) = 36),
    ticket_type_id TEXT NOT NULL REFERENCES ticket_types (id) ON DELETE CASCADE,
    from_state     TEXT NOT NULL,
    to_state       TEXT NOT NULL,
    position       INTEGER NOT NULL DEFAULT 0 CHECK (position >= 0),
    min_role       TEXT NOT NULL CHECK (min_role IN ('staff','admin','owner')),
    round_robin    INTEGER NOT NULL DEFAULT 0 CHECK (round_robin IN (0, 1)),
    UNIQUE (ticket_type_id, from_state, to_state),
    CHECK (from_state <> to_state)
)`

// ddlAlerts is the pending-alert queue of RF11: "scheduler in-process ... inserta
// alertas pendientes ... Hermes las recoge con `get_pending_alerts` y las marca
// con `mark_alert_as_sent`. El backend jamás envía mensajes por sí mismo".
//
//   - kind is the closed PRD vocabulary of §3.7. Unlike ticket_events.kind it IS
//     CHECKed: the kinds are a product-level taxonomy (the panel groups them, the
//     PRD lists them) that does not grow with user configuration, so a new kind is
//     a product change and deserves the migration.
//   - client_id / account_id / area_id are the possible targets (§3.7: "target
//     (cliente o account)"; RF6 adds the area: "se genera una alerta `overdue`
//     pendiente para el área asignada"). At least one must be present — an alert
//     nobody can be told about could never be delivered and would sit in the
//     queue forever. All cascade: deleting the target row removes the alerts
//     about it, because an alert is a message TO someone about themselves.
//     (area_id is born with migration 5 instead of waiting for the scheduler:
//     relaxing a CHECK later means rebuilding the table in SQLite — a
//     preventive additive column is the cheap path.)
//   - state is 'pending' until Hermes reports delivery; sent_at records when.
//     "Se entrega una vez" is the pair (state, sent_at), not a delete: RF11 keeps
//     the trace of what was notified.
//   - payload is a JSON object (possibly '{}') carrying what the message needs
//     (ticket id, area name, masked identifier...), json_valid like every other
//     JSON column of the schema.
const ddlAlerts = `
CREATE TABLE alerts (
    id         TEXT PRIMARY KEY NOT NULL CHECK (length(id) = 36),
    kind       TEXT NOT NULL CHECK (kind IN ('overdue','on-enter','on-exit','inactividad','nuevo-ticket','posible-duplicado','seguimiento-lead')),
    client_id  TEXT REFERENCES clients (id) ON DELETE CASCADE,
    account_id TEXT REFERENCES accounts (id) ON DELETE CASCADE,
    area_id    TEXT REFERENCES areas (id) ON DELETE CASCADE,
    state      TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','sent')),
    payload    TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(payload)),
    created_at TEXT NOT NULL DEFAULT (` + timestampDefault + `),
    sent_at    TEXT,
    CHECK (client_id IS NOT NULL OR account_id IS NOT NULL OR area_id IS NOT NULL)
)`

// ddlAlertsPendingIndex serves the one read the queue exists for: "give me the
// pending alerts, oldest first" (RF11 criterion: alerts accumulated while Hermes
// was down arrive in order, without duplicates). Without it that read is a scan
// of every alert ever sent.
const ddlAlertsPendingIndex = `
CREATE INDEX alerts_pending_idx ON alerts (state, created_at, id)`

// ddlTicketsAreaID adds the area of a ticket (PRD §3.7: "`tickets`: Instancia de
// contacto — tipo, cliente, estado actual, área, asignado, deadlines
// computadas").
//
// Nullable: a ticket whose type declares no default area starts with no area, and
// the area can be assigned later. ON DELETE SET NULL for the same reason as the
// type's default: removing an area must never remove tickets.
const ddlTicketsAreaID = `
ALTER TABLE tickets ADD COLUMN area_id TEXT REFERENCES areas (id) ON DELETE SET NULL`

// ddlTicketsDeadlineAt is the computed deadline of the ticket in its current
// state (RF6: "`deadline` por estado con aviso overdue"). It is written when the
// state changes — computed from the state's deadline_hours — so the scheduler
// compares one stored instant instead of a join per ticket, and NULL means "the
// current state has no deadline".
const ddlTicketsDeadlineAt = `
ALTER TABLE tickets ADD COLUMN deadline_at TEXT`

// ddlTicketsClosedAt is when the ticket reached a terminal state. F1 could not
// have it (the vocabulary was fixed and `state` was the only signal); F2 marks
// terminality by configuration, so the instant has to be stored. NULL means the
// conversation is still open — the F1 rows keep it NULL until they move.
const ddlTicketsClosedAt = `
ALTER TABLE tickets ADD COLUMN closed_at TEXT`

// The data statements of migration 5. Every value travels as a bound argument in
// workflowSeedStatements (R2-004): no product data is ever pasted into SQL text.
const (
	seedAreaVentas = `
INSERT INTO areas (id, name) VALUES (?, ?)`

	seedTicketType = `
INSERT INTO ticket_types (id, name, default_area_id, initial_state) VALUES (?, ?, ?, ?)`

	// seedTicketTypeInitialState completes a type that already exists (the F1
	// 'consulta', created by migration 2 before the column existed).
	seedTicketTypeInitialState = `
UPDATE ticket_types SET initial_state = ? WHERE id = ?`

	seedTicketState = `
INSERT INTO ticket_states (id, ticket_type_id, name, position, deadline_hours, on_enter_alert, on_exit_alert)
VALUES (?, ?, ?, ?, ?, ?, ?)`

	seedTicketTransition = `
INSERT INTO ticket_transitions (id, ticket_type_id, from_state, to_state, position, min_role, round_robin)
VALUES (?, ?, ?, ?, ?, ?, ?)`

	// convertConsultaTicketState rewrites the single F1 state whose name the
	// 'consulta' template does not carry (see f1StateClosed).
	convertConsultaTicketState = `
UPDATE tickets SET state = ? WHERE ticket_type_id = ? AND state = ?`
)

// seededStateKind and seededTransitionKind namespace the fixed identifiers of the
// seeded configuration rows, so a state and a transition can never collide on an
// id while each keeps a readable name.
const (
	seededStateKind      = 1
	seededTransitionKind = 2
)

// seededConfigID builds the fixed id of the index-th seeded row of one kind.
//
// Fixed and deterministic on purpose: two databases seeded by the same binary hold
// byte-identical configuration rows, so a backup diff shows data changes and not
// id churn, and the ids stay valid across restore — the same property the F1 seed
// gets from TicketTypeConsultaID.
func seededConfigID(kind, index int) string {
	return fmt.Sprintf("00000000-0000-4000-8%03d-%012d", kind, index)
}

// ticketTypeSeed is one seeded template: the ticket type row it creates (if the
// step creates it at all) and the workflow taken from the domain.
type ticketTypeSeed struct {
	// typeID is the internal UUID of the ticket type.
	typeID string

	// config is the domain template: the single source of the states and
	// transitions this step writes.
	config domain.WorkflowConfig

	// defaultArea is the area the type declares by default, or "" for
	// "sin área fija" (stored as NULL).
	defaultArea string

	// createType tells the seed generator whether the type row itself has to be
	// inserted. The 'consulta' type arrives from migration 2 and is converted, so
	// its row already exists.
	createType bool
}

// seededWorkflowTemplates are the seed templates of PRD §3.7, built from the
// domain so the pipeline the product ships and the pipeline the engine executes
// are the same declaration.
//
// 'venta' links to the 'Ventas' area this migration creates (the domain template
// ships DefaultAreaID empty on purpose: the domain never knows row ids, the
// seeding migration owns the link). 'consulta' ships "sin área fija", which is
// the NULL default_area_id its existing row already carries.
var seededWorkflowTemplates = []ticketTypeSeed{
	{
		typeID:     TicketTypeConsultaID,
		config:     domain.ConsultaTemplate(),
		createType: false,
	},
	{
		typeID:      TicketTypeVentaID,
		config:      domain.VentaTemplate(),
		defaultArea: AreaVentasID,
		createType:  true,
	},
}

// workflowSeedStatements generates the data statements of migration 5 in
// dependency order: the area, the ticket type that points at it, then every
// state and transition of every seeded template, and last the conversion of the
// F1 'consulta' tickets.
//
// Generating the INSERTs from the templates instead of hand-writing one statement
// per row keeps the seed and the engine on one declaration. The generated text
// carries table and column names only — every value is a bound argument, and
// placeholder arity is derived from the argument list itself, so the structural
// check of validateSeeds holds by construction.
func workflowSeedStatements() []seedStatement {
	seeds := []seedStatement{
		{sql: seedAreaVentas, args: []any{AreaVentasID, AreaVentasName}},
	}

	stateIndex, transitionIndex := 0, 0
	for _, template := range seededWorkflowTemplates {
		if template.createType {
			seeds = append(seeds, seedStatement{
				sql: seedTicketType,
				args: []any{
					template.typeID,
					template.config.TypeName,
					nullableText(template.defaultArea),
					nullableText(template.config.InitialState),
				},
			})
		} else {
			// The type row predates this step: only its initial state is missing.
			seeds = append(seeds, seedStatement{
				sql:  seedTicketTypeInitialState,
				args: []any{nullableText(template.config.InitialState), template.typeID},
			})
		}

		for position, state := range template.config.States {
			seeds = append(seeds, seedStatement{
				sql: seedTicketState,
				args: []any{
					seededConfigID(seededStateKind, stateIndex),
					template.typeID,
					state.Name,
					position,
					optionalHours(state.DeadlineHours),
					flagInt(state.OnEnterAlert),
					flagInt(state.OnExitAlert),
				},
			})
			stateIndex++
		}

		for position, transition := range template.config.Transitions {
			seeds = append(seeds, seedStatement{
				sql: seedTicketTransition,
				args: []any{
					seededConfigID(seededTransitionKind, transitionIndex),
					template.typeID,
					transition.From,
					transition.To,
					position,
					string(transition.MinRole),
					flagInt(transition.RoundRobin),
				},
			})
			transitionIndex++
		}
	}

	seeds = append(seeds, seedStatement{
		sql:  convertConsultaTicketState,
		args: []any{consultaStateResolved, TicketTypeConsultaID, f1StateClosed},
	})
	return seeds
}

// nullableText maps the empty string of a carrier to SQL NULL. The two mean
// different things in the schema (” would be an area named nothing, NULL is "no
// area"), and the DDL default is NULL.
func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// optionalHours maps an optional deadline to the column: a state without a
// deadline stores NULL, not 0 (0 is rejected by the CHECK and would read as
// "expires immediately").
func optionalHours(deadline *int) any {
	if deadline == nil {
		return nil
	}
	return *deadline
}

// flagInt maps a declarative rule flag to the integer the DDL stores.
func flagInt(flag bool) any {
	if flag {
		return 1
	}
	return 0
}
