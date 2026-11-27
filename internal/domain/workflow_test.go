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

// TestWorkflowDeadlineBoundaries pins the inclusive lower bound and the newly
// added upper bound of StateConfig.DeadlineHours (rider R3-3 of review
// 8fc68db721abce52): the boundary values themselves are the contract, and the
// deadline of one hour was never exercised before.
func TestWorkflowDeadlineBoundaries(t *testing.T) {
	withDeadline := func(value int) WorkflowConfig {
		cfg := validMinimal()
		cfg.States[0].DeadlineHours = hours(value)
		return cfg
	}

	t.Run("una hora se acepta (cota inferior inclusiva)", func(t *testing.T) {
		if err := withDeadline(1).Validate(); err != nil {
			t.Fatalf("deadline de una hora rechazado: %v", err)
		}
	})

	t.Run("un año se acepta (cota superior inclusiva)", func(t *testing.T) {
		if err := withDeadline(MaxDeadlineHours).Validate(); err != nil {
			t.Fatalf("deadline de %d horas rechazado: %v", MaxDeadlineHours, err)
		}
	})

	t.Run("una hora por encima del máximo se rechaza", func(t *testing.T) {
		err := withDeadline(MaxDeadlineHours + 1).Validate()
		rejectCode(t, err, CodeInvalidWorkflowState)

		var semantic *SemanticError
		if !errors.As(err, &semantic) {
			t.Fatalf("esperaba *SemanticError, obtuve %T", err)
		}
		// The message names the cap so the Owner can fix the typo without
		// reading documentation.
		if !strings.Contains(semantic.Message(), fmt.Sprintf("%d", MaxDeadlineHours)) {
			t.Fatalf("el mensaje no nombra el máximo de horas: %q", semantic.Message())
		}
	})
}

