package mcp_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/egkike/crm-ai-business/internal/application/usecase"
	"github.com/egkike/crm-ai-business/internal/auth"
)

// TestRegisterClientLoopOverHTTP pins the transport half of the F1 conversational
// loop: an unknown sender is told it is unknown, registers through the anonymous
// seam over the real HTTP surface, and the very next tool call resolves as a
// client — the identity chain of PRD §3.8 closes end to end.
func TestRegisterClientLoopOverHTTP(t *testing.T) {
	fixture := newFixture(t)
	const sender = "+5491100077777"

	// 1. Unknown sender: the gateway learns it must register.
	refusal := callTool(t, fixture.handler, sender, sampleClientToolName, `{}`)
	if refusal.Error == nil || refusal.Error.Code != -32000 {
		t.Fatalf("antes del registro se espera -32000, got %+v", refusal.Error)
	}
	if got := refusal.dataCode(t); got != string(auth.CodeUnknownCaller) {
		t.Fatalf("data.code = %q, se espera %q", got, auth.CodeUnknownCaller)
	}

	// 2. The sender can discover the registration seam even while unknown.
	if names := listTools(t, fixture.handler, sender); !contains(names, "register_client") {
		t.Fatalf("tools/list del desconocido = %v, se espera register_client", names)
	}

	// 3. Registration through the anonymous seam.
	registered := callTool(t, fixture.handler, sender, "register_client", `{"display_name":"Ana Pérez"}`)
	if registered.Error != nil {
		t.Fatalf("register_client = %+v, se espera éxito", registered.Error)
	}

	var output struct {
		ClientID    string `json:"client_id"`
		DisplayName string `json:"display_name"`
		Created     bool   `json:"created"`
	}
	decodeStructured(t, registered, &output)
	if !output.Created {
		t.Fatalf("created = false, se espera true en el primer registro")
	}
	if output.ClientID == "" {
		t.Fatal("client_id vacío: el contrato pide el UUID interno")
	}
	if output.DisplayName != "Ana Pérez" {
		t.Fatalf("display_name = %q, se espera el nombre enviado", output.DisplayName)
	}

	// 4. The sender now resolves as a client and reaches the client tool.
	after := callTool(t, fixture.handler, sender, sampleClientToolName, `{}`)
	if after.Error != nil {
		t.Fatalf("después del registro se espera éxito, error: %+v", after.Error)
	}
	if names := listTools(t, fixture.handler, sender); !contains(names, sampleClientToolName) {
		t.Fatalf("tools/list del cliente = %v, se espera %q", names, sampleClientToolName)
	}
}

// TestRegisterClientIsIdempotent pins RF3's retry contract at the transport
// level: repeating the call answers created=false instead of failing or
// duplicating, and the caller-id travels verbatim (no trimming).
func TestRegisterClientIsIdempotent(t *testing.T) {
	fixture := newFixture(t)
	const sender = "cliente@ejemplo.com"

	first := callTool(t, fixture.handler, sender, "register_client", `{"display_name":"Ana"}`)
	if first.Error != nil {
		t.Fatalf("primer registro = %+v", first.Error)
	}
	second := callTool(t, fixture.handler, sender, "register_client", `{"display_name":"Ana"}`)
	if second.Error != nil {
		t.Fatalf("segundo registro = %+v", second.Error)
	}
	if createdOf(t, second) {
		t.Fatal("created = true en el segundo registro; el reintento no debe duplicar")
	}

	calls := fixture.register.recordedCalls()
	if len(calls) != 2 {
		t.Fatalf("llamadas al caso de uso = %d, se esperan 2", len(calls))
	}
	for _, call := range calls {
		if call.CallerID != sender {
			t.Fatalf("caller-id entregado = %q, se espera %q verbatim", call.CallerID, sender)
		}
	}
}

