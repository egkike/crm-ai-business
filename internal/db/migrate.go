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
// at 1, and that every step carries at least one statement with its arguments
// matching its placeholders. A failure here is a programming error, not an
// operator error, and is reported as a startup refusal so it can never reach a
// database.
func validateMigrations(steps []migration) error {
	if len(steps) == 0 {
		return errors.New("migraciones: la secuencia está vacía")
	}
	for i, step := range steps {
		want := i + 1
		if step.version != want {
			return fmt.Errorf("migraciones: la versión %d está fuera de secuencia (se esperaba %d sin huecos)", step.version, want)
		}
		if len(step.statements) == 0 && len(step.seeds) == 0 {
			return fmt.Errorf("migraciones: la versión %d no tiene sentencias DDL", step.version)
		}
		if err := validateSeeds(step); err != nil {
			return err
		}
	}
	return nil
}

// validateSeeds proves every data statement of a step binds exactly one value
// per placeholder. It is the structural half of R2-004: a seed that forgot an
// argument, or that pasted a value into its SQL, is refused at startup instead
// of failing on the operator's database.
func validateSeeds(step migration) error {
	for _, seed := range step.seeds {
		if strings.TrimSpace(seed.sql) == "" {
			return fmt.Errorf("migraciones: la versión %d tiene una sentencia de datos vacía", step.version)
		}
		placeholders := strings.Count(seed.sql, "?")
		if placeholders != len(seed.args) {
			return fmt.Errorf("migraciones: la versión %d tiene una sentencia de datos con %d placeholders y %d valores", step.version, placeholders, len(seed.args))
		}
	}
	return nil
}

// appliedState is the bookkeeping summary of a database: how many versions it
// records and the highest of them (R3-003 — one aggregate query instead of
// loading every row).
//
// Two integers answer everything the runner asks. Versions are the primary key
// of schema_migrations, so a consistent database that records version N holds
// exactly N rows: count == highest means the applied set is precisely 1..N, and
// any other relation means a hole or a hand-edited file.
type appliedState struct {
	count   int
	highest int
}

// appliedVersions reads the bookkeeping summary in a single query.
func appliedVersions(ctx context.Context, handle *sql.DB) (appliedState, error) {
	var state appliedState
	if err := handle.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(MAX(version), 0) FROM schema_migrations`,
	).Scan(&state.count, &state.highest); err != nil {
		return appliedState{}, fmt.Errorf("leer schema_migrations: %w", err)
	}
	return state, nil
}

// pendingMigrations returns the steps that still have to run, refusing a
// schema that is unknown or has a hole below the highest applied version.
//
// Precondition: the compiled-in sequence is exactly 1..N (validateMigrations
// runs before this), so the highest applied version is also the slice index of
// the first pending step.
func pendingMigrations(applied appliedState, steps []migration) ([]migration, error) {
	known := len(steps)

	// A version this binary does not know: the schema is ahead of the binary.
	if applied.highest > known {
		return nil, fmt.Errorf("esquema desconocido: la base registra la migración %d, que este binario no conoce; se niega a arrancar (fail-closed)", applied.highest)
	}

	// The applied set must be the prefix 1..highest. A count below the highest
	// version means a step was skipped (or the table was hand-edited) and every
	// later step is suspect; a count above it is impossible for distinct
	// positive versions, so the same refusal covers a corrupted file.
	if applied.count != applied.highest {
		return nil, fmt.Errorf("esquema inválido: la base registra %d migraciones con la versión máxima %d (la secuencia debe ser 1..N sin huecos)", applied.count, applied.highest)
	}

	return steps[applied.highest:], nil
}

// applyMigration runs one step atomically: its DDL, its data statements and the
// schema_migrations row commit together or not at all.
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

	for _, seed := range step.seeds {
		if _, err := tx.ExecContext(ctx, seed.sql, seed.args...); err != nil {
			return fmt.Errorf("migración %d: %s: %w", step.version, statementName(seed.sql), err)
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
