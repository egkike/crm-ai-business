package domain

import (
	"fmt"
	"strings"
)

// WorkflowConfig is the whole declarative workflow of one ticket type (RF6):
// its states, the valid transitions between them, and the rules the engine
// executes — notification on entering/leaving a state, an optional deadline
// per state, round-robin auto-assignment on a transition, and the minimum
// role a transition requires. "Sin scripting ni condiciones libres": every
// rule is one of the declarative fields below, nothing more.
//
// Validate is the fail-closed gate every saved config crosses (RF6: "La config
// inválida se rechaza con error semántico"). A config that does not validate
// is never partially written: the rejection happens before any persistence,
// and the Spanish message names the offending part so the Owner can fix it.
type WorkflowConfig struct {
	// TypeName identifies the ticket type (RF5: "estados/deadlines computan
	// desde la config del tipo"). 1..MaxWorkflowNameLen runes, no surrounding
	// spaces — it is written by internal callers and echoed to the Owner.
	TypeName string

	// DefaultAreaID is the optional area every new ticket of this type starts
	// in (RF7: "cada tipo de ticket declara su área por defecto"). Empty
	// means no fixed area — the 'consulta' seed ships without one. The value
	// is an internal area UUID; Validate only checks the shape of the
	// workflow, never that the area row exists (that is the save-time
	// invariant of the application layer, which owns the repository ports).
	DefaultAreaID string

	// InitialState is the state a new ticket of this type is born in (RF5:
	// "queda en el estado inicial del tipo"). It must name a declared state.
	InitialState string

	// States are the states of the pipeline, in declaration order.
	States []StateConfig

	// Transitions are the valid state changes. An undeclared origin or
	// destination is a config the engine refuses as a whole.
	Transitions []TransitionConfig
}

// StateConfig is one state of a workflow: an optional deadline (RF6: "`deadline`
// por estado con aviso overdue") and the declarative notification rules
// on-enter/on-exit. The engine turns the flags into pending alerts (RF11);
// it never sends messages itself.
type StateConfig struct {
	Name string

	// DeadlineHours, when set, is the hours a ticket may sit in this state
	// before the scheduler emits an overdue alert for the assigned area.
	// Positive; a missing value means the state has no deadline.
	DeadlineHours *int

	// OnEnterAlert generates a pending alert when a ticket enters this state.
	OnEnterAlert bool

	// OnExitAlert generates a pending alert when a ticket leaves this state.
	OnExitAlert bool
}

// TransitionConfig is one valid state change: origin, destination, the
// minimum role authorized to execute it (RF6: "rol autorizado por
// transición") and the optional round-robin auto-assignment of the area
// (RF6: "auto-asignación round-robin del área").
type TransitionConfig struct {
	From string
	To   string

	// MinRole is the least role that may execute the transition. The
	// vocabulary is internal roles only — a client never executes a
	// transition (RF6 criterion: a client attempt is rejected FORBIDDEN).
	MinRole TransitionRole

	// RoundRobin auto-assigns the ticket to the next staff member of its
	// area when the transition executes.
	RoundRobin bool
}

// TransitionRole is the internal-role vocabulary of a transition. It is a
// domain vocabulary on purpose: internal/auth owns caller resolution, and
// the dependency rule forbids the domain from importing it. The application
// layer maps a resolved auth.Role onto these constants.
type TransitionRole string

// The transition roles, in ascending authority.
const (
	TransitionRoleStaff TransitionRole = "staff"
	TransitionRoleAdmin TransitionRole = "admin"
	TransitionRoleOwner TransitionRole = "owner"
)

// validTransitionRole reports whether the role belongs to the vocabulary.
// A "client" or empty role is not a transition role.
func (r TransitionRole) valid() bool {
	switch r {
	case TransitionRoleStaff, TransitionRoleAdmin, TransitionRoleOwner:
		return true
	default:
		return false
	}
}

// The structural caps of a workflow. They keep validation and the engine's
// in-memory passes bounded: a config is declarative, not a scripting surface,
// and a cap is the fail-closed answer to a runaway config (PRD R3:
// "Reglas declarativas acotadas").
const (
	// MaxWorkflowNameLen bounds the type name and every state name (50 runes,
	// the same bound the F1 tool schemas use for names).
	MaxWorkflowNameLen = 50

	// MaxWorkflowStates bounds the states of one type.
	MaxWorkflowStates = 50

	// MaxWorkflowTransitions bounds the transitions of one type.
	MaxWorkflowTransitions = 200
)

