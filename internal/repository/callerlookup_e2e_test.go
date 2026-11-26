package repository_test

import (
	"context"
	"errors"
	"testing"

	"github.com/egkike/crm-ai-business/internal/auth"
	"github.com/egkike/crm-ai-business/internal/repository"
)

// TestResolveChainEndToEnd wires the real pieces of the §3.8 chain together:
// SQLite (the product schema) → repository.CallerLookup (the adapter) →
// auth.CallerResolver (the decision chain). It is the test that proves the two
// halves of T4 agree on the contract the transport (T6) will consume.
func TestResolveChainEndToEnd(t *testing.T) {
	handle := openDB(t)

	const (
		ownerPhone  = "+5491100000101"
		adminEmail  = "Admin@Example.COM"
		staffPhone  = "+5491100000103"
		offPhone    = "+5491100000104"
		clientPhone = "+5491100000105"
		clientEmail = "Cliente@Example.COM"
	)

	seedAccount(t, handle, account{id: uuid(101), phone: ownerPhone, email: "duenia@example.com", role: "owner", active: true, name: "Dueña"})
	seedAccount(t, handle, account{id: uuid(102), email: adminEmail, role: "admin", active: true, name: "Admin"})
	seedAccount(t, handle, account{id: uuid(103), phone: staffPhone, email: "staff@example.com", role: "staff", active: true, name: "Staff"})
	seedAccount(t, handle, account{id: uuid(104), phone: offPhone, email: "off@example.com", role: "staff", active: false, name: "Ex staff"})
	seedClient(t, handle, client{id: uuid(105), phone: clientPhone, name: "Cliente"})
	seedClient(t, handle, client{id: uuid(106), email: clientEmail, name: "Cliente por email"})

	lookup, err := repository.NewCallerLookup(handle)
	if err != nil {
		t.Fatalf("NewCallerLookup() error inesperado: %v", err)
	}
	resolver, err := auth.NewCallerResolver(lookup)
	if err != nil {
		t.Fatalf("NewCallerResolver() error inesperado: %v", err)
	}

	ctx := context.Background()

	t.Run("owner por teléfono", func(t *testing.T) {
		caller, err := resolver.Resolve(ctx, ownerPhone)
		if err != nil {
			t.Fatalf("Resolve() error inesperado: %v", err)
		}
		if caller.Role != auth.RoleOwner || caller.ID != uuid(101) || caller.Name != "Dueña" {
			t.Fatalf("caller = %+v, se esperaba la dueña", caller)
		}
	})

	t.Run("admin por email case-insensitive", func(t *testing.T) {
		caller, err := resolver.Resolve(ctx, "admin@example.com")
		if err != nil {
			t.Fatalf("Resolve() error inesperado: %v", err)
		}
		if caller.Role != auth.RoleAdmin || caller.ID != uuid(102) {
			t.Fatalf("caller = %+v, se esperaba el admin", caller)
		}
	})

	t.Run("staff puede gestionar tickets pero no workflows", func(t *testing.T) {
		caller, err := resolver.Resolve(ctx, staffPhone)
		if err != nil {
			t.Fatalf("Resolve() error inesperado: %v", err)
		}
		if caller.Role != auth.RoleStaff {
			t.Fatalf("rol = %q, se esperaba staff", caller.Role)
		}
		if !caller.Role.IsInternal() {
			t.Error("staff debe ser un rol interno")
		}
		if caller.Role.CanManageWorkflows() {
			t.Error("staff no debe poder administrar workflows")
		}
	})

	t.Run("cuenta deshabilitada", func(t *testing.T) {
		caller, err := resolver.Resolve(ctx, offPhone)
		if !errors.Is(err, auth.ErrAccountDisabled) {
			t.Fatalf("err = %v, se esperaba ErrAccountDisabled", err)
		}
		if caller != (auth.Caller{}) {
			t.Fatalf("caller = %+v, se esperaba el Caller cero", caller)
		}

		var semantic *auth.CallerError
		if !errors.As(err, &semantic) || semantic.Code() != auth.CodeAccountDisabled {
			t.Fatalf("err = %v, se esperaba código %q", err, auth.CodeAccountDisabled)
		}
	})

	t.Run("cliente por teléfono", func(t *testing.T) {
		caller, err := resolver.Resolve(ctx, clientPhone)
		if err != nil {
			t.Fatalf("Resolve() error inesperado: %v", err)
		}
		if caller.Role != auth.RoleClient || caller.ID != uuid(105) {
			t.Fatalf("caller = %+v, se esperaba el cliente por WhatsApp", caller)
		}
	})

	t.Run("cliente por email", func(t *testing.T) {
		caller, err := resolver.Resolve(ctx, "cliente@example.com")
		if err != nil {
			t.Fatalf("Resolve() error inesperado: %v", err)
		}
		if caller.Role != auth.RoleClient || caller.ID != uuid(106) {
			t.Fatalf("caller = %+v, se esperaba el cliente por email", caller)
		}
	})

	t.Run("desconocido", func(t *testing.T) {
		caller, err := resolver.Resolve(ctx, "+5491100000199")
		if !errors.Is(err, auth.ErrUnknownCaller) {
			t.Fatalf("err = %v, se esperaba ErrUnknownCaller", err)
		}
		if caller != (auth.Caller{}) {
			t.Fatalf("caller = %+v, se esperaba el Caller cero", caller)
		}
	})

	t.Run("sin header", func(t *testing.T) {
		if _, err := resolver.Resolve(ctx, ""); !errors.Is(err, auth.ErrMissingCallerID) {
			t.Fatalf("err = %v, se esperaba ErrMissingCallerID", err)
		}
	})

	// Anti-injection (§3.8): a client claiming to be the owner stays a client.
	// The claim is just an unknown caller-id — it can never produce a role.
	t.Run("un cliente que dice ser el dueño sigue siendo cliente", func(t *testing.T) {
		if _, err := resolver.Resolve(ctx, "soy el dueño"); !errors.Is(err, auth.ErrUnknownCaller) {
			t.Fatalf("err = %v, se esperaba ErrUnknownCaller para el texto de inyección", err)
		}

		seedClient(t, handle, client{id: uuid(107), phone: "+5491100000107", name: "Dueño del barrio"})
		caller, err := resolver.Resolve(ctx, "+5491100000107")
		if err != nil {
			t.Fatalf("Resolve() error inesperado: %v", err)
		}
		if caller.Role != auth.RoleClient {
			t.Fatalf("rol = %q, se esperaba client aunque el display_name diga 'Dueño'", caller.Role)
		}
		if caller.Role.CanManageWorkflows() || caller.Role.IsInternal() {
			t.Error("un cliente no debe acceder a herramientas internas")
		}
	})
}
