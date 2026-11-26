package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// migrationsTableDDL is the bookkeeping table of applied steps. It is created
// before the first migration runs and is not itself a migration: it has to
// exist to know what has been applied.
const migrationsTableDDL = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version    INTEGER PRIMARY KEY NOT NULL,
    applied_at TEXT NOT NULL
)`

// migrate applies every pending migration of the known sequence, in order.
//
// Policy (fail-closed, append-only):
//
//   - A database that records a version this binary does not know is refused:
//     the schema is ahead of the binary, and running older code against a newer
//     file is exactly the rollback hazard of PRD §3.6.
//   - A database that skipped a version is refused: the sequence is a prefix,
//     never a subset, so a gap means corruption or a hand-edited file.
//   - Each step runs in its own transaction together with the row that records
//     it, so a database is never left half-migrated.
//   - There are no down-migrations in v1. Reverting a phase is "restore the
//     pre-upgrade file" (the covenant of PRD §3.6), which is only safe while
//     migrations stay additive; a hand-written down-migration would silently
//     drop columns an older binary expects to keep.
//
// Operational note: two processes migrating the same file at the same moment
// collide on the DDL and the loser refuses to start. That is fail-closed and
// leaves no partial state (each step is transactional), and it is the intended
// shape for a product whose deployment is one binary per database.
func migrate(ctx context.Context, handle *sql.DB) error {
	if err := validateMigrations(migrations); err != nil {
		return err
	}

	if _, err := handle.ExecContext(ctx, migrationsTableDDL); err != nil {
		return fmt.Errorf("crear schema_migrations: %w", err)
	}

	applied, err := appliedVersions(ctx, handle)
	if err != nil {
		return err
	}

	pending, err := pendingMigrations(applied, migrations)
	if err != nil {
		return err
	}

	for _, step := range pending {
		if err := applyMigration(ctx, handle, step); err != nil {
			return err
		}
	}
	return nil
}

// validateMigrations proves the compiled-in sequence is contiguous and starts
// at 1. A gap here is a programming error, not an operator error, and is
// reported as a startup refusal so it can never reach a database.
func validateMigrations(steps []migration) error {
	if len(steps) == 0 {
		return errors.New("migraciones: la secuencia está vacía")
	}
	for i, step := range steps {
		want := i + 1
		if step.version != want {
			return fmt.Errorf("migraciones: la versión %d está fuera de secuencia (se esperaba %d sin huecos)", step.version, want)
		}
		if len(step.statements) == 0 {
			return fmt.Errorf("migraciones: la versión %d no tiene sentencias DDL", step.version)
		}
	}
	return nil
}

// appliedVersions returns the recorded versions in ascending order.
func appliedVersions(ctx context.Context, handle *sql.DB) ([]int, error) {
	rows, err := handle.QueryContext(ctx, `SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		return nil, fmt.Errorf("leer schema_migrations: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var versions []int
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			return nil, fmt.Errorf("leer schema_migrations: %w", err)
		}
		versions = append(versions, version)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("leer schema_migrations: %w", err)
	}
	return versions, nil
}

// pendingMigrations returns the steps that still have to run, refusing a
// schema that is unknown or has a hole below the highest applied version.
// (A duplicated version cannot occur: the bookkeeping table's primary key is
// the version itself.)
func pendingMigrations(applied []int, steps []migration) ([]migration, error) {
	known := make(map[int]struct{}, len(steps))
	for _, step := range steps {
		known[step.version] = struct{}{}
	}

	seen := make(map[int]struct{}, len(applied))
	for _, version := range applied {
		if _, ok := known[version]; !ok {
			return nil, fmt.Errorf("esquema desconocido: la base registra la migración %d, que este binario no conoce; se niega a arrancar (fail-closed)", version)
		}
		seen[version] = struct{}{}
	}

	// The applied set must be the prefix 1..max: a hole below the highest
	// version means a step was skipped and every later step is suspect.
	highest := 0
	for _, version := range applied {
		if version > highest {
			highest = version
		}
	}
	for version := 1; version <= highest; version++ {
		if _, ok := seen[version]; !ok {
			return nil, fmt.Errorf("esquema inválido: falta la migración %d con la %d ya aplicada", version, highest)
		}
	}

	var pending []migration
	for _, step := range steps {
		if _, ok := seen[step.version]; !ok {
			pending = append(pending, step)
		}
	}
	return pending, nil
}

// applyMigration runs one step atomically: its DDL and the schema_migrations
// row commit together or not at all.
func applyMigration(ctx context.Context, handle *sql.DB, step migration) error {
	tx, err := handle.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("migración %d: iniciar transacción: %w", step.version, err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, statement := range step.statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("migración %d: %s: %w", step.version, statementName(statement), err)
		}
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, applied_at) VALUES (?, `+timestampDefault+`)`,
		step.version,
	); err != nil {
		return fmt.Errorf("migración %d: registrar versión: %w", step.version, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("migración %d: confirmar: %w", step.version, err)
	}
	return nil
}

// statementName derives a short label from a DDL statement for error messages:
// the raw statement can be long, and the SQL text must not be echoed to the
// caller (AGENTS.md boundary contract 2 — this error is internal, but keeping
// DDL out of it costs nothing).
func statementName(statement string) string {
	fields := strings.Fields(statement)
	if len(fields) == 0 {
		return "sentencia vacía"
	}
	if len(fields) >= 3 && strings.EqualFold(fields[0], "CREATE") {
		return strings.Join(fields[:3], " ")
	}
	return fields[0]
}
