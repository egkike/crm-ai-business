package db

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// openTempDB opens a migrated database in t.TempDir(). Every test gets its own
// file, so no test can see another one's rows and the repository directory is
// never touched.
func openTempDB(t *testing.T) *sql.DB {
	t.Helper()
	handle, err := Open(context.Background(), filepath.Join(t.TempDir(), "crm.db"))
	if err != nil {
		t.Fatalf("Open() error inesperado: %v", err)
	}
	t.Cleanup(func() {
		if err := handle.Close(); err != nil {
			t.Errorf("cerrar la base: %v", err)
		}
	})
	return handle
}

// queryValue scans a single-column, single-row query result into a string.
func queryValue(t *testing.T, handle *sql.DB, query string, args ...any) string {
	t.Helper()
	var value string
	if err := handle.QueryRow(query, args...).Scan(&value); err != nil {
		t.Fatalf("consulta %q: %v", query, err)
	}
	return value
}

// TestOpenRefusesInvalidPath proves Open fails closed before touching the
// filesystem: a rejected path config never creates a file or a directory.
//
// (Rider R2-003 of review 31ae9e99067acc33 moved this test here from
// migrate_test.go: it pins Open's own contract, not the migration runner's, and
// a reader looking for the fail-closed path rule should find it in the file that
// owns path resolution and opening.)
func TestOpenRefusesInvalidPath(t *testing.T) {
	base := t.TempDir()
	directory := filepath.Join(base, "archivo-que-es-directorio")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatalf("preparar el directorio: %v", err)
	}

	for _, invalid := range []string{"crm.db", "   ", "/tmp/crm.db?cache=shared", directory} {
		t.Run(fmt.Sprintf("%q", invalid), func(t *testing.T) {
			if handle, err := Open(context.Background(), invalid); err == nil {
				_ = handle.Close()
				t.Fatal("Open() con una ruta inválida debía fallar (fail-closed)")
			}
		})
	}

	// Nothing was created along the way: the rejected configuration did not get
	// as far as the filesystem.
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatalf("leer el directorio de la prueba: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(directory) {
		t.Fatalf("el directorio de la prueba cambió: %v", entries)
	}
}

// TestOpenFailureLeavesNoDirectory pins rider R3-004: a startup that fails after
// creating the data directory removes what it created, so a refused open never
// leaves an empty database behind for the operator to mistake for state.
func TestOpenFailureLeavesNoDirectory(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, appDirName)
	path := filepath.Join(dir, dbFileName)

	// A cancelled context fails at the first statement the driver runs, which is
	// the natural way to reach the failure path after the directory exists.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	handle, err := Open(ctx, path)
	if err == nil {
		_ = handle.Close()
		t.Fatal("Open() con un contexto cancelado debía fallar")
	}

	if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
		t.Fatalf("el directorio %q sobrevivió a un Open fallido (stat: %v)", dir, statErr)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("el archivo %q sobrevivió a un Open fallido (stat: %v)", path, statErr)
	}
}

// TestOpenFailureKeepsPreexistingState pins the other half of R3-004: cleanup
// removes only what this call created. A database that already existed and
// failed to open is the operator's file, and deleting it would turn a startup
// refusal into data loss.
func TestOpenFailureKeepsPreexistingState(t *testing.T) {
	path := filepath.Join(t.TempDir(), dbFileName)
	if err := os.WriteFile(path, []byte("no soy una base sqlite"), 0o600); err != nil {
		t.Fatalf("preparar el archivo inválido: %v", err)
	}

	if handle, err := Open(context.Background(), path); err == nil {
		_ = handle.Close()
		t.Fatal("Open() debía fallar con un archivo que no es una base")
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("el archivo preexistente debe sobrevivir: %v", err)
	}
}

