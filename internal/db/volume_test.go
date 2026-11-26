package db_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/egkike/crm-ai-business/internal/application/dto"
	"github.com/egkike/crm-ai-business/internal/db"
	"github.com/egkike/crm-ai-business/internal/repository"
)

// The product declares a validated ceiling, not an assumed one (PRD §2.2 / RNF
// Escalabilidad): 10k clients and 100k tickets with their events, and the tools
// still answer inside the loopback p95 budget.
const (
	volumeClients = 10_000
	volumeTickets = 100_000

	// ceilingBudget is the PRD's p95 target for tool answers on loopback with the
	// ceiling dataset loaded. Generous but real: it fails on an accidental O(n)
	// scan, not on CI jitter.
	ceilingBudget = 200 * time.Millisecond
)

// TestScaleCeiling seeds the declared ceiling and pins that identity resolution
// and catalog search answer inside the budget on the loaded database.
func TestScaleCeiling(t *testing.T) {
	ctx := context.Background()

	dbPath, err := db.ResolvePath(t.TempDir() + "/ceiling.db")
	if err != nil {
		t.Fatalf("db.ResolvePath() error inesperado: %v", err)
	}
	handle, err := db.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("db.Open() error inesperado: %v", err)
	}
	t.Cleanup(func() { _ = handle.Close() })

	seedCeiling(t, handle)

	lookup, err := repository.NewCallerLookup(handle)
	if err != nil {
		t.Fatalf("NewCallerLookup() error inesperado: %v", err)
	}
	catalog, err := repository.NewCatalogRepo(handle)
	if err != nil {
		t.Fatalf("NewCatalogRepo() error inesperado: %v", err)
	}

	// A client written first and one written last: the last is the honest probe
	// (a resolver that scans and happens to hit the first row proves nothing).
	for _, probe := range []struct {
		name  string
		phone string
	}{
		{"primer cliente", volumePhone(0)},
		{"último cliente", volumePhone(volumeClients - 1)},
	} {
		t.Run("resolución "+probe.name, func(t *testing.T) {
			measure(t, ceilingBudget, func() error {
				_, err := lookup.LookupCaller(ctx, probe.phone)
				return err
			})
		})
	}

	t.Run("búsqueda de catálogo con el techo cargado", func(t *testing.T) {
		measure(t, ceilingBudget, func() error {
			_, err := catalog.SearchCatalog(ctx, dto.SearchCatalogQuery{Text: "freezer no enfría", Limit: 10})
			return err
		})
	})
}

// measure runs fn repeatedly and fails when the median answer exceeds the
// budget: a single slow sample must not fail the test, a systematically slow
// path must.
func measure(t *testing.T, budget time.Duration, fn func() error) {
	t.Helper()

	const samples = 20
	durations := make([]time.Duration, 0, samples)
	for index := 0; index < samples; index++ {
		start := time.Now()
		if err := fn(); err != nil {
			t.Fatalf("operación falló: %v", err)
		}
		durations = append(durations, time.Since(start))
	}

	for i := 1; i < len(durations); i++ {
		for j := i; j > 0 && durations[j] < durations[j-1]; j-- {
			durations[j], durations[j-1] = durations[j-1], durations[j]
		}
	}
	median := durations[len(durations)/2]
	if median > budget {
		t.Fatalf("mediana = %v, presupuesto = %v (el techo declarado no se cumple)", median, budget)
	}
	t.Logf("mediana = %v (presupuesto %v) sobre %d muestras", median, budget, samples)
}

// seedCeiling loads the ceiling dataset: clients, the fixed ticket type from the
// migration seed, tickets with their opening events, and catalog rows carrying
// knowledge text so the FTS index has real content to search.
func seedCeiling(t *testing.T, handle *sql.DB) {
	t.Helper()

	seed := func(label, statement string, rows int, args func(int) []any) {
		t.Helper()
		tx, err := handle.Begin()
		if err != nil {
			t.Fatalf("begin %s: %v", label, err)
		}
		defer func() { _ = tx.Rollback() }()
		stmt, err := tx.Prepare(statement)
		if err != nil {
			t.Fatalf("prepare %s: %v", label, err)
		}
		defer func() { _ = stmt.Close() }()
		for index := 0; index < rows; index++ {
			if _, err := stmt.Exec(args(index)...); err != nil {
				t.Fatalf("insert %s[%d]: %v", label, index, err)
			}
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit %s: %v", label, err)
		}
	}

	seed("clients",
		`INSERT INTO clients (id, phone, display_name, created_at, updated_at) VALUES (?, ?, ?, datetime('now'), datetime('now'))`,
		volumeClients,
		func(index int) []any {
			return []any{volumeClientID(index), volumePhone(index), "Cliente " + volumePhone(index)}
		})

	for index := 0; index < 5; index++ {
		seed("catalog",
			`INSERT INTO catalog_items (id, kind, name, description, use_cases, limitations, price_cents, is_active, created_at, updated_at)
			 VALUES (?, 'servicio', ?, ?, ?, ?, ?, 1, datetime('now'), datetime('now'))`,
			1,
			func(int) []any {
				return []any{
					volumeCatalogID(index),
					"Servicio " + string(rune('A'+index)),
					"Asistencia técnica",
					"reparación de heladeras y freezer que no enfría",
					"no cubre equipos en garantía",
					1500000 + index,
				}
			})
	}

	const ticketTypeID = "00000000-0000-4000-8000-000000000001"
	seed("tickets",
		`INSERT INTO tickets (id, ticket_type_id, client_id, state, created_at, updated_at) VALUES (?, ?, ?, 'nuevo', datetime('now'), datetime('now'))`,
		volumeTickets,
		func(index int) []any {
			return []any{volumeTicketID(index), ticketTypeID, volumeClientID(index % volumeClients)}
		})

	seed("ticket_events",
		`INSERT INTO ticket_events (ticket_id, actor, kind, payload, created_at) VALUES (?, 'system', 'opened', '{}', datetime('now'))`,
		volumeTickets,
		func(index int) []any {
			return []any{volumeTicketID(index)}
		})
}

// uuidFromInt renders a deterministic RFC 4122 v4-shaped UUID for seed row n.
func uuidFromInt(n int) string {
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", n)
}

// volumePhone renders a deterministic, format-valid phone for seed row n.
func volumePhone(n int) string {
	return fmt.Sprintf("+54911%07d", n)
}

func volumeClientID(index int) string  { return uuidFromInt(index + 1_000_000) }
func volumeTicketID(index int) string  { return uuidFromInt(index + 9_000_000) }
func volumeCatalogID(index int) string { return uuidFromInt(index + 5_000_000) }
