package repository_test

import (
	"context"
	"database/sql"
	"reflect"
	"testing"

	"github.com/egkike/crm-ai-business/internal/db"
	"github.com/egkike/crm-ai-business/internal/domain"
	"github.com/egkike/crm-ai-business/internal/repository"
)

// newWorkflowConfigOf wires the adapter over handle.
func newWorkflowConfigOf(t *testing.T, handle *sql.DB) *repository.WorkflowConfigRepo {
	t.Helper()
	repo, err := repository.NewWorkflowConfigRepo(handle)
	if err != nil {
		t.Fatalf("NewWorkflowConfigRepo() error inesperado: %v", err)
	}
	return repo
}

// mustLoadConfig loads the config of a seeded ticket type through the port.
func mustLoadConfig(t *testing.T, repo *repository.WorkflowConfigRepo, typeName string) domain.WorkflowConfig {
	t.Helper()
	cfg, found, err := repo.LoadConfig(context.Background(), typeName)
	if err != nil {
		t.Fatalf("LoadConfig(%q) error inesperado: %v", typeName, err)
	}
	if !found {
		t.Fatalf("LoadConfig(%q) found=false, se esperaba el template sembrado", typeName)
	}
	return cfg
}

// TestNewWorkflowConfigRepoRejectsNilHandle pins the fail-closed construction
// rule every adapter follows.
func TestNewWorkflowConfigRepoRejectsNilHandle(t *testing.T) {
	repo, err := repository.NewWorkflowConfigRepo(nil)
	if err == nil {
		t.Fatal("NewWorkflowConfigRepo(nil) debía fallar (fail-closed)")
	}
	if repo != nil {
		t.Fatalf("NewWorkflowConfigRepo(nil) devolvió un adaptador utilizable: %+v", repo)
	}
}

// TestLoadSeededWorkflowConfig is the seed-parity contract of PRD §3.7: what the
// migration writes is exactly the domain template, so the config the engine loads
// from a shipped database is the config the domain declares.
//
// The single documented difference is the 'venta' default area: the domain
// template ships DefaultAreaID empty (the domain never knows row ids) and the
// seeding migration links the type to the 'Ventas' area it creates.
func TestLoadSeededWorkflowConfig(t *testing.T) {
	repo := newWorkflowConfigOf(t, openDB(t))

	t.Run("venta: template del dominio con el área Ventas enlazada", func(t *testing.T) {
		want := domain.VentaTemplate()
		want.DefaultAreaID = db.AreaVentasID

		got := mustLoadConfig(t, repo, domain.VentaTemplate().TypeName)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("config sembrada = %+v\nse esperaba %+v", got, want)
		}
		if err := got.Validate(); err != nil {
			t.Fatalf("la config sembrada de 'venta' no valida: %v", err)
		}
	})

	t.Run("consulta: template del dominio sin área fija", func(t *testing.T) {
		want := domain.ConsultaTemplate()

		got := mustLoadConfig(t, repo, domain.ConsultaTemplate().TypeName)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("config sembrada = %+v\nse esperaba %+v", got, want)
		}
		if err := got.Validate(); err != nil {
			t.Fatalf("la config sembrada de 'consulta' no valida: %v", err)
		}
	})

	t.Run("tipo inexistente: found=false", func(t *testing.T) {
		cfg, found, err := repo.LoadConfig(context.Background(), "no-existe")
		if err != nil {
			t.Fatalf("LoadConfig() error inesperado: %v", err)
		}
		if found {
			t.Fatal("LoadConfig() found=true para un tipo inexistente")
		}
		if !reflect.DeepEqual(cfg, domain.WorkflowConfig{}) {
			t.Fatalf("LoadConfig() devolvió una config para un tipo inexistente: %+v", cfg)
		}
	})
}

// TestSaveWorkflowConfigRoundtrip pins the write path: a config saved through the
// port is returned by the next read with its state order, deadlines, flags,
// round-robin rules and roles intact, and the result validates.
func TestSaveWorkflowConfigRoundtrip(t *testing.T) {
	handle := openDB(t)
	repo := newWorkflowConfigOf(t, handle)
	ctx := context.Background()

	deadline := 48
	want := domain.WorkflowConfig{
		TypeName:      domain.ConsultaTemplate().TypeName,
		DefaultAreaID: db.AreaVentasID,
		InitialState:  "nuevo",
		States: []domain.StateConfig{
			{Name: "nuevo", OnEnterAlert: true},
			{Name: "atendido", DeadlineHours: &deadline},
			{Name: "resuelto", OnExitAlert: true},
		},
		Transitions: []domain.TransitionConfig{
			{From: "nuevo", To: "atendido", MinRole: domain.TransitionRoleStaff, RoundRobin: true},
			{From: "atendido", To: "resuelto", MinRole: domain.TransitionRoleAdmin},
			{From: "resuelto", To: "nuevo", MinRole: domain.TransitionRoleOwner},
		},
	}
	if err := want.Validate(); err != nil {
		t.Fatalf("la config de prueba no valida: %v", err)
	}

	found, err := repo.SaveConfig(ctx, want)
	if err != nil {
		t.Fatalf("SaveConfig() error inesperado: %v", err)
	}
	if !found {
		t.Fatal("SaveConfig() found=false para un tipo existente")
	}

	got := mustLoadConfig(t, repo, want.TypeName)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("config leída = %+v\nse esperaba %+v", got, want)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("la config guardada no valida: %v", err)
	}

	// The default area of the type rides the same write: the port replaces the
	// type's area together with its workflow.
	if got := queryScalar(t, handle, `SELECT default_area_id FROM ticket_types WHERE name = ?`, want.TypeName); got != db.AreaVentasID {
		t.Fatalf("área por defecto guardada = %q, se esperaba %q", got, db.AreaVentasID)
	}
}

