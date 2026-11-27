package domain

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// hours is a pointer helper for optional DeadlineHours values.
func hours(v int) *int { return &v }

// validMinimal is the smallest config the engine accepts: one state, that
// state initial, no transitions.
func validMinimal() WorkflowConfig {
	return WorkflowConfig{
		TypeName:     "soporte",
		InitialState: "nuevo",
		States:       []StateConfig{{Name: "nuevo"}},
	}
}

// rejectCode asserts that Validate rejected the config with the exact
// machine-readable class. The code is the contract transports branch on;
// a rejection with the wrong class is a different rule firing.
func rejectCode(t *testing.T, err error, want ErrorCode) {
	t.Helper()
	if err == nil {
		t.Fatalf("esperaba rechazo con código %q, obtuvo nil", want)
	}
	var semantic *SemanticError
	if !errors.As(err, &semantic) {
		t.Fatalf("esperaba *SemanticError, obtuve %T: %v", err, err)
	}
	if semantic.Code() != want {
		t.Fatalf("esperaba código %q, obtuve %q (mensaje %q)",
			want, semantic.Code(), semantic.Message())
	}
}

func TestWorkflowValidateAccepts(t *testing.T) {
	t.Run("minimal single state", func(t *testing.T) {
		if err := validMinimal().Validate(); err != nil {
			t.Fatalf("config mínima válida rechazada: %v", err)
		}
	})

	t.Run("full pipeline with deadline and round-robin", func(t *testing.T) {
		cfg := WorkflowConfig{
			TypeName:      "venta",
			DefaultAreaID: "area-uuid",
			InitialState:  "nuevo",
			States: []StateConfig{
				{Name: "nuevo"},
				{Name: "contactado", DeadlineHours: hours(48)},
				{Name: "presupuestado", OnEnterAlert: true},
				{Name: "cerrado"},
			},
			Transitions: []TransitionConfig{
				{From: "nuevo", To: "contactado", MinRole: TransitionRoleStaff, RoundRobin: true},
				{From: "contactado", To: "presupuestado", MinRole: TransitionRoleAdmin},
				{From: "presupuestado", To: "cerrado", MinRole: TransitionRoleOwner},
			},
		}
		if err := cfg.Validate(); err != nil {
			t.Fatalf("config completa válida rechazada: %v", err)
		}
	})

	t.Run("seed template venta", func(t *testing.T) {
		cfg := VentaTemplate()
		if err := cfg.Validate(); err != nil {
			t.Fatalf("plantilla 'venta' rechazada: %v", err)
		}
	})

	t.Run("seed template consulta", func(t *testing.T) {
		cfg := ConsultaTemplate()
		if err := cfg.Validate(); err != nil {
			t.Fatalf("plantilla 'consulta' rechazada: %v", err)
		}
	})
}

func TestWorkflowValidateRejectsName(t *testing.T) {
	t.Run("empty type name", func(t *testing.T) {
		cfg := validMinimal()
		cfg.TypeName = ""
		rejectCode(t, cfg.Validate(), CodeInvalidWorkflowName)
	})

	t.Run("type name over 50 runes", func(t *testing.T) {
		cfg := validMinimal()
		cfg.TypeName = string(make([]rune, MaxWorkflowNameLen+1))
		rejectCode(t, cfg.Validate(), CodeInvalidWorkflowName)
	})

	t.Run("type name with surrounding spaces", func(t *testing.T) {
		cfg := validMinimal()
		cfg.TypeName = " soporte"
		rejectCode(t, cfg.Validate(), CodeInvalidWorkflowName)
	})
}