// TestBuildDSN pins rider R3-001: the connection string is assembled from a
// validated path and encoded parameters, and every path that could change the
// DSN's meaning is refused instead of being pasted in.
func TestBuildDSN(t *testing.T) {
	t.Run("ruta simple y parámetros del contrato", func(t *testing.T) {
		dsn, err := buildDSN("/datos/crm.db", connectionParams())
		if err != nil {
			t.Fatalf("buildDSN() error inesperado: %v", err)
		}
		if !strings.HasPrefix(dsn, "/datos/crm.db?") {
			t.Fatalf("dsn = %q, se esperaba la ruta seguida de la query", dsn)
		}
		for _, want := range []string{"_busy_timeout=5000", "_journal_mode=WAL", "_foreign_keys=ON"} {
			if !strings.Contains(dsn, want) {
				t.Fatalf("dsn = %q, se esperaba %q", dsn, want)
			}
		}
	})

	t.Run("sin parámetros la ruta va sola", func(t *testing.T) {
		dsn, err := buildDSN("/datos/crm.db", url.Values{})
		if err != nil {
			t.Fatalf("buildDSN() error inesperado: %v", err)
		}
		if dsn != "/datos/crm.db" {
			t.Fatalf("dsn = %q, se esperaba solo la ruta", dsn)
		}
	})

	t.Run("rutas que cambian el significado del DSN son rechazadas", func(t *testing.T) {
		for _, invalid := range []string{"", "/datos/crm.db?modo=otro", "/datos/crm#1.db", "/datos/crm\x00.db"} {
			if dsn, err := buildDSN(invalid, connectionParams()); err == nil {
				t.Fatalf("buildDSN(%q) = %q, se esperaba fail-closed", invalid, dsn)
			}
		}
	})

	t.Run("parámetros fuera del contrato son rechazados", func(t *testing.T) {
		cases := map[string]url.Values{
			"sin prefijo":             {"journal_mode": {"WAL"}},
			"con separador en nombre": {"_mode&x": {"WAL"}},
			"con separador en valor":  {"_journal_mode": {"WAL&_foreign_keys=OFF"}},
			"valor vacío":             {"_journal_mode": {""}},
			"sin valor":               {"_journal_mode": {}},
		}
		for name, params := range cases {
			t.Run(name, func(t *testing.T) {
				if dsn, err := buildDSN("/datos/crm.db", params); err == nil {
					t.Fatalf("buildDSN() = %q, se esperaba fail-closed", dsn)
				}
			})
		}
	})
}

// TestOpenAppliesConnectionPragmas pins the AGENTS.md connection contract:
// every pooled connection is opened with WAL, busy_timeout=5000 and foreign
// keys ON, because two of those three are per-connection settings.
func TestOpenAppliesConnectionPragmas(t *testing.T) {
	handle := openTempDB(t)

	if got := queryValue(t, handle, `PRAGMA journal_mode`); got != "wal" {
		t.Errorf("journal_mode = %q, se esperaba wal", got)
	}
	if got := queryValue(t, handle, `PRAGMA busy_timeout`); got != "5000" {
		t.Errorf("busy_timeout = %q, se esperaba 5000", got)
	}
	if got := queryValue(t, handle, `PRAGMA foreign_keys`); got != "1" {
		t.Errorf("foreign_keys = %q, se esperaba 1", got)
	}
}

// TestForeignKeysAreEnforced proves the pragma behaviourally: an orphan FK is
// rejected by the database, not merely reported by PRAGMA.
func TestForeignKeysAreEnforced(t *testing.T) {
	handle := openTempDB(t)

	_, err := handle.Exec(
		`INSERT INTO tickets (id, ticket_type_id, client_id) VALUES (?, ?, ?)`,
		uuidLike(900), TicketTypeConsultaID, uuidLike(901),
	)
	if err == nil {
		t.Fatal("insertar un ticket con client_id inexistente debía fallar (foreign_keys=ON)")
	}
	if !strings.Contains(err.Error(), "FOREIGN KEY") {
		t.Fatalf("error = %v, se esperaba una violación de FOREIGN KEY", err)
	}
}