// TestRegisterClientWithoutArguments pins the optional argument: the tool runs
// and lets the use case decide the default name.
func TestRegisterClientWithoutArguments(t *testing.T) {
	fixture := newFixture(t)
	const sender = "+5491100066666"

	for _, arguments := range []string{"", `{}`, `null`} {
		t.Run("args="+arguments, func(t *testing.T) {
			response := callTool(t, fixture.handler, sender, "register_client", arguments)
			if response.Error != nil {
				t.Fatalf("register_client = %+v, se espera éxito", response.Error)
			}
		})
	}

	for _, call := range fixture.register.recordedCalls() {
		if call.DisplayName != "" {
			t.Fatalf("display_name = %q, se espera vacío para que el caso de uso aplique el default", call.DisplayName)
		}
	}
}

// TestRegisterClientRejectsInjectedArguments pins the input schema of the
// anonymous seam: the only accepted key is display_name. A role-ish key is
// REFUSED, not ignored — ignoring it would let a caller believe a role or
// identifier was accepted (PRD §3.8 anti-injection), and the use case must never
// see the attempt.
func TestRegisterClientRejectsInjectedArguments(t *testing.T) {
	cases := []struct {
		name      string
		arguments string
	}{
		{name: "rol", arguments: `{"display_name":"Ana","role":"owner"}`},
		{name: "identificador ajeno", arguments: `{"client_id":"33333333-3333-4333-8333-333333333333"}`},
		{name: "modo admin", arguments: `{"mode":"admin"}`},
		{name: "display_name no textual", arguments: `{"display_name":42}`},
		{name: "display_name demasiado largo", arguments: `{"display_name":"` + strings.Repeat("a", 121) + `"}`},
		{name: "argumentos que no son objeto", arguments: `["role"]`},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newFixture(t)

			response := callTool(t, fixture.handler, "+5491100055555", "register_client", testCase.arguments)

			if response.Error == nil {
				t.Fatalf("se espera error JSON-RPC, result: %s", response.Result)
			}
			if response.Error.Code != -32602 {
				t.Fatalf("code = %d, se espera -32602", response.Error.Code)
			}
			if got := response.dataCode(t); got != "invalid_arguments" {
				t.Fatalf("data.code = %q, se espera invalid_arguments", got)
			}
			if calls := fixture.register.recordedCalls(); len(calls) != 0 {
				t.Fatalf("el caso de uso se ejecutó pese a los argumentos inválidos: %+v", calls)
			}
		})
	}
}

// TestRegisterClientSemanticErrorIsMapped pins the -32002 class: a business
// rejection keeps the Spanish message the use case phrased and its code, and
// nothing is retried or rewritten on the way out.
func TestRegisterClientSemanticErrorIsMapped(t *testing.T) {
	fixture := newFixture(t)
	fixture.register.setFailure(usecase.ErrIdentifierTaken)

	response := callTool(t, fixture.handler, "+5491100044444", "register_client", `{}`)

	if response.Error == nil {
		t.Fatalf("se espera error JSON-RPC, result: %s", response.Result)
	}
	if response.Error.Code != -32002 {
		t.Fatalf("code = %d, se espera -32002", response.Error.Code)
	}
	if want := "ya existe un cliente con ese dato. Contactá al negocio."; response.Error.Message != want {
		t.Fatalf("message = %q, se espera %q", response.Error.Message, want)
	}
	if got := response.dataCode(t); got != string(usecase.CodeIdentifierTaken) {
		t.Fatalf("data.code = %q, se espera %q", got, usecase.CodeIdentifierTaken)
	}
}

// TestRegisterClientInternalErrorIsCollapsed pins AGENTS.md boundary contract 2
// on the registration path: a driver failure inside the use case reaches the wire
// as the generic Spanish message, and the fake driver text is nowhere in the
// payload.
func TestRegisterClientInternalErrorIsCollapsed(t *testing.T) {
	fixture := newFixture(t)
	fixture.register.setFailure(errFakeDriver)

	response := callTool(t, fixture.handler, "+5491100033333", "register_client", `{}`)

	if response.Error == nil {
		t.Fatalf("se espera error JSON-RPC, result: %s", response.Result)
	}
	if response.Error.Code != -32603 {
		t.Fatalf("code = %d, se espera -32603", response.Error.Code)
	}
	if want := "error interno del servidor"; response.Error.Message != want {
		t.Fatalf("message = %q, se espera %q", response.Error.Message, want)
	}
	assertNoInternals(t, response)
	if got := response.dataCode(t); got != "" {
		t.Fatalf("data.code = %q, se espera vacío en un error interno", got)
	}
}