func TestWorkflowValidateRejectsStates(t *testing.T) {
	t.Run("no states", func(t *testing.T) {
		cfg := validMinimal()
		cfg.States = nil
		rejectCode(t, cfg.Validate(), CodeInvalidWorkflowState)
	})

	t.Run("state without name", func(t *testing.T) {
		cfg := validMinimal()
		cfg.States = append(cfg.States, StateConfig{Name: ""})
		rejectCode(t, cfg.Validate(), CodeInvalidWorkflowState)
	})

	t.Run("duplicate state names", func(t *testing.T) {
		cfg := validMinimal()
		cfg.States = append(cfg.States, StateConfig{Name: "nuevo"})
		rejectCode(t, cfg.Validate(), CodeInvalidWorkflowState)
	})

	t.Run("state name over 50 runes", func(t *testing.T) {
		cfg := validMinimal()
		cfg.States = []StateConfig{{Name: string(make([]rune, MaxWorkflowNameLen+1))}}
		cfg.InitialState = cfg.States[0].Name
		rejectCode(t, cfg.Validate(), CodeInvalidWorkflowState)
	})

	t.Run("zero deadline", func(t *testing.T) {
		cfg := validMinimal()
		cfg.States[0].DeadlineHours = hours(0)
		rejectCode(t, cfg.Validate(), CodeInvalidWorkflowState)
	})

	t.Run("negative deadline", func(t *testing.T) {
		cfg := validMinimal()
		cfg.States[0].DeadlineHours = hours(-1)
		rejectCode(t, cfg.Validate(), CodeInvalidWorkflowState)
	})

	t.Run("over the state cap", func(t *testing.T) {
		cfg := validMinimal()
		cfg.States = cfg.States[:0]
		for i := 0; i <= MaxWorkflowStates; i++ {
			cfg.States = append(cfg.States, StateConfig{Name: fmt.Sprintf("estado-%02d", i)})
		}
		cfg.InitialState = cfg.States[0].Name
		rejectCode(t, cfg.Validate(), CodeInvalidWorkflowState)
	})
}

func TestWorkflowValidateRejectsInitial(t *testing.T) {
	t.Run("missing initial state", func(t *testing.T) {
		cfg := validMinimal()
		cfg.InitialState = ""
		rejectCode(t, cfg.Validate(), CodeInvalidWorkflowInitial)
	})

	t.Run("initial state not declared (RF6: transición a un estado inexistente)", func(t *testing.T) {
		cfg := validMinimal()
		cfg.InitialState = "inexistente"
		err := cfg.Validate()
		rejectCode(t, err, CodeInvalidWorkflowInitial)
		var semantic *SemanticError
		if !errors.As(err, &semantic) {
			t.Fatalf("esperaba *SemanticError, obtuve %T", err)
		}
		if !strings.Contains(semantic.Message(), "inexistente") {
			t.Fatalf("el mensaje no nombra el estado: %q", semantic.Message())
		}
	})
}

func TestWorkflowValidateRejectsTransitions(t *testing.T) {
	validTwoState := func() WorkflowConfig {
		return WorkflowConfig{
			TypeName:     "soporte",
			InitialState: "nuevo",
			States:       []StateConfig{{Name: "nuevo"}, {Name: "resuelto"}},
		}
	}

	t.Run("transition from an undeclared state", func(t *testing.T) {
		cfg := validTwoState()
		cfg.Transitions = []TransitionConfig{{From: "fantasma", To: "resuelto", MinRole: TransitionRoleStaff}}
		rejectCode(t, cfg.Validate(), CodeInvalidWorkflowTransition)
	})

	t.Run("transition to an undeclared state (RF6 criterio: config rechazada íntegra)", func(t *testing.T) {
		cfg := validTwoState()
		cfg.Transitions = []TransitionConfig{{From: "nuevo", To: "fantasma", MinRole: TransitionRoleStaff}}
		err := cfg.Validate()
		rejectCode(t, err, CodeInvalidWorkflowTransition)
		var semantic *SemanticError
		if !errors.As(err, &semantic) {
			t.Fatalf("esperaba *SemanticError, obtuve %T", err)
		}
		if !strings.Contains(semantic.Message(), "fantasma") {
			t.Fatalf("el mensaje no nombra el estado inexistente: %q", semantic.Message())
		}
	})

	t.Run("transition without origin and destination", func(t *testing.T) {
		cfg := validTwoState()
		cfg.Transitions = []TransitionConfig{{From: "", To: "", MinRole: TransitionRoleStaff}}
		rejectCode(t, cfg.Validate(), CodeInvalidWorkflowTransition)
	})

	t.Run("self-loop transition", func(t *testing.T) {
		cfg := validTwoState()
		cfg.Transitions = []TransitionConfig{{From: "nuevo", To: "nuevo", MinRole: TransitionRoleStaff}}
		rejectCode(t, cfg.Validate(), CodeInvalidWorkflowTransition)
	})

	t.Run("duplicate origin-destination pair", func(t *testing.T) {
		cfg := validTwoState()
		cfg.Transitions = []TransitionConfig{
			{From: "nuevo", To: "resuelto", MinRole: TransitionRoleStaff},
			{From: "nuevo", To: "resuelto", MinRole: TransitionRoleAdmin},
		}
		rejectCode(t, cfg.Validate(), CodeInvalidWorkflowTransition)
	})

	t.Run("empty role", func(t *testing.T) {
		cfg := validTwoState()
		cfg.Transitions = []TransitionConfig{{From: "nuevo", To: "resuelto"}}
		rejectCode(t, cfg.Validate(), CodeInvalidWorkflowTransition)
	})

	t.Run("client role is never a transition role", func(t *testing.T) {
		cfg := validTwoState()
		cfg.Transitions = []TransitionConfig{{From: "nuevo", To: "resuelto", MinRole: TransitionRole("client")}}
		rejectCode(t, cfg.Validate(), CodeInvalidWorkflowTransition)
	})

	t.Run("unknown role", func(t *testing.T) {
		cfg := validTwoState()
		cfg.Transitions = []TransitionConfig{{From: "nuevo", To: "resuelto", MinRole: TransitionRole("dios")}}
		rejectCode(t, cfg.Validate(), CodeInvalidWorkflowTransition)
	})

	t.Run("over the transition cap", func(t *testing.T) {
		cfg := validTwoState()
		// Unique pairs within the 50-state cap: i→j for i≠j.
		n := MaxWorkflowStates - len(cfg.States) // fill up to the cap exactly
		for i := 0; i < n; i++ {
			cfg.States = append(cfg.States, StateConfig{Name: fmt.Sprintf("extra-%02d", i)})
		}
		outer := 0
	loop:
		for i := 0; i < n; i++ {
			for j := 0; j < n; j++ {
				if i == j {
					continue
				}
				cfg.Transitions = append(cfg.Transitions, TransitionConfig{
					From: fmt.Sprintf("extra-%02d", i), To: fmt.Sprintf("extra-%02d", j),
					MinRole: TransitionRoleStaff,
				})
				outer++
				if outer > MaxWorkflowTransitions {
					break loop
				}
			}
		}
		rejectCode(t, cfg.Validate(), CodeInvalidWorkflowTransition)
	})
}

