package repository_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egkike/crm-ai-business/internal/auth"
	"github.com/egkike/crm-ai-business/internal/db"
	"github.com/egkike/crm-ai-business/internal/repository"
)

// openDB opens a real, migrated SQLite database inside t.TempDir(). The tests
// deliberately run against the product schema: an adapter tested against a
// hand-made table would prove nothing about the schema the product ships.
func openDB(t *testing.T) *sql.DB {
	t.Helper()
	handle, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "crm.db"))
	if err != nil {
		t.Fatalf("db.Open() error inesperado: %v", err)
	}
	t.Cleanup(func() {
		if err := handle.Close(); err != nil {
			t.Errorf("cerrar la base: %v", err)
		}
	})
	return handle
}

// newLookupOf wires the adapter over handle.
func newLookupOf(t *testing.T, handle *sql.DB) *repository.CallerLookup {
	t.Helper()
	lookup, err := repository.NewCallerLookup(handle)
	if err != nil {
		t.Fatalf("NewCallerLookup() error inesperado: %v", err)
	}
	return lookup
}

// account is the fixture shape of an accounts row.
type account struct {
	id     string
	phone  string // "" is stored as NULL
	email  string
	role   string
	active bool
	name   string
}

// client is the fixture shape of a clients row.
type client struct {
	id    string
	phone string // "" is stored as NULL
	email string // "" is stored as NULL
	name  string
}

// seedAccount inserts acc.
func seedAccount(t *testing.T, handle *sql.DB, acc account) {
	t.Helper()
	active := 0
	if acc.active {
		active = 1
	}
	_, err := handle.Exec(
		`INSERT INTO accounts (id, phone, email, role, is_active, display_name) VALUES (?, ?, ?, ?, ?, ?)`,
		acc.id, nullable(acc.phone), acc.email, acc.role, active, acc.name,
	)
	if err != nil {
		t.Fatalf("sembrar cuenta %s: %v", acc.id, err)
	}
}

// seedClient inserts cli.
func seedClient(t *testing.T, handle *sql.DB, cli client) {
	t.Helper()
	_, err := handle.Exec(
		`INSERT INTO clients (id, phone, email, display_name) VALUES (?, ?, ?, ?)`,
		cli.id, nullable(cli.phone), nullable(cli.email), cli.name,
	)
	if err != nil {
		t.Fatalf("sembrar cliente %s: %v", cli.id, err)
	}
}

// nullable maps the fixture's "" to SQL NULL: natural identifiers are nullable
// columns, and an empty string is not a valid identifier.
func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// uuid builds a UUID-shaped fixture id (the DDL enforces the 36-char shape).
func uuid(n int) string {
	return fmt.Sprintf("11111111-1111-4111-8111-%012d", n)
}

// TestNewCallerLookupRejectsNilHandle pins the fail-closed construction rule.
func TestNewCallerLookupRejectsNilHandle(t *testing.T) {
	lookup, err := repository.NewCallerLookup(nil)
	if err == nil {
		t.Fatal("NewCallerLookup(nil) debía fallar (fail-closed)")
	}
	if lookup != nil {
		t.Fatalf("NewCallerLookup(nil) devolvió un adaptador utilizable: %+v", lookup)
	}
}