// TestWorkflowValidateRejectsDefaultArea pins the shape rule of the optional
// DefaultAreaID (rider R3-2 of review 8fc68db721abce52): a value with
// surrounding whitespace is rejected before it can reach the save path, where it
// would fail as an unknown area instead of as the typo it is.
func TestWorkflowValidateRejectsDefaultArea(t *testing.T) {
	for name, value := range map[string]string{
		"espacio al inicio":       " 4f1c2b3a-0000-4000-8000-000000000001",
		"espacio al final":        "4f1c2b3a-0000-4000-8000-000000000001 ",
		"solo espacios":           "   ",
		"tabulador alrededor":     "\t4f1c2b3a-0000-4000-8000-000000000001",
		"salto de línea al final": "4f1c2b3a-0000-4000-8000-000000000001\n",
	} {
		t.Run(name, func(t *testing.T) {
			cfg := validMinimal()
			cfg.DefaultAreaID = value
			rejectCode(t, cfg.Validate(), CodeInvalidWorkflowArea)
		})
	}

	t.Run("un id sin espacios alrededor se acepta", func(t *testing.T) {
		cfg := validMinimal()
		cfg.DefaultAreaID = "4f1c2b3a-0000-4000-8000-000000000001"
		if err := cfg.Validate(); err != nil {
			t.Fatalf("área por defecto válida rechazada: %v", err)
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

// --- The engine rules (F2 T3) ----------------------------------------------
//
// These are the pure lookups the ticket use cases execute the configured
// workflow with. They live in the domain so the application layer stays a thin
// orchestration layer: "which state is this?", "is this pair a declared
// transition?", "is this state terminal?", "may this role run it?".

func TestWorkflowStateLookup(t *testing.T) {
	cfg := ConsultaTemplate()

	state, ok := cfg.State("atendido")
	if !ok {
		t.Fatal("State(atendido) no encontró un estado declarado")
	}
	if state.Name != "atendido" {
		t.Fatalf("State(atendido).Name = %q", state.Name)
	}

	if _, ok := cfg.State("en-proceso"); ok {
		t.Fatal("State(en-proceso) encontró un estado que la config no declara")
	}
	if _, ok := cfg.State(""); ok {
		t.Fatal("State(\"\") encontró un estado")
	}
}

func TestWorkflowTransitionLookup(t *testing.T) {
	cfg := VentaTemplate()

	transition, ok := cfg.Transition("nuevo", "contactado")
	if !ok {
		t.Fatal("Transition(nuevo→contactado) no encontró la transición declarada")
	}
	if transition.MinRole != TransitionRoleStaff || !transition.RoundRobin {
		t.Fatalf("transición = %+v, se esperaba el rol staff y el round-robin", transition)
	}

	// A pair the config does not declare is not a transition, even if both
	// states exist: the engine never infers the graph.
	for _, pair := range [][2]string{
		{"nuevo", "presupuestado"},
		{"cerrado", "nuevo"},
		{"nuevo", "nuevo"},
		{"no-existe", "contactado"},
	} {
		if _, ok := cfg.Transition(pair[0], pair[1]); ok {
			t.Fatalf("Transition(%s→%s) aceptó un par no declarado", pair[0], pair[1])
		}
	}
}

func TestWorkflowIsTerminal(t *testing.T) {
	t.Run("venta: the two ends of the pipeline are terminal", func(t *testing.T) {
		cfg := VentaTemplate()
		for state, want := range map[string]bool{
			"nuevo":         false,
			"contactado":    false,
			"presupuestado": false,
			"cerrado":       true,
			"perdido":       true,
		} {
			if got := cfg.IsTerminal(state); got != want {
				t.Fatalf("IsTerminal(%q) = %t, esperaba %t", state, got, want)
			}
		}
	})

	t.Run("consulta: resuelto is terminal", func(t *testing.T) {
		cfg := ConsultaTemplate()
		if !cfg.IsTerminal("resuelto") {
			t.Fatal("IsTerminal(resuelto) = false en la plantilla 'consulta'")
		}
		if cfg.IsTerminal("nuevo") || cfg.IsTerminal("atendido") {
			t.Fatal("un estado con transiciones salientes no es terminal")
		}
	})

	t.Run("fail-closed: a state the config does not declare is terminal", func(t *testing.T) {
		// An unknown state has no outgoing transition, so nothing accepts
		// activity on it: the engine treats it as closed instead of guessing
		// that it is open.
		if !VentaTemplate().IsTerminal("archivado") {
			t.Fatal("IsTerminal(estado no declarado) = false, se esperaba fail-closed")
		}
		if !VentaTemplate().IsTerminal("") {
			t.Fatal("IsTerminal(\"\") = false, se esperaba fail-closed")
		}
	})

	t.Run("TerminalStates lists them in declaration order", func(t *testing.T) {
		got := VentaTemplate().TerminalStates()
		want := []string{"cerrado", "perdido"}
		if len(got) != len(want) {
			t.Fatalf("TerminalStates() = %v, esperaba %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("TerminalStates() = %v, esperaba %v", got, want)
			}
		}

		resolved := ConsultaTemplate().TerminalStates()
		if len(resolved) != 1 || resolved[0] != "resuelto" {
			t.Fatalf("TerminalStates() de consulta = %v, esperaba [resuelto]", resolved)
		}
	})
}

// TestTransitionRoleAuthority pins the ascending authority rule of RF6: a
// transition declares the LEAST role that may run it, so a role at or above it
// passes and anything below it — including a client, which is not part of the
// vocabulary at all — is refused.
func TestTransitionRoleAuthority(t *testing.T) {
	cfg := WorkflowConfig{
		TypeName:     "soporte",
		InitialState: "nuevo",
		States:       []StateConfig{{Name: "nuevo"}, {Name: "en-proceso"}, {Name: "escalado"}, {Name: "cerrado"}},
		Transitions: []TransitionConfig{
			{From: "nuevo", To: "en-proceso", MinRole: TransitionRoleStaff},
			{From: "en-proceso", To: "escalado", MinRole: TransitionRoleAdmin},
			{From: "escalado", To: "cerrado", MinRole: TransitionRoleOwner},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config de prueba rechazada: %v", err)
	}

	staffToProcess, _ := cfg.Transition("nuevo", "en-proceso")
	adminToEscalate, _ := cfg.Transition("en-proceso", "escalado")
	ownerToClose, _ := cfg.Transition("escalado", "cerrado")

	cases := []struct {
		name       string
		transition TransitionConfig
		role       TransitionRole
		want       bool
	}{
		{"staff runs a staff transition", staffToProcess, TransitionRoleStaff, true},
		{"admin runs a staff transition", staffToProcess, TransitionRoleAdmin, true},
		{"owner runs a staff transition", staffToProcess, TransitionRoleOwner, true},
		{"staff cannot run an admin transition", adminToEscalate, TransitionRoleStaff, false},
		{"admin runs an admin transition", adminToEscalate, TransitionRoleAdmin, true},
		{"owner runs an admin transition", adminToEscalate, TransitionRoleOwner, true},
		{"admin cannot run an owner transition", ownerToClose, TransitionRoleAdmin, false},
		{"owner runs an owner transition", ownerToClose, TransitionRoleOwner, true},
		// A client is not a transition role at all (RF6 criterion: the attempt
		// is rejected), and neither is an unresolved/forward-compatible value.
		{"client is not a transition role", staffToProcess, TransitionRole("client"), false},
		{"empty role is not a transition role", staffToProcess, TransitionRole(""), false},
		{"unknown role is not a transition role", staffToProcess, TransitionRole("supervisor"), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.transition.CanBeExecutedBy(tc.role); got != tc.want {
				t.Fatalf("CanBeExecutedBy(%q) = %t, esperaba %t", tc.role, got, tc.want)
			}
		})
	}
}