// TestSaveWorkflowConfigRejectsUnknownType pins the aggregate boundary: saving a
// config for a type that does not exist writes nothing (creating a type is a
// separate decision, never a side effect of a config save).
func TestSaveWorkflowConfigRejectsUnknownType(t *testing.T) {
	handle := openDB(t)
	repo := newWorkflowConfigOf(t, handle)
	ctx := context.Background()

	statesBefore := queryScalar(t, handle, `SELECT COUNT(*) FROM ticket_states`)

	cfg := domain.WorkflowConfig{
		TypeName:     "inventado",
		InitialState: "nuevo",
		States:       []domain.StateConfig{{Name: "nuevo"}},
	}
	found, err := repo.SaveConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("SaveConfig() error inesperado: %v", err)
	}
	if found {
		t.Fatal("SaveConfig() found=true para un tipo inexistente: no debe crear el tipo")
	}
	if got := queryScalar(t, handle, `SELECT COUNT(*) FROM ticket_states`); got != statesBefore {
		t.Fatalf("estados tras un guardado rechazado = %s, se esperaba %s", got, statesBefore)
	}
	if got := queryScalar(t, handle, `SELECT COUNT(*) FROM ticket_types WHERE name = ?`, "inventado"); got != "0" {
		t.Fatalf("el tipo inexistente quedó creado (count=%s)", got)
	}
}

// TestSaveWorkflowConfigIsAtomic pins RF6's whole-config rule ("se rechaza la
// config completa y nada queda parcialmente escrito") at the transaction
// boundary: a failure in the middle of the write leaves the previous config
// untouched — not a prefix of the new one.
//
// Three injections, each failing at a different point of the same transaction:
// the type's default area (FK), a state row (CHECK on the state name) and a
// transition row (CHECK on the role). The first one fails before any state is
// deleted, the second after the states were deleted, the third after every state
// was re-inserted — so the rollback is proven at the start, the middle and the
// end of the write.
func TestSaveWorkflowConfigIsAtomic(t *testing.T) {
	ctx := context.Background()
	deadline := 12

	// sabotaged returns a config derived from the seeded 'venta' type with one
	// deliberate violation at the point named by the caller.
	sabotaged := func(t *testing.T, inject func(*domain.WorkflowConfig)) domain.WorkflowConfig {
		t.Helper()
		cfg := domain.VentaTemplate()
		cfg.DefaultAreaID = db.AreaVentasID
		cfg.States[1].DeadlineHours = &deadline
		cfg.States[2].OnEnterAlert = true
		inject(&cfg)
		return cfg
	}

	cases := map[string]func(*domain.WorkflowConfig){
		// FK: no area carries this id, so the type's default area cannot be set.
		"área por defecto inexistente (primer paso)": func(cfg *domain.WorkflowConfig) {
			cfg.DefaultAreaID = "00000000-0000-4000-8000-0000000009ff"
		},
		// CHECK on the state name: the first states are written, this one fails.
		"estado con nombre fuera del límite (a mitad de los estados)": func(cfg *domain.WorkflowConfig) {
			cfg.States[3].Name = "un-nombre-de-estado-que-supera-el-limite-de-cincuenta-caracteres"
		},
		// CHECK on min_role: every state is re-inserted, the last transition fails.
		"transición con rol inválido (último paso)": func(cfg *domain.WorkflowConfig) {
			cfg.Transitions[len(cfg.Transitions)-1].MinRole = domain.TransitionRole("client")
		},
	}

	for name, inject := range cases {
		t.Run(name, func(t *testing.T) {
			repo := newWorkflowConfigOf(t, openDB(t))
			before := mustLoadConfig(t, repo, domain.VentaTemplate().TypeName)

			if _, err := repo.SaveConfig(ctx, sabotaged(t, inject)); err == nil {
				t.Fatal("SaveConfig() debía fallar con la config inválida")
			}

			after := mustLoadConfig(t, repo, domain.VentaTemplate().TypeName)
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("la config quedó parcialmente escrita:\nantes = %+v\ndespués = %+v", before, after)
			}
		})
	}
}