// TestLookupAccounts pins the internal identity resolution: the role comes from
// accounts.role verbatim, the name from display_name and Enabled from
// is_active.
func TestLookupAccounts(t *testing.T) {
	handle := openDB(t)
	cases := []account{
		{id: uuid(1), phone: "+5491100000001", email: "duenia@example.com", role: "owner", active: true, name: "Dueña"},
		{id: uuid(2), phone: "+5491100000002", email: "admin@example.com", role: "admin", active: true, name: "Admin"},
		{id: uuid(3), phone: "+5491100000003", email: "staff@example.com", role: "staff", active: true, name: "Staff"},
	}
	for _, acc := range cases {
		seedAccount(t, handle, acc)
	}
	lookup := newLookupOf(t, handle)

	for _, acc := range cases {
		t.Run("teléfono "+acc.role, func(t *testing.T) {
			identity, err := lookup.LookupCaller(context.Background(), acc.phone)
			if err != nil {
				t.Fatalf("LookupCaller(%q) error inesperado: %v", acc.phone, err)
			}
			want := auth.Identity{ID: acc.id, Name: acc.name, Role: auth.Role(acc.role), Enabled: true}
			if identity != want {
				t.Fatalf("identidad = %+v, se esperaba %+v", identity, want)
			}
		})
	}

	t.Run("email en cualquier capitalización", func(t *testing.T) {
		for _, callerID := range []string{
			"staff@example.com",
			"STAFF@EXAMPLE.COM",
			"Staff@Example.Com",
		} {
			identity, err := lookup.LookupCaller(context.Background(), callerID)
			if err != nil {
				t.Fatalf("LookupCaller(%q) error inesperado: %v", callerID, err)
			}
			if identity.Role != auth.RoleStaff || identity.ID != uuid(3) {
				t.Fatalf("identidad para %q = %+v, se esperaba el staff %s", callerID, identity, uuid(3))
			}
		}
	})
}

// TestLookupPhoneIsByteExact pins the anti-normalization invariant of PRD
// §3.8: the phone is compared verbatim, so a normalized variant of the same
// number is a different (unknown) caller.
func TestLookupPhoneIsByteExact(t *testing.T) {
	handle := openDB(t)
	const verbatim = "+54 9 11 4444-0001"
	seedAccount(t, handle, account{id: uuid(10), phone: verbatim, email: "verbatim@example.com", role: "staff", active: true, name: "Staff"})
	lookup := newLookupOf(t, handle)

	if _, err := lookup.LookupCaller(context.Background(), verbatim); err != nil {
		t.Fatalf("LookupCaller(%q) error inesperado: %v", verbatim, err)
	}

	for _, normalized := range []string{
		"+5491144440001",
		"+54 9 11 4444-0001 ",
		" 5491144440001",
		"+54-9-11-4444-0001",
	} {
		if _, err := lookup.LookupCaller(context.Background(), normalized); !errors.Is(err, auth.ErrCallerNotFound) {
			t.Fatalf("LookupCaller(%q) err = %v, se esperaba ErrCallerNotFound (el teléfono no se normaliza)", normalized, err)
		}
	}
}

// TestLookupInactiveAccountIsReportedAsDisabled pins the adapter's
// responsibility boundary: it reports what the row says (Enabled=false) and
// never decides the rejection itself — the resolver turns it into
// ErrAccountDisabled.
func TestLookupInactiveAccountIsReportedAsDisabled(t *testing.T) {
	handle := openDB(t)
	seedAccount(t, handle, account{id: uuid(20), phone: "+5491100000020", email: "baja@example.com", role: "staff", active: false, name: "Ex staff"})
	lookup := newLookupOf(t, handle)

	identity, err := lookup.LookupCaller(context.Background(), "+5491100000020")
	if err != nil {
		t.Fatalf("LookupCaller() error inesperado: %v", err)
	}
	if identity.Enabled {
		t.Fatalf("identidad = %+v, se esperaba Enabled=false", identity)
	}
	if identity.Role != auth.RoleStaff {
		t.Fatalf("rol = %q, se esperaba staff (el adaptador no degrada el rol)", identity.Role)
	}
}