// TestMigrationsAreIdempotent pins the runner's central property: opening an
// already-migrated database applies nothing and changes nothing.
func TestMigrationsAreIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "crm.db")

	first, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("primer Open() error inesperado: %v", err)
	}
	firstVersions := migrationVersions(t, first)
	firstCount := queryValue(t, first, `SELECT COUNT(*) FROM ticket_types WHERE name = ?`, TicketTypeConsultaName)
	if err := first.Close(); err != nil {
		t.Fatalf("cerrar la base: %v", err)
	}

	second, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("segundo Open() error inesperado: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })

	if got := migrationVersions(t, second); got != firstVersions {
		t.Fatalf("migraciones aplicadas = %v tras reabrir, se esperaba %v (la segunda apertura no aplica nada)", got, firstVersions)
	}
	if got := queryValue(t, second, `SELECT COUNT(*) FROM ticket_types WHERE name = ?`, TicketTypeConsultaName); got != firstCount {
		t.Fatalf("filas del tipo sembrado = %s tras reabrir, se esperaba %s (la semilla no se duplica)", got, firstCount)
	}
}

// TestMigrationsRecordEveryVersion checks the bookkeeping table holds one row
// per known step, in order.
func TestMigrationsRecordEveryVersion(t *testing.T) {
	handle := openTempDB(t)

	want := fmt.Sprintf("%d", len(migrations))
	if got := queryValue(t, handle, `SELECT COUNT(*) FROM schema_migrations`); got != want {
		t.Fatalf("filas en schema_migrations = %s, se esperaban %s", got, want)
	}
	if got := queryValue(t, handle, `SELECT applied_at FROM schema_migrations ORDER BY version LIMIT 1`); !looksLikeTimestamp(got) {
		t.Fatalf("applied_at = %q, se esperaba un timestamp ISO-8601 UTC", got)
	}
}

// TestMigrateRefusesUnknownVersion is the forward-incompatibility arm of the
// fail-closed rule: a file migrated by a newer binary must not be opened by an
// older one.
func TestMigrateRefusesUnknownVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "crm.db")
	seedSchemaWith(t, path, `INSERT INTO schema_migrations (version, applied_at) VALUES (999, '2026-01-01T00:00:00.000Z')`)

	if _, err := Open(context.Background(), path); err == nil {
		t.Fatal("abrir una base con una migración desconocida debía fallar (fail-closed)")
	}
}

// TestMigrateRefusesAppliedSequenceHole is the corruption arm: a missing
// version below the highest applied one means the schema is not a prefix.
func TestMigrateRefusesAppliedSequenceHole(t *testing.T) {
	path := filepath.Join(t.TempDir(), "crm.db")
	seedSchemaWith(t, path, `DELETE FROM schema_migrations WHERE version = 1`)

	if _, err := Open(context.Background(), path); err == nil {
		t.Fatal("abrir una base con un hueco en la secuencia debía fallar (fail-closed)")
	}
}

// TestValidateMigrationsRejectsProgrammerErrors covers the compiled-in
// sequence: holes, duplicates and empty steps are startup refusals, never a
// half-applied schema.
func TestValidateMigrationsRejectsProgrammerErrors(t *testing.T) {
	ok := func() migration {
		return migration{version: 0, statements: []string{"SELECT 1"}}
	}

	t.Run("secuencia válida", func(t *testing.T) {
		a, b := ok(), ok()
		a.version, b.version = 1, 2
		if err := validateMigrations([]migration{a, b}); err != nil {
			t.Fatalf("validateMigrations() error inesperado: %v", err)
		}
	})

	t.Run("hueco", func(t *testing.T) {
		a, c := ok(), ok()
		a.version, c.version = 1, 3
		if err := validateMigrations([]migration{a, c}); err == nil {
			t.Fatal("una secuencia con hueco debía fallar")
		}
	})

	t.Run("duplicada", func(t *testing.T) {
		a, b := ok(), ok()
		a.version, b.version = 1, 1
		if err := validateMigrations([]migration{a, b}); err == nil {
			t.Fatal("una secuencia con versión duplicada debía fallar")
		}
	})

	t.Run("empieza en cero", func(t *testing.T) {
		z := ok()
		if err := validateMigrations([]migration{z}); err == nil {
			t.Fatal("una secuencia que no empieza en 1 debía fallar")
		}
	})

	t.Run("vacía", func(t *testing.T) {
		if err := validateMigrations(nil); err == nil {
			t.Fatal("una secuencia vacía debía fallar")
		}
	})

	t.Run("sin sentencias", func(t *testing.T) {
		if err := validateMigrations([]migration{{version: 1}}); err == nil {
			t.Fatal("una migración sin sentencias debía fallar")
		}
	})
}

