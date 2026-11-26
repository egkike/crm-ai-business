// Package db owns the canonical SQLite schema of CRM-AI-Business, the
// connection factory and the versioned migration runner (AGENTS.md: "schema
// changes only via the canonical DDL in internal/db").
//
// # Scope: F1 only (option B)
//
// The schema in this package is exactly what F1's consumers use. Later phases
// add their tables and columns as NEW numbered migrations in schema.go — never
// by editing an already-applied step (append-only, see migrate.go). The
// following are deliberately absent and arrive with their phase:
//
//   - areas, ticket_states, ticket_transitions, schedules, hours_exceptions —
//     F2 (workflow engine) and F3 (bookings). Until then a ticket carries a
//     plain `state` text column with the seeded initial state 'nuevo'.
//   - bookings, alerts, sessions — F3, F2 and F4 respectively.
//
// The FTS5 search index over catalog_items is NOT deferred any more: migration
// 3 (T7, RF4) creates it as an external-content virtual table kept in sync by
// triggers, so search_catalog matches the problem the client describes and not
// just the item's name.
//
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