// TestRegisterClientWithoutCallerIDIsRejected pins that the anonymous seam is not
// an identity bypass: with no X-Caller-Id there is no identifier to register and
// the call fails closed with RF2's -32000.
func TestRegisterClientWithoutCallerIDIsRejected(t *testing.T) {
	fixture := newFixture(t)

	response := callTool(t, fixture.handler, "", "register_client", `{"display_name":"Ana"}`)

	if response.Error == nil {
		t.Fatalf("se espera error JSON-RPC, result: %s", response.Result)
	}
	if response.Error.Code != -32000 {
		t.Fatalf("code = %d, se espera -32000", response.Error.Code)
	}
	if got := response.dataCode(t); got != string(auth.CodeMissingCallerID) {
		t.Fatalf("data.code = %q, se espera %q", got, auth.CodeMissingCallerID)
	}
	if calls := fixture.register.recordedCalls(); len(calls) != 0 {
		t.Fatalf("el caso de uso se ejecutó sin identidad: %+v", calls)
	}
}

// TestRegisterClientDeclaresNoRoleInput pins the anti-injection invariant at the
// schema level: the announced input schema has exactly one property and forbids
// additional ones, so a client cannot even construct a call carrying a role.
func TestRegisterClientDeclaresNoRoleInput(t *testing.T) {
	fixture := newFixture(t)

	_, response := post(t, fixture.handler, ownerCaller, `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	if response.Error != nil {
		t.Fatalf("tools/list falló: %+v", response.Error)
	}

	var listed struct {
		Tools []struct {
			Name        string `json:"name"`
			InputSchema struct {
				Type                 string                     `json:"type"`
				Properties           map[string]json.RawMessage `json:"properties"`
				AdditionalProperties bool                       `json:"additionalProperties"`
			} `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(response.Result, &listed); err != nil {
		t.Fatalf("tools/list ilegible: %v", err)
	}

	var found bool
	for _, tool := range listed.Tools {
		if tool.Name != "register_client" {
			continue
		}
		found = true
		if tool.InputSchema.Type != "object" {
			t.Fatalf("inputSchema.type = %q, se espera object", tool.InputSchema.Type)
		}
		if tool.InputSchema.AdditionalProperties {
			t.Fatal("inputSchema.additionalProperties debe ser false")
		}
		if len(tool.InputSchema.Properties) != 1 {
			t.Fatalf("inputSchema acepta %d propiedades, se espera solo display_name", len(tool.InputSchema.Properties))
		}
		if _, ok := tool.InputSchema.Properties["display_name"]; !ok {
			t.Fatalf("inputSchema.properties = %v, se espera display_name", tool.InputSchema.Properties)
		}
	}
	if !found {
		t.Fatal("register_client no aparece en tools/list")
	}
}

// createdOf decodes created from a tools/call result.
func createdOf(t *testing.T, response rpcResponse) bool {
	t.Helper()

	var output struct {
		Created bool `json:"created"`
	}
	decodeStructured(t, response, &output)
	return output.Created
}

// decodeStructured decodes the structuredContent of a tools/call result into
// target. Going through structuredContent (instead of the raw result) is what
// makes the assertion meaningful: decoding the envelope into a payload struct
// would silently succeed with zero values.
func decodeStructured(t *testing.T, response rpcResponse, target any) {
	t.Helper()

	var shaped callToolResult
	if err := json.Unmarshal(response.Result, &shaped); err != nil {
		t.Fatalf("result ilegible: %v (%s)", err, response.Result)
	}
	if shaped.IsError {
		t.Fatalf("la llamada respondió isError: %+v", shaped.Content)
	}
	if len(shaped.StructuredContent) == 0 {
		t.Fatalf("la llamada no trae structuredContent: %s", response.Result)
	}
	if err := json.Unmarshal(shaped.StructuredContent, target); err != nil {
		t.Fatalf("structuredContent ilegible: %v (%s)", err, shaped.StructuredContent)
	}
}