// TestResolvePath pins the fail-closed configuration rule of EnvPath, mirroring
// the composition root's MCP_PORT behaviour.
func TestResolvePath(t *testing.T) {
	t.Run("vacío usa el default XDG", func(t *testing.T) {
		t.Setenv(envXDGDataHome, "/datos/xdg")
		got, err := ResolvePath("")
		if err != nil {
			t.Fatalf("ResolvePath(\"\") error inesperado: %v", err)
		}
		want := filepath.Join("/datos/xdg", appDirName, dbFileName)
		if got != want {
			t.Fatalf("ResolvePath(\"\") = %q, se esperaba %q", got, want)
		}
	})

	t.Run("XDG relativo se ignora (especificación XDG)", func(t *testing.T) {
		t.Setenv(envXDGDataHome, "relativo/xdg")
		t.Setenv("HOME", "/home/prueba")
		got, err := ResolvePath("")
		if err != nil {
			t.Fatalf("ResolvePath(\"\") error inesperado: %v", err)
		}
		want := filepath.Join("/home/prueba", ".local", "share", appDirName, dbFileName)
		if got != want {
			t.Fatalf("ResolvePath(\"\") = %q, se esperaba el fallback %q", got, want)
		}
	})

	t.Run("absoluto válido pasa", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "otra.db")
		got, err := ResolvePath(path)
		if err != nil || got != path {
			t.Fatalf("ResolvePath(%q) = %q, %v; se esperaba la misma ruta", path, got, err)
		}
	})

	dir := t.TempDir()
	for _, raw := range []string{
		"   ",
		"crm.db",
		"./crm.db",
		"/tmp/crm.db?cache=shared",
		"/tmp/crm#1.db",
		"/tmp/crm\x00.db",
		dir,
	} {
		t.Run("inválido "+fmt.Sprintf("%q", raw), func(t *testing.T) {
			if _, err := ResolvePath(raw); err == nil {
				t.Fatalf("ResolvePath(%q) = nil error; se espera fail-closed", raw)
			}
		})
	}
}

// seedSchemaWith creates the database at path, applies the real migrations
// through Open, then runs extra SQL and closes it, so a follow-up Open sees a
// deliberately broken schema.
func seedSchemaWith(t *testing.T, path, extraSQL string) {
	t.Helper()
	handle, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open() inicial error inesperado: %v", err)
	}
	if _, err := handle.Exec(extraSQL); err != nil {
		_ = handle.Close()
		t.Fatalf("preparar esquema roto (%q): %v", extraSQL, err)
	}
	if err := handle.Close(); err != nil {
		t.Fatalf("cerrar la base preparada: %v", err)
	}
}

// migrationVersions returns the applied versions as a comparable string.
func migrationVersions(t *testing.T, handle *sql.DB) string {
	t.Helper()
	return queryValue(t, handle, `SELECT group_concat(version, ',') FROM (SELECT version FROM schema_migrations ORDER BY version)`)
}

// uuidLike builds a valid UUID-shaped identifier for fixtures (the DDL enforces
// the 36-character shape).
func uuidLike(n int) string {
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", n)
}

// looksLikeTimestamp reports whether value matches the DDL's
// strftime('%Y-%m-%dT%H:%M:%fZ') shape.
func looksLikeTimestamp(value string) bool {
	if !strings.HasSuffix(value, "Z") {
		return false
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && parsed.Location() == time.UTC
}
