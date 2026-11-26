package db

import (
	"context"
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

// TestOpenRefusesInvalidPath proves Open fails closed before touching the
// filesystem: a rejected path config never creates a file or a directory.
func TestOpenRefusesInvalidPath(t *testing.T) {
	if _, err := Open(context.Background(), "crm.db"); err == nil {
		t.Fatal("Open() con una ruta relativa debía fallar (fail-closed)")
	}
}