// The workflow validation classes. They are the machine-readable contract a
// transport (and the F4 panel) branches on; the Spanish prose travels in the
// message.
const (
	// CodeInvalidWorkflowName: the type name violates its rule.
	CodeInvalidWorkflowName ErrorCode = "invalid_workflow_name"

	// CodeInvalidWorkflowState: a state rule (none declared, empty or
	// duplicated name, oversized name, non-positive deadline, over the cap).
	CodeInvalidWorkflowState ErrorCode = "invalid_workflow_state"

	// CodeInvalidWorkflowInitial: the initial state is missing or is not a
	// declared state (RF6: the config is rejected as a whole).
	CodeInvalidWorkflowInitial ErrorCode = "invalid_workflow_initial_state"

	// CodeInvalidWorkflowTransition: a transition rule (empty origin or
	// destination, undeclared state, self-loop, duplicated pair, invalid
	// role, over the cap).
	CodeInvalidWorkflowTransition ErrorCode = "invalid_workflow_transition"
)

// Validate checks the whole config and rejects it with a semantic error if
// any rule is violated. Rules run in a fixed order (name, states, initial,
// transitions) so the first rejection is deterministic; every rule rejects
// before anything is persisted, which is what "rechaza la config completa y
// nada queda parcialmente escrito" requires.
func (c WorkflowConfig) Validate() error {
	if err := c.validateName(); err != nil {
		return err
	}
	stateIndex, err := c.validateStates()
	if err != nil {
		return err
	}
	if err := c.validateInitial(stateIndex); err != nil {
		return err
	}
	return c.validateTransitions(stateIndex)
}

// validateName enforces the type-name rules.
func (c WorkflowConfig) validateName() error {
	if c.TypeName == "" {
		return NewSemanticError(CodeInvalidWorkflowName,
			"el nombre del tipo de ticket es obligatorio.")
	}
	if strings.TrimSpace(c.TypeName) != c.TypeName {
		return NewSemanticError(CodeInvalidWorkflowName,
			"el nombre del tipo de ticket no puede empezar ni terminar con espacios.")
	}
	if len([]rune(c.TypeName)) > MaxWorkflowNameLen {
		return NewSemanticError(CodeInvalidWorkflowName, fmt.Sprintf(
			"el nombre del tipo de ticket supera el máximo de %d caracteres.", MaxWorkflowNameLen))
	}
	return nil
}

// validateStates enforces every state rule and returns the declared-state
// index the initial and transition rules resolve against.
func (c WorkflowConfig) validateStates() (map[string]bool, error) {
	if len(c.States) == 0 {
		return nil, NewSemanticError(CodeInvalidWorkflowState,
			"el workflow necesita al menos un estado.")
	}
	if len(c.States) > MaxWorkflowStates {
		return nil, NewSemanticError(CodeInvalidWorkflowState, fmt.Sprintf(
			"el workflow supera el máximo de %d estados.", MaxWorkflowStates))
	}

	index := make(map[string]bool, len(c.States))
	for _, state := range c.States {
		if state.Name == "" {
			return nil, NewSemanticError(CodeInvalidWorkflowState,
				"el nombre del estado es obligatorio.")
		}
		if strings.TrimSpace(state.Name) != state.Name {
			return nil, NewSemanticError(CodeInvalidWorkflowState,
				"el nombre de un estado no puede empezar ni terminar con espacios.")
		}
		if len([]rune(state.Name)) > MaxWorkflowNameLen {
			return nil, NewSemanticError(CodeInvalidWorkflowState, fmt.Sprintf(
				"el nombre de un estado supera el máximo de %d caracteres.", MaxWorkflowNameLen))
		}
		if index[state.Name] {
			return nil, NewSemanticError(CodeInvalidWorkflowState, fmt.Sprintf(
				"hay dos estados con el nombre '%s': cada estado debe ser único.", state.Name))
		}
		if state.DeadlineHours != nil && *state.DeadlineHours < 1 {
			return nil, NewSemanticError(CodeInvalidWorkflowState, fmt.Sprintf(
				"el deadline del estado '%s' debe ser de al menos una hora.", state.Name))
		}
		index[state.Name] = true
	}
	return index, nil
}