func TestSeedTemplates(t *testing.T) {
	t.Run("venta: §3.7 pipeline with round-robin into Ventas", func(t *testing.T) {
		cfg := VentaTemplate()
		if cfg.TypeName != "venta" {
			t.Fatalf("TypeName = %q, esperaba %q", cfg.TypeName, "venta")
		}
		wantStates := []string{"nuevo", "contactado", "presupuestado", "cerrado", "perdido"}
		if len(cfg.States) != len(wantStates) {
			t.Fatalf("estados de venta = %d, esperaba %d", len(cfg.States), len(wantStates))
		}
		for i, want := range wantStates {
			if cfg.States[i].Name != want {
				t.Fatalf("estado[%d] = %q, esperaba %q", i, cfg.States[i].Name, want)
			}
		}
		if cfg.InitialState != "nuevo" {
			t.Fatalf("InitialState = %q, esperaba %q", cfg.InitialState, "nuevo")
		}
		// The round-robin rule rides the transition out of the initial state.
		rr := false
		for _, tr := range cfg.Transitions {
			if tr.From == "nuevo" && tr.To == "contactado" && tr.RoundRobin {
				rr = true
			}
		}
		if !rr {
			t.Fatal("la plantilla 'venta' no declara round-robin en nuevo→contactado")
		}
	})

	t.Run("consulta: §3.7 pipeline without fixed area", func(t *testing.T) {
		cfg := ConsultaTemplate()
		if cfg.TypeName != "consulta" {
			t.Fatalf("TypeName = %q, esperaba %q", cfg.TypeName, "consulta")
		}
		if cfg.DefaultAreaID != "" {
			t.Fatalf("DefaultAreaID = %q, esperaba vacío ('sin área fija')", cfg.DefaultAreaID)
		}
		wantStates := []string{"nuevo", "atendido", "resuelto"}
		if len(cfg.States) != len(wantStates) {
			t.Fatalf("estados de consulta = %d, esperaba %d", len(cfg.States), len(wantStates))
		}
		for i, want := range wantStates {
			if cfg.States[i].Name != want {
				t.Fatalf("estado[%d] = %q, esperaba %q", i, cfg.States[i].Name, want)
			}
		}
		for _, tr := range cfg.Transitions {
			if tr.RoundRobin {
				t.Fatal("la plantilla 'consulta' no declara round-robin")
			}
		}
	})

	t.Run("templates are fresh copies", func(t *testing.T) {
		a := VentaTemplate()
		a.States[0].DeadlineHours = hours(1)
		if VentaTemplate().States[0].DeadlineHours != nil {
			t.Fatal("VentaTemplate comparte estado entre llamadas")
		}
	})
}
