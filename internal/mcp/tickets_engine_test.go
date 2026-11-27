package mcp_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/egkike/crm-ai-business/internal/application/usecase"
	"github.com/egkike/crm-ai-business/internal/domain"
)

// This file pins the F2 T3 wire contract: the ticket tools execute the configured
// workflow (S1/S2), the transition's vocabulary is refuted by the use case instead
// of by a decoder enum (rider R1-transition-state-decode), and the wire carries
// the configured facts (type name, area, deadline, closing instant) the agent
// needs to operate the pipeline.

// TestCreateTicketWithAConfiguredType pins the creation over the wire: the type
// the business configured decides the initial state, the area and the deadline,
// and the wire shows all three.
func TestCreateTicketWithAConfiguredType(t *testing.T) {
	fixture := newTicketsFixture(t)

	created := ticketCallResult(t, callTool(t, fixture.handler, emailCaller, createTicketTool,
		`{"motive":"quiero un presupuesto","ticket_type":"venta"}`))

	if created.TicketType != "venta" {
		t.Fatalf("ticket_type = %q, se esperaba 'venta'", created.TicketType)
	}
	if created.State != "nuevo" {
		t.Fatalf("state = %q, se esperaba el estado inicial de 'venta'", created.State)
	}
	if created.AreaID != ventasAreaID {
		t.Fatalf("area_id = %q, se esperaba el área por defecto de 'venta'", created.AreaID)
	}
	if created.AssignedTo != "" {
		t.Fatalf("assigned_to = %q, un ticket nace sin asignar", created.AssignedTo)
	}

	// The round-robin out of 'nuevo' assigns the ticket, and the wire shows it.
	contacted := ticketCallResult(t, callTool(t, fixture.handler, staffCaller, transitionTicketTool,
		`{"ticket_id":"`+created.ID+`","state":"contactado"}`))
	if contacted.State != "contactado" {
		t.Fatalf("state = %q, se esperaba 'contactado'", contacted.State)
	}
	if contacted.AssignedTo != staffAccountID {
		t.Fatalf("assigned_to = %q, se esperaba el miembro del área %q", contacted.AssignedTo, staffAccountID)
	}
}

// TestUnknownTicketTypeIsASemanticRejection pins the fail-closed creation: a type
// nobody configured is a business rejection with its own code, never a fallback to
// the retired fixed pipeline.
func TestUnknownTicketTypeIsASemanticRejection(t *testing.T) {
	fixture := newTicketsFixture(t)

	response := callTool(t, fixture.handler, emailCaller, createTicketTool,
		`{"motive":"consulta","ticket_type":"inexistente"}`)
	assertSemanticWire(t, response, usecase.CodeUnknownTicketType, "no existe el tipo de ticket 'inexistente'.")
}

// TestTransitionEnforcesTheConfiguredRoleOverTheWire pins the RF6 criterion as a
// caller experiences it: "Dada una transición que exige rol `staff`+, cuando un
// `client` intenta ejecutarla por chat, entonces es rechazada con `FORBIDDEN`
// semántico" — and, for an internal caller that is still below the transition's
// minimum, the engine's own role rejection.
func TestTransitionEnforcesTheConfiguredRoleOverTheWire(t *testing.T) {
	fixture := newTicketsFixture(t)
	fixture.store.workflows.configs["escalado"] = domain.WorkflowConfig{
		TypeName:     "escalado",
		InitialState: "nuevo",
		States:       []domain.StateConfig{{Name: "nuevo"}, {Name: "revisado"}, {Name: "escalado"}},
		Transitions: []domain.TransitionConfig{
			{From: "nuevo", To: "revisado", MinRole: domain.TransitionRoleStaff},
			{From: "revisado", To: "escalado", MinRole: domain.TransitionRoleAdmin},
		},
	}

	created := ticketCallResult(t, callTool(t, fixture.handler, emailCaller, createTicketTool,
		`{"motive":"necesito escalar un caso","ticket_type":"escalado"}`))

	// The client cannot execute ANY transition: the tool is not even announced to
	// it, and the registry refuses the call before a handler runs.
	clientAttempt := callTool(t, fixture.handler, emailCaller, transitionTicketTool,
		`{"ticket_id":"`+created.ID+`","state":"revisado"}`)
	if clientAttempt.Error == nil || clientAttempt.Error.Code != -32000 {
		t.Fatalf("respuesta del cliente = %+v, se esperaba -32000", clientAttempt.Error)
	}
	if got := clientAttempt.dataCode(t); got != "role_not_allowed" {
		t.Fatalf("data.code = %q, se esperaba role_not_allowed", got)
	}

	// A staff caller may move staff→ transitions but not the admin-gated one.
	if err := assertTransitionApplied(t, fixture, created.ID, "revisado"); err != nil {
		t.Fatalf("la transición de staff debía ejecutarse: %v", err)
	}
	staffBelowAdmin := callTool(t, fixture.handler, staffCaller, transitionTicketTool,
		`{"ticket_id":"`+created.ID+`","state":"escalado"}`)
	assertSemanticWire(t, staffBelowAdmin, usecase.CodeRoleNotAllowed,
		"tu rol no permite pasar un ticket del estado 'revisado' al 'escalado': "+
			"el workflow del tipo 'escalado' reserva esa transición al rol admin o superior.")
}

