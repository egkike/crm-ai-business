// Package db owns the canonical SQLite schema of CRM-AI-Business, the
// connection factory and the versioned migration runner (AGENTS.md: "schema
// changes only via the canonical DDL in internal/db").
//
// # Scope: F1 and the F2 workflow configuration
//
// The schema in this package is exactly what the shipped consumers use. Later
// phases add their tables and columns as NEW numbered migrations in schema.go —
// never by editing an already-applied step (append-only, see migrate.go).
//
// Migration 5 (F2) added the configurable workflow of PRD §3.7: areas and their
// N:M membership with accounts, ticket_states and ticket_transitions per ticket
// type, the alert queue, and the four columns on `tickets` the F1 DDL had
// reserved (area_id, deadline_at, closed_at) plus the type-level declarations
// (default_area_id, initial_state). The seed templates 'venta' and 'consulta' come
// from internal/domain, so the pipeline shipped and the pipeline executed are one
// declaration.
//
// The following are deliberately absent and arrive with their phase:
//
//   - schedules, hours_exceptions, bookings — F3 (bookings).
//   - sessions — F4 (the panel's server-side sessions).
//
// `tickets.state` stays a free TEXT column: the states are rows in ticket_states
// now, and a CHECK here would demand a migration per configured state. The
// DEFAULT ('nuevo') is the F1 initial state and stays for the rows written by the
// F1 paths; the engine writes the configured initial state of the type.
//
// The FTS5 search index over catalog_items is NOT deferred any more: migration
// 3 (T7, RF4) creates it as an external-content virtual table kept in sync by
// triggers, so search_catalog matches the problem the client describes and not
// just the item's name.//
// Per-column deferrals are documented next to the DDL that owns the table, so
// the next migration author knows exactly where to resume.
//
// # Invariants encoded in the DDL
//
//   - Internal UUID primary keys (TEXT, 36 characters) for every business
//     entity. Natural identifiers (phone, email) are nullable UNIQUE columns,
//     never foreign-key material (PRD §3.7).
//   - Natural identifiers are stored verbatim: no normalization is applied to
//     phone numbers, and e-mail uniqueness is expressed over lower(email) so
//     the resolver's case-insensitive match is the same comparison that the
//     index enforces.
//   - Enum-like columns carry CHECK constraints restricted to the PRD's
//     vocabulary, so a bad value is rejected by the file, not only by Go.
//   - Timestamps are TEXT in ISO-8601 UTC (strftime('%Y-%m-%dT%H:%M:%fZ')),
//     which sorts lexicographically. `updated_at` is maintained by the writer
//     (no trigger): a trigger would mutate rows behind the audit trail of
//     ticket_events.
//
// # Connection contract
//
// Open applies journal_mode=WAL, busy_timeout=5000 and foreign_keys=ON to
// every pooled connection through the driver DSN (AGENTS.md "Concurrency &
// SQLite"), refuses invalid configuration fail-closed (mirroring the
// composition root's MCP_PORT rule) and runs the pending migrations before
// returning the handle.
package db