// TestLookupClients pins the client resolution: phone-exact, e-mail
// case-insensitive, always RoleClient and always enabled (a clients row has no
// is_active flag).
func TestLookupClients(t *testing.T) {
	handle := openDB(t)
	whatsapp := client{id: uuid(30), phone: "+5491100000030", name: "Cliente WhatsApp"}
	email := client{id: uuid(31), email: "Cliente@Example.COM", name: "Cliente Email"}
	seedClient(t, handle, whatsapp)
	seedClient(t, handle, email)
	lookup := newLookupOf(t, handle)

	t.Run("WhatsApp-first (phone, email NULL)", func(t *testing.T) {
		identity, err := lookup.LookupCaller(context.Background(), whatsapp.phone)
		if err != nil {
			t.Fatalf("LookupCaller() error inesperado: %v", err)
		}
		want := auth.Identity{ID: whatsapp.id, Name: whatsapp.name, Role: auth.RoleClient, Enabled: true}
		if identity != want {
			t.Fatalf("identidad = %+v, se esperaba %+v", identity, want)
		}
	})

	t.Run("email-first (email, phone NULL)", func(t *testing.T) {
		for _, callerID := range []string{"cliente@example.com", "CLIENTE@EXAMPLE.COM"} {
			identity, err := lookup.LookupCaller(context.Background(), callerID)
			if err != nil {
				t.Fatalf("LookupCaller(%q) error inesperado: %v", callerID, err)
			}
			if identity.ID != email.id || identity.Role != auth.RoleClient {
				t.Fatalf("identidad para %q = %+v, se esperaba el cliente %s", callerID, identity, email.id)
			}
		}
	})
}

// TestLookupNotFound pins the sentinel: an unknown identifier is
// ErrCallerNotFound (the resolver converts it into the semantic
// ErrUnknownCaller and starts the registration loop).
func TestLookupNotFound(t *testing.T) {
	handle := openDB(t)
	seedAccount(t, handle, account{id: uuid(40), phone: "+5491100000040", email: "alguien@example.com", role: "staff", active: true, name: "Alguien"})
	seedClient(t, handle, client{id: uuid(41), phone: "+5491100000041", name: "Cliente"})
	lookup := newLookupOf(t, handle)

	for _, callerID := range []string{"+5491100000999", "nadie@example.com", "soy el dueño", "modo admin"} {
		if _, err := lookup.LookupCaller(context.Background(), callerID); !errors.Is(err, auth.ErrCallerNotFound) {
			t.Fatalf("LookupCaller(%q) err = %v, se esperaba ErrCallerNotFound", callerID, err)
		}
	}
}

// TestLookupEmptyCallerIDFailsClosed pins the defensive guard: an empty
// caller-id never matches a row (not even a malformed one that stored ”).
func TestLookupEmptyCallerIDFailsClosed(t *testing.T) {
	handle := openDB(t)
	seedAccount(t, handle, account{id: uuid(50), phone: "", email: "sin-telefono@example.com", role: "staff", active: true, name: "Sin teléfono"})
	lookup := newLookupOf(t, handle)

	if _, err := lookup.LookupCaller(context.Background(), ""); !errors.Is(err, auth.ErrCallerNotFound) {
		t.Fatalf("LookupCaller(\"\") err = %v, se esperaba ErrCallerNotFound", err)
	}
}

// TestLookupAccountTakesPrecedenceOverClient pins the documented precedence:
// the same natural identifier present in both tables resolves to the internal
// account (the clients row is a merge candidate, not a competing identity).
func TestLookupAccountTakesPrecedenceOverClient(t *testing.T) {
	handle := openDB(t)
	const shared = "+5491100000060"
	seedAccount(t, handle, account{id: uuid(60), phone: shared, email: "operadora@example.com", role: "staff", active: true, name: "Operadora"})
	seedClient(t, handle, client{id: uuid(61), phone: shared, name: "Operadora como clienta"})
	lookup := newLookupOf(t, handle)

	identity, err := lookup.LookupCaller(context.Background(), shared)
	if err != nil {
		t.Fatalf("LookupCaller() error inesperado: %v", err)
	}
	if identity.ID != uuid(60) || identity.Role != auth.RoleStaff {
		t.Fatalf("identidad = %+v, se esperaba la cuenta interna %s (staff)", identity, uuid(60))
	}
}