// assertTransitionApplied runs one transition as the staff caller and reports the
// wire rejection verbatim when it was refused.
func assertTransitionApplied(t *testing.T, fixture *ticketsFixture, ticketID, state string) error {
	t.Helper()

	response := callTool(t, fixture.handler, staffCaller, transitionTicketTool,
		`{"ticket_id":"`+ticketID+`","state":"`+state+`"}`)
	if response.Error != nil {
		return errors.New(response.Error.Message)
	}
	return nil
}

// TestTransitionStateIsNotADecoderEnum pins rider R1-transition-state-decode: the
// announced schema declares the state as a plain string, because the vocabulary is
// configured per ticket type and a static enum would be a lie the engine then has
// to contradict (the use case is the gate).
func TestTransitionStateIsNotADecoderEnum(t *testing.T) {
	fixture := newTicketsFixture(t)

	tools := announcedToolDefinitions(t, fixture.handler, staffCaller)
	definition, ok := tools[transitionTicketTool]
	if !ok {
		t.Fatalf("tools/list no anunció %q", transitionTicketTool)
	}

	var schema struct {
		Properties map[string]struct {
			Type string   `json:"type"`
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(definition.InputSchema, &schema); err != nil {
		t.Fatalf("inputSchema de %q ilegible: %v", transitionTicketTool, err)
	}
	state, ok := schema.Properties["state"]
	if !ok {
		t.Fatal("el inputSchema de transition_ticket no declara 'state'")
	}
	if state.Type != "string" {
		t.Fatalf("state.type = %q, se esperaba string", state.Type)
	}
	if len(state.Enum) != 0 {
		t.Fatalf("state.enum = %v, la vocabulario es configurable y no puede anunciarse como enum", state.Enum)
	}
}

// TestTicketOutputCarriesTheConfiguredFacts pins the wire fields the F2 pipeline
// needs: the type by NAME, the area, the deadline and the closing instant.
func TestTicketOutputCarriesTheConfiguredFacts(t *testing.T) {
	fixture := newTicketsFixture(t)
	fixture.store.workflows.configs["con-deadline"] = domain.WorkflowConfig{
		TypeName:      "con-deadline",
		DefaultAreaID: ventasAreaID,
		InitialState:  "esperando",
		States: []domain.StateConfig{
			{Name: "esperando", DeadlineHours: func() *int { value := 24; return &value }()},
			{Name: "listo"},
		},
		Transitions: []domain.TransitionConfig{
			{From: "esperando", To: "listo", MinRole: domain.TransitionRoleStaff},
		},
	}

	created := ticketCallResult(t, callTool(t, fixture.handler, emailCaller, createTicketTool,
		`{"motive":"necesito el turno","ticket_type":"con-deadline"}`))
	if created.DeadlineAt == "" {
		t.Fatal("deadline_at vacío: el estado inicial declara 24 horas")
	}
	if created.ClosedAt != "" {
		t.Fatalf("closed_at = %q, el ticket nace abierto", created.ClosedAt)
	}

	done := ticketCallResult(t, callTool(t, fixture.handler, staffCaller, transitionTicketTool,
		`{"ticket_id":"`+created.ID+`","state":"listo"}`))
	if done.DeadlineAt != "" {
		t.Fatalf("deadline_at = %q, 'listo' no declara deadline y debe limpiarlo", done.DeadlineAt)
	}
	if done.ClosedAt == "" {
		t.Fatal("closed_at vacío: 'listo' es terminal en su configuración")
	}

	// The closed conversation refuses a note with the Spanish rejection of RF5.
	response := callTool(t, fixture.handler, emailCaller, addTicketNoteTool,
		`{"ticket_id":"`+created.ID+`","text":"sigo esperando"}`)
	assertSemanticWire(t, response, usecase.CodeTicketClosed,
		"el ticket está cerrado: una conversación cerrada no se reabre con una nota. "+
			"Si el cliente vuelve, se abre un ticket nuevo.")
}

// announcedToolDefinition is the announced shape of one tool.
type announcedToolDefinition struct {
	Name        string          `json:"name"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// announcedToolDefinitions indexes the tools a caller is told about by name, with
// their input schemas, so a test can pin the announced contract itself.
func announcedToolDefinitions(t *testing.T, handler http.Handler, callerID string) map[string]announcedToolDefinition {
	t.Helper()

	_, response := post(t, handler, callerID, `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	if response.Error != nil {
		t.Fatalf("tools/list falló: %+v", response.Error)
	}

	var result struct {
		Tools []announcedToolDefinition `json:"tools"`
	}
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatalf("tools/list result ilegible: %v (%s)", err, response.Result)
	}

	definitions := make(map[string]announcedToolDefinition, len(result.Tools))
	for _, definition := range result.Tools {
		definitions[definition.Name] = definition
	}
	return definitions
}
