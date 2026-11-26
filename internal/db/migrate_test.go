package db

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// TestApplyMigrationIsAtomic pins the runner's transaction boundary: a step
// that fails halfway (here, a statement that collides with an existing table)
// leaves neither its DDL nor its schema_migrations row behind, so a retry
// starts from the same version.
func TestApplyMigrationIsAtomic(t *testing.T) {
	handle := openTempDB(t)

	broken := migration{
		version: len(migrations) + 1,
		statements: []string{
			`CREATE TABLE prueba_atomica (id INTEGER PRIMARY KEY)`,
			`CREATE TABLE accounts (id TEXT)`,
		},
	}

	err := applyMigration(context.Background(), handle, broken)
	if err == nil {
		t.Fatal("applyMigration() debía fallar con la segunda sentencia inválida")
	}
	if strings.Contains(err.Error(), "CREATE TABLE accounts (id TEXT)") {
		t.Fatalf("err = %v, el mensaje no debe volcar la DDL completa", err)
	}
	if !strings.Contains(err.Error(), "CREATE TABLE accounts") {
		t.Fatalf("err = %v, se esperaba la etiqueta corta de la sentencia que falló", err)
	}

	if got := queryValue(t, handle, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, "prueba_atomica"); got != "0" {
		t.Fatalf("la tabla parcial de una migración fallida sobrevivió (count=%s): la transacción no revirtió", got)
	}
	if got := queryValue(t, handle, `SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, broken.version); got != "0" {
		t.Fatalf("la versión fallida quedó registrada (count=%s)", got)
	}
}

// TestStatementName pins the short DDL label used by migration errors.
func TestStatementName(t *testing.T) {
	cases := map[string]string{
		"":                                 "sentencia vacía",
		"   \n\t ":                         "sentencia vacía",
		"CREATE TABLE accounts (id TEXT)":  "CREATE TABLE accounts",
		"CREATE UNIQUE INDEX idx ON t (c)": "CREATE UNIQUE INDEX",
		"INSERT INTO ticket_types VALUES":  "INSERT",
		"DElEtE FROM schema_migrations":    "DElEtE",
	}
	for statement, want := range cases {
		if got := statementName(statement); got != want {
			t.Errorf("statementName(%q) = %q, se esperaba %q", statement, got, want)
		}
	}
}

// TestValidateMigrationsRejectsEmptyStep pins that a step with neither DDL nor
// data is a startup refusal: it would record a version that changed nothing.
func TestValidateMigrationsRejectsEmptyStep(t *testing.T) {
	if err := validateMigrations([]migration{{version: 1}}); err == nil {
		t.Fatal("una migración sin sentencias debía fallar")
	}
}

// TestValidateSeedsPinsPlaceholderArity pins the structural half of rider
// R2-004: a data statement must bind exactly one value per placeholder, so a
// seed can never interpolate a value into its SQL or forget an argument.
func TestValidateSeedsPinsPlaceholderArity(t *testing.T) {
	t.Run("mismo número de placeholders y valores", func(t *testing.T) {
		step := migration{version: 1, seeds: []seedStatement{{sql: `INSERT INTO t (a, b) VALUES (?, ?)`, args: []any{1, 2}}}}
		if err := validateMigrations([]migration{step}); err != nil {
			t.Fatalf("validateMigrations() error inesperado: %v", err)
		}
	})

	t.Run("falta un valor", func(t *testing.T) {
		step := migration{version: 1, seeds: []seedStatement{{sql: `INSERT INTO t (a, b) VALUES (?, ?)`, args: []any{1}}}}
		if err := validateMigrations([]migration{step}); err == nil {
			t.Fatal("una sentencia de datos con un placeholder sin valor debía fallar")
		}
	})

	t.Run("valor de más", func(t *testing.T) {
		step := migration{version: 1, seeds: []seedStatement{{sql: `INSERT INTO t (a) VALUES (?)`, args: []any{1, 2}}}}
		if err := validateMigrations([]migration{step}); err == nil {
			t.Fatal("una sentencia de datos con un valor sin placeholder debía fallar")
		}
	})

	t.Run("sentencia vacía", func(t *testing.T) {
		step := migration{version: 1, seeds: []seedStatement{{sql: "   "}}}
		if err := validateMigrations([]migration{step}); err == nil {
			t.Fatal("una sentencia de datos vacía debía fallar")
		}
	})
}

// TestPendingMigrationsPinsPrefix pins the arithmetic of the MAX/COUNT summary:
// a database that records versions 1..N resumes at N+1, and the applied set is
// never a subset.
func TestPendingMigrationsPinsPrefix(t *testing.T) {
	steps := []migration{{version: 1}, {version: 2}, {version: 3}}

	t.Run("base vacía aplica todo", func(t *testing.T) {
		pending, err := pendingMigrations(appliedState{}, steps)
		if err != nil {
			t.Fatalf("pendingMigrations() error inesperado: %v", err)
		}
		if len(pending) != len(steps) {
			t.Fatalf("pendientes = %d, se esperaban %d", len(pending), len(steps))
		}
	})

	t.Run("prefijo completo no aplica nada", func(t *testing.T) {
		pending, err := pendingMigrations(appliedState{count: 3, highest: 3}, steps)
		if err != nil {
			t.Fatalf("pendingMigrations() error inesperado: %v", err)
		}
		if len(pending) != 0 {
			t.Fatalf("pendientes = %d, se esperaba 0", len(pending))
		}
	})

	t.Run("prefijo parcial reanuda en la versión siguiente", func(t *testing.T) {
		pending, err := pendingMigrations(appliedState{count: 2, highest: 2}, steps)
		if err != nil {
			t.Fatalf("pendingMigrations() error inesperado: %v", err)
		}
		if len(pending) != 1 || pending[0].version != 3 {
			t.Fatalf("pendientes = %+v, se esperaba solo la versión 3", pending)
		}
	})

	t.Run("versión desconocida es rechazada", func(t *testing.T) {
		if _, err := pendingMigrations(appliedState{count: 4, highest: 4}, steps); err == nil {
			t.Fatal("una base migrada por un binario más nuevo debía rechazarse")
		}
	})

	t.Run("hueco es rechazado", func(t *testing.T) {
		if _, err := pendingMigrations(appliedState{count: 2, highest: 3}, steps); err == nil {
			t.Fatal("un hueco en la secuencia aplicada debía rechazarse")
		}
	})
}

// TestAppliedVersionsMatchesRows pins the equivalence rider R3-003 bets on: the
// MAX/COUNT summary must report exactly what the rows say, so the collapse from
// "load every version" to one aggregate changes nothing observable.
func TestAppliedVersionsMatchesRows(t *testing.T) {
	handle := openTempDB(t)

	state, err := appliedVersions(context.Background(), handle)
	if err != nil {
		t.Fatalf("appliedVersions() error inesperado: %v", err)
	}

	rows := queryValue(t, handle, `SELECT COUNT(*) FROM schema_migrations`)
	highest := queryValue(t, handle, `SELECT MAX(version) FROM schema_migrations`)
	if got := fmt.Sprint(state.count); got != rows {
		t.Fatalf("count = %s, las filas dicen %s", got, rows)
	}
	if got := fmt.Sprint(state.highest); got != highest {
		t.Fatalf("highest = %s, MAX(version) dice %s", got, highest)
	}
	if state.count != len(migrations) || state.highest != len(migrations) {
		t.Fatalf("estado = %+v, se esperaba 1..%d aplicado", state, len(migrations))
	}
}
