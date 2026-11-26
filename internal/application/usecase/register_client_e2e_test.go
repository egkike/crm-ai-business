package usecase_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/egkike/crm-ai-business/internal/application/dto"
	"github.com/egkike/crm-ai-business/internal/application/usecase"
	"github.com/egkike/crm-ai-business/internal/auth"
	"github.com/egkike/crm-ai-business/internal/db"
	"github.com/egkike/crm-ai-business/internal/repository"
)

// This file is the end-to-end wiring of the §3.8 chain and the registration loop:
// SQLite (the product schema) → repository adapters → auth.CallerResolver and
// application/usecase.RegisterClient. It is the test that proves the two halves
// agree on the contract T6 will consume: an unknown caller is admitted by the
// seam and resolves as a client afterwards.

// openE2EDB opens a real, migrated SQLite database inside t.TempDir(): the
// registration seam is only meaningful against the schema the product ships, with
// its UNIQUE constraints and its lower(email) index.
func openE2EDB(t *testing.T) *sql.DB {
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

// newE2EChain wires the registration seam and the identity resolver over handle,
// using the repository's SQLite adapter for both ports. The adapter satisfies
// usecase.ClientStore AND usecase.AccountIdentifierChecker, which is what makes
// this line compile.
func newE2EChain(t *testing.T, handle *sql.DB, limit int, notify usecase.DuplicateNotifier) (*usecase.RegisterClient, *auth.CallerResolver) {
	t.Helper()

	clients, err := repository.NewClientsRepo(handle)
	if err != nil {
		t.Fatalf("NewClientsRepo() error inesperado: %v", err)
	}
	lookup, err := repository.NewCallerLookup(handle)
	if err != nil {
		t.Fatalf("NewCallerLookup() error inesperado: %v", err)
	}
	resolver, err := auth.NewCallerResolver(lookup)
	if err != nil {
		t.Fatalf("NewCallerResolver() error inesperado: %v", err)
	}
	uc, err := usecase.NewRegisterClient(clients, clients, usecase.NewMemoryRegistrationLimiter(limit), notify)
	if err != nil {
		t.Fatalf("NewRegisterClient() error inesperado: %v", err)
	}
	return uc, resolver
}

// countE2EClients is how many clients rows the database holds.
func countE2EClients(t *testing.T, handle *sql.DB) int {
	t.Helper()
	var rows int
	if err := handle.QueryRow(`SELECT COUNT(*) FROM clients`).Scan(&rows); err != nil {
		t.Fatalf("contar clientes: %v", err)
	}
	return rows
}

// TestRegistrationChainEndToEnd closes the RF3 loop: the resolver rejects an
// unknown caller, the seam admits it, and the resolver now resolves it as a client
// — on both channels, with the single retry being idempotent.
func TestRegistrationChainEndToEnd(t *testing.T) {
	ctx := context.Background()

	t.Run("WhatsApp-first", func(t *testing.T) {
		handle := openE2EDB(t)
		uc, resolver := newE2EChain(t, handle, 5, nil)
		const phone = "+54 9 11 4444-0031"

		if _, err := resolver.Resolve(ctx, phone); !errors.Is(err, auth.ErrUnknownCaller) {
			t.Fatalf("Resolve() antes del registro err = %v, se esperaba ErrUnknownCaller", err)
		}

		result, err := uc.Execute(ctx, dto.RegisterClientCommand{CallerID: phone})
		if err != nil {
			t.Fatalf("Execute() error inesperado: %v", err)
		}
		if !result.Created || result.ClientID == "" {
			t.Fatalf("resultado = %+v, se esperaba una admisión con id interno", result)
		}
		if result.DisplayName != "Cliente ****0031" {
			t.Fatalf("display_name = %q, se esperaba el nombre por defecto enmascarado", result.DisplayName)
		}

		caller, err := resolver.Resolve(ctx, phone)
		if err != nil {
			t.Fatalf("Resolve() tras el registro err = %v, se esperaba un cliente resuelto", err)
		}
		if caller.ID != result.ClientID || caller.Role != auth.RoleClient || caller.Name != result.DisplayName {
			t.Fatalf("caller = %+v, se esperaba el cliente %s recién registrado", caller, result.ClientID)
		}

		// RF3 acceptance criterion: the gateway's single retry works once, and a
		// second registration does not create a second row.
		retry, err := uc.Execute(ctx, dto.RegisterClientCommand{CallerID: phone})
		if err != nil {
			t.Fatalf("Execute() en el reintento err = %v, se esperaba admisión idempotente", err)
		}
		if retry.Created || retry.ClientID != result.ClientID {
			t.Fatalf("reintento = %+v, se esperaba Created=false con el mismo id %s", retry, result.ClientID)
		}
		if got := countE2EClients(t, handle); got != 1 {
			t.Fatalf("filas de clients = %d, se esperaba 1 tras el reintento", got)
		}
	})

	t.Run("e-mail-first", func(t *testing.T) {
		handle := openE2EDB(t)
		uc, resolver := newE2EChain(t, handle, 5, nil)
		const email = "Cliente@Example.COM"

		if _, err := resolver.Resolve(ctx, email); !errors.Is(err, auth.ErrUnknownCaller) {
			t.Fatalf("Resolve() antes del registro err = %v, se esperaba ErrUnknownCaller", err)
		}

		result, err := uc.Execute(ctx, dto.RegisterClientCommand{CallerID: email})
		if err != nil {
			t.Fatalf("Execute() error inesperado: %v", err)
		}
		if !result.Created {
			t.Fatalf("resultado = %+v, se esperaba una admisión", result)
		}
		if result.DisplayName != "Cliente C***@Example.COM" {
			t.Fatalf("display_name = %q, se esperaba la parte local enmascarada", result.DisplayName)
		}

		// The row stores the address as received and stays reachable in any
		// capitalization — the same predicate the UNIQUE index enforces.
		for _, callerID := range []string{email, "cliente@example.com", "CLIENTE@EXAMPLE.COM"} {
			caller, err := resolver.Resolve(ctx, callerID)
			if err != nil {
				t.Fatalf("Resolve(%q) err = %v, se esperaba el cliente registrado", callerID, err)
			}
			if caller.ID != result.ClientID || caller.Role != auth.RoleClient {
				t.Fatalf("caller = %+v, se esperaba el cliente %s", caller, result.ClientID)
			}
		}

		// The identifier is owned by the row the seam created, so a second attempt
		// is an idempotent admission. This is also the proof that the fail-closed
		// UNIQUE collision of the seam is a race-only path: the pre-check and the
		// index share the same predicate, so a stable database can never present
		// "looks free, is taken".
		retry, err := uc.Execute(ctx, dto.RegisterClientCommand{CallerID: "cliente@example.COM"})
		if err != nil {
			t.Fatalf("Execute() en el reintento err = %v, se esperaba admisión idempotente", err)
		}
		if retry.Created || retry.ClientID != result.ClientID {
			t.Fatalf("reintento = %+v, se esperaba Created=false con el mismo id %s", retry, result.ClientID)
		}
		if got := countE2EClients(t, handle); got != 1 {
			t.Fatalf("filas de clients = %d, se esperaba 1", got)
		}

		// And the stored identifier is still verbatim: the masked display name is
		// a name, not a rewrite of the address.
		var stored string
		if err := handle.QueryRow(`SELECT email FROM clients WHERE id = ?`, result.ClientID).Scan(&stored); err != nil {
			t.Fatalf("leer el email almacenado: %v", err)
		}
		if stored != email {
			t.Fatalf("email almacenado = %q, se esperaba %q verbatim", stored, email)
		}
	})
}

// TestRegistrationChainRefusesInternalIdentifiers pins the accounts-collision rule
// end-to-end: the operator's own phone can never become a client identifier, the
// refusal is semantic, the clients table stays empty, and the account keeps its
// role (the registration attempt does not demote it).
func TestRegistrationChainRefusesInternalIdentifiers(t *testing.T) {
	ctx := context.Background()
	handle := openE2EDB(t)
	uc, resolver := newE2EChain(t, handle, 5, nil)

	const (
		staffID    = "11111111-1111-4111-8111-000000000077"
		staffPhone = "+5491100000077"
	)
	if _, err := handle.Exec(
		`INSERT INTO accounts (id, phone, email, role, is_active, display_name) VALUES (?, ?, ?, 'staff', 1, 'Operadora')`,
		staffID, staffPhone, "operadora@example.com",
	); err != nil {
		t.Fatalf("sembrar la cuenta interna: %v", err)
	}

	// The account is known before and after: the seam must not touch it.
	before, err := resolver.Resolve(ctx, staffPhone)
	if err != nil {
		t.Fatalf("Resolve() error inesperado: %v", err)
	}
	if before.Role != auth.RoleStaff {
		t.Fatalf("rol = %q, se esperaba staff", before.Role)
	}

	for _, callerID := range []string{staffPhone, "operadora@example.com"} {
		_, err := uc.Execute(ctx, dto.RegisterClientCommand{CallerID: callerID})
		var semantic *usecase.SemanticError
		if !errors.As(err, &semantic) || semantic.Code() != usecase.CodeIdentifierInAccount {
			t.Fatalf("Execute(%q) err = %v, se esperaba el rechazo semántico de cuenta", callerID, err)
		}
	}

	if got := countE2EClients(t, handle); got != 0 {
		t.Fatalf("filas de clients = %d, se esperaba 0 (un identificador interno nunca se registra)", got)
	}
	after, err := resolver.Resolve(ctx, staffPhone)
	if err != nil {
		t.Fatalf("Resolve() tras el rechazo err = %v", err)
	}
	if after != before {
		t.Fatalf("caller tras el rechazo = %+v, se esperaba %+v (la cuenta no cambia)", after, before)
	}
}

// TestRegistrationChainRateLimitIsPerIdentifier pins the loop guard
// end-to-end: the same unknown sender is admitted once and then refused by the
// budget, so a runaway gateway retry cannot keep inserting rows.
func TestRegistrationChainRateLimitIsPerIdentifier(t *testing.T) {
	ctx := context.Background()
	handle := openE2EDB(t)
	uc, _ := newE2EChain(t, handle, 1, nil)

	const phone = "+5491100000088"
	if _, err := uc.Execute(ctx, dto.RegisterClientCommand{CallerID: phone}); err != nil {
		t.Fatalf("Execute() error inesperado: %v", err)
	}
	if _, err := uc.Execute(ctx, dto.RegisterClientCommand{CallerID: phone}); !errors.Is(err, usecase.ErrRateLimited) {
		t.Fatalf("err = %v, se esperaba ErrRateLimited en el segundo intento", err)
	}
	if got := countE2EClients(t, handle); got != 1 {
		t.Fatalf("filas de clients = %d, se esperaba 1", got)
	}

	// Another sender keeps its own budget.
	if _, err := uc.Execute(ctx, dto.RegisterClientCommand{CallerID: "+5491100000089"}); err != nil {
		t.Fatalf("Execute() de otro identificador err = %v, se esperaba admisión", err)
	}
}