// validateInitial enforces the initial-state rule against the declared
// states.
func (c WorkflowConfig) validateInitial(stateIndex map[string]bool) error {
	if c.InitialState == "" {
		return NewSemanticError(CodeInvalidWorkflowInitial,
			"el workflow debe declarar su estado inicial.")
	}
	if !stateIndex[c.InitialState] {
		return NewSemanticError(CodeInvalidWorkflowInitial, fmt.Sprintf(
			"el estado inicial '%s' no existe en el workflow.", c.InitialState))
	}
	return nil
}

// validateTransitions enforces every transition rule against the declared
// states.
func (c WorkflowConfig) validateTransitions(stateIndex map[string]bool) error {
	if len(c.Transitions) > MaxWorkflowTransitions {
		return NewSemanticError(CodeInvalidWorkflowTransition, fmt.Sprintf(
			"el workflow supera el máximo de %d transiciones.", MaxWorkflowTransitions))
	}

	seen := make(map[string]bool, len(c.Transitions))
	for _, tr := range c.Transitions {
		if tr.From == "" || tr.To == "" {
			return NewSemanticError(CodeInvalidWorkflowTransition,
				"la transición debe declarar estado de origen y destino.")
		}
		if tr.From == tr.To {
			return NewSemanticError(CodeInvalidWorkflowTransition, fmt.Sprintf(
				"la transición de '%s' no puede volver al mismo estado.", tr.From))
		}
		if !stateIndex[tr.From] {
			return NewSemanticError(CodeInvalidWorkflowTransition, fmt.Sprintf(
				"el estado de origen '%s' no existe en el workflow.", tr.From))
		}
		if !stateIndex[tr.To] {
			return NewSemanticError(CodeInvalidWorkflowTransition, fmt.Sprintf(
				"el estado de destino '%s' no existe en el workflow.", tr.To))
		}
		pair := tr.From + "\x00" + tr.To
		if seen[pair] {
			return NewSemanticError(CodeInvalidWorkflowTransition, fmt.Sprintf(
				"hay dos transiciones de '%s' hacia '%s': cada par origen-destino debe ser único.",
				tr.From, tr.To))
		}
		seen[pair] = true
		if !tr.MinRole.valid() {
			return NewSemanticError(CodeInvalidWorkflowTransition, fmt.Sprintf(
				"la transición de '%s' hacia '%s' declara un rol autorizado inválido.", tr.From, tr.To))
		}
	}
	return nil
}

// VentaTemplate is the 'venta' seed pipeline of PRD §3.7: "'venta': nuevo →
// contactado → presupuestado → cerrado/perdido, área Ventas con round-robin,
// evento 'link enviado'". Every transition out of the initial state carries
// the round-robin rule so a new sale is assigned to the next staff member of
// its area. DefaultAreaID ships empty: the seeding migration links the type
// to the 'Ventas' area row it creates (the domain never knows row ids).
// The 'link enviado' event vocabulary is the engine's concern (RF23 'venta'
// template), not a workflow rule.
func VentaTemplate() WorkflowConfig {
	return WorkflowConfig{
		TypeName:     "venta",
		InitialState: "nuevo",
		States: []StateConfig{
			{Name: "nuevo"},
			{Name: "contactado"},
			{Name: "presupuestado"},
			{Name: "cerrado"},
			{Name: "perdido"},
		},
		Transitions: []TransitionConfig{
			{From: "nuevo", To: "contactado", MinRole: TransitionRoleStaff, RoundRobin: true},
			{From: "contactado", To: "presupuestado", MinRole: TransitionRoleStaff},
			{From: "presupuestado", To: "cerrado", MinRole: TransitionRoleStaff},
			{From: "presupuestado", To: "perdido", MinRole: TransitionRoleStaff},
		},
	}
}

// ConsultaTemplate is the 'consulta' seed pipeline of PRD §3.7: "'consulta':
// nuevo → atendido → resuelto, sin área fija". It is the F1 'consulta' type,
// reborn as a configurable workflow: no default area, no round-robin — an
// unassigned consultation stays in the area's queue for anyone to take.
func ConsultaTemplate() WorkflowConfig {
	return WorkflowConfig{
		TypeName:     "consulta",
		InitialState: "nuevo",
		States: []StateConfig{
			{Name: "nuevo"},
			{Name: "atendido"},
			{Name: "resuelto"},
		},
		Transitions: []TransitionConfig{
			{From: "nuevo", To: "atendido", MinRole: TransitionRoleStaff},
			{From: "atendido", To: "resuelto", MinRole: TransitionRoleStaff},
		},
	}
}