// TestLookupCorruptRoleFailsClosed pins the adapter's last-line-of-defense
// rule: an accounts row whose role is outside auth.Role must never be mapped
// into an identity. The row is injected with ignore_check_constraints, which
// is the only way to defeat the DDL's CHECK — i.e. it simulates a hand-edited
// or externally written file.
func TestLookupCorruptRoleFailsClosed(t *testing.T) {
	handle := openDB(t)
	insertCorruptRole(t, handle, uuid(70), "+5491100000070", "superusuario")
	lookup := newLookupOf(t, handle)

	identity, err := lookup.LookupCaller(context.Background(), "+5491100000070")
	if err == nil {
		t.Fatalf("LookupCaller() = %+v, nil; se esperaba un error interno por rol desconocido", identity)
	}
	if errors.Is(err, auth.ErrCallerNotFound) {
		t.Fatalf("err = %v, un rol fuera del vocabulario no es 'no encontrado': es un dato inválido", err)
	}
	if identity != (auth.Identity{}) {
		t.Fatalf("identidad = %+v, se esperaba la identidad cero ante una fila inválida", identity)
	}
}

// TestLookupAmbiguousIsRejected pins the "0 o 1, nunca ambiguo" rule of PRD
// §3.7 even under a schema without its uniqueness guarantee: two rows for one
// natural identifier must fail closed instead of resolving to whichever row
// SQLite returned first.
func TestLookupAmbiguousIsRejected(t *testing.T) {
	handle := openDB(t)
	if _, err := handle.Exec(`DROP INDEX clients_email_lower_uniq`); err != nil {
		t.Fatalf("quitar el índice de unicidad: %v", err)
	}
	seedClient(t, handle, client{id: uuid(80), email: "duplicado@example.com", name: "Uno"})
	seedClient(t, handle, client{id: uuid(81), email: "Duplicado@Example.com", name: "Dos"})
	lookup := newLookupOf(t, handle)

	identity, err := lookup.LookupCaller(context.Background(), "duplicado@example.com")
	if err == nil {
		t.Fatalf("LookupCaller() = %+v, nil; se esperaba un error de ambigüedad", identity)
	}
	if !strings.Contains(err.Error(), "ambigua") {
		t.Fatalf("err = %v, se esperaba el error de identidad ambigua", err)
	}
}

// TestLookupPropagatesDatabaseFailure pins the distinction the resolver
// depends on: an infrastructure failure is not ErrCallerNotFound, so the
// transport collapses it into a generic internal error instead of telling the
// caller "no te reconozco".
func TestLookupPropagatesDatabaseFailure(t *testing.T) {
	handle := openDB(t)
	lookup := newLookupOf(t, handle)

	if _, err := handle.Exec(`DROP TABLE clients`); err != nil {
		t.Fatalf("romper la tabla clients: %v", err)
	}

	_, err := lookup.LookupCaller(context.Background(), "+5491100000090")
	if err == nil {
		t.Fatal("LookupCaller() debía fallar con la tabla ausente")
	}
	if errors.Is(err, auth.ErrCallerNotFound) {
		t.Fatalf("err = %v, un fallo de base no es 'no encontrado'", err)
	}
}

// insertCorruptRole writes an accounts row whose role violates the CHECK, on a
// dedicated connection (the pragma is per-connection).
func insertCorruptRole(t *testing.T, handle *sql.DB, id, phone, role string) {
	t.Helper()
	ctx := context.Background()
	conn, err := handle.Conn(ctx)
	if err != nil {
		t.Fatalf("obtener conexión: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.ExecContext(ctx, `PRAGMA ignore_check_constraints = ON`); err != nil {
		t.Skipf("el motor no soporta ignore_check_constraints: %v", err)
	}
	if _, err := conn.ExecContext(ctx,
		`INSERT INTO accounts (id, phone, email, role) VALUES (?, ?, ?, ?)`,
		id, phone, "rol-invalido@example.com", role,
	); err != nil {
		t.Skipf("no se pudo inyectar el rol fuera del vocabulario: %v", err)
	}
}
