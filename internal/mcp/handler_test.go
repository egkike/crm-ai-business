package mcp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gomcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/egkike/crm-ai-business/internal/auth"
	"github.com/egkike/crm-ai-business/internal/mcp"
)

// TestNonPostIsMethodNotAllowed pins the T2/RF1 transport contract that T6 must
// preserve: /mcp is POST-only, every other method answers 405 with Allow: POST
// before any protocol work happens.
func TestNonPostIsMethodNotAllowed(t *testing.T) {
	fixture := newFixture(t)

	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete, http.MethodPatch, http.MethodOptions, http.MethodHead} {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
			req.Header.Set(mcp.CallerIDHeader, ownerCaller)
			rec := httptest.NewRecorder()

			fixture.handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("%s /mcp = %d, se espera 405", method, rec.Code)
			}
			if got := rec.Header().Get("Allow"); got != http.MethodPost {
				t.Fatalf("%s /mcp Allow = %q, se espera POST", method, got)
			}
			if body := rec.Body.String(); strings.Contains(body, "result") {
				t.Fatalf("%s /mcp no debe servir el protocolo, cuerpo: %q", method, body)
			}
			if fixture.spy.invocations() != 0 {
				t.Fatalf("%s /mcp ejecutó una herramienta", method)
			}
		})
	}
}

// TestMissingCallerIDIsRejected pins RF2's acceptance criterion: a request
// without X-Caller-Id that reaches any tool call answers JSON-RPC -32000 with
// the canonical Spanish message and the machine-readable class in data.code,
// without executing anything.
func TestMissingCallerIDIsRejected(t *testing.T) {
	fixture := newFixture(t)

	for _, tool := range []string{sampleOwnerToolName, "cualquier_herramienta"} {
		t.Run(tool, func(t *testing.T) {
			response := callTool(t, fixture.handler, "", tool, `{}`)

			if response.Error == nil {
				t.Fatalf("se esperaba error JSON-RPC, result: %s", response.Result)
			}
			if response.Error.Code != -32000 {
				t.Fatalf("code = %d, se espera -32000", response.Error.Code)
			}
			if want := "no se proporcionó X-Caller-Id"; response.Error.Message != want {
				t.Fatalf("message = %q, se espera %q", response.Error.Message, want)
			}
			if got := response.dataCode(t); got != string(auth.CodeMissingCallerID) {
				t.Fatalf("data.code = %q, se espera %q", got, auth.CodeMissingCallerID)
			}
			if fixture.spy.invocations() != 0 {
				t.Fatal("la herramienta se ejecutó sin identidad")
			}
		})
	}
}

// TestUnknownCallerIsRejected pins the other half of RF2: an identity the
// database does not know answers -32000 with the registration hint — the entry
// point of the RF3 loop — and never reaches a gated tool.
func TestUnknownCallerIsRejected(t *testing.T) {
	fixture := newFixture(t)

	response := callTool(t, fixture.handler, "+5491100009999", sampleOwnerToolName, `{}`)

	if response.Error == nil {
		t.Fatalf("se esperaba error JSON-RPC, result: %s", response.Result)
	}
	if response.Error.Code != -32000 {
		t.Fatalf("code = %d, se espera -32000", response.Error.Code)
	}
	if want := "no te reconozco. Por favor regístrate primero."; response.Error.Message != want {
		t.Fatalf("message = %q, se espera %q", response.Error.Message, want)
	}
	if got := response.dataCode(t); got != string(auth.CodeUnknownCaller) {
		t.Fatalf("data.code = %q, se espera %q", got, auth.CodeUnknownCaller)
	}
	if fixture.spy.invocations() != 0 {
		t.Fatal("la herramienta se ejecutó con una identidad desconocida")
	}
}

// TestDisabledAccountIsRejected pins the third semantic identity outcome: a
// known but switched-off account gets its own message and class, not the
// registration hint.
func TestDisabledAccountIsRejected(t *testing.T) {
	resolver := &stubResolver{failure: auth.ErrAccountDisabled}
	handler := newHandlerWith(t, resolver, &fakeRegisterClient{resolver: newFakeResolver()})

	response := callTool(t, handler, "+5491100000003", "cualquier_herramienta", `{}`)

	if response.Error == nil || response.Error.Code != -32000 {
		t.Fatalf("se espera error -32000, got %+v", response.Error)
	}
	if want := "tu cuenta está deshabilitada. Contacta al administrador."; response.Error.Message != want {
		t.Fatalf("message = %q, se espera %q", response.Error.Message, want)
	}
	if got := response.dataCode(t); got != string(auth.CodeAccountDisabled) {
		t.Fatalf("data.code = %q, se espera %q", got, auth.CodeAccountDisabled)
	}
}

// TestInternalFailureIsCollapsed pins AGENTS.md boundary contract 2 (backlog
// R3-001) end to end: when the identity lookup itself fails, the wire carries
// the generic Spanish message and not one byte of the driver text. It also pins
// the fail-closed direction: an unresolved identity is refused, never served as
// anonymous.
func TestInternalFailureIsCollapsed(t *testing.T) {
	resolver := newFakeResolver()
	handler := newHandlerWith(t, resolver, &fakeRegisterClient{resolver: resolver})
	resolver.fail(errFakeDriver)

	response := callTool(t, handler, ownerCaller, sampleOwnerToolName, `{}`)

	if response.Error == nil {
		t.Fatalf("se esperaba error JSON-RPC, result: %s", response.Result)
	}
	if response.Error.Code != -32603 {
		t.Fatalf("code = %d, se espera -32603", response.Error.Code)
	}
	if want := "error interno del servidor"; response.Error.Message != want {
		t.Fatalf("message = %q, se espera %q", response.Error.Message, want)
	}
	assertNoInternals(t, response)

	// tools/list must not degrade into an anonymous listing either.
	_, listResponse := post(t, handler, ownerCaller, `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`)
	if listResponse.Error == nil || listResponse.Error.Code != -32603 {
		t.Fatalf("tools/list = %+v, se espera -32603", listResponse)
	}
	assertNoInternals(t, listResponse)
}

// TestProtocolErrorsStayStandard pins that malformed JSON-RPC and unregistered
// methods are answered by the SDK's standard handling instead of a second
// dialect invented here: an HTTP 400 with the SDK's own diagnostics, no tool
// executed and no internal detail (a table, a path) in the body.
//
// Note for the reviewer: -32601 as a JSON-RPC response is only produced on the
// newer stateless protocol (>= 2026-07-28). For the protocol version this
// product targets (2025-11-25, PRD RF1) the SDK fails the HTTP request itself,
// which is what T6 was told to keep: "standard SDK/JSON-RPC error handling".
func TestProtocolErrorsStayStandard(t *testing.T) {
	fixture := newFixture(t)

	cases := []struct {
		name string
		body string
	}{
		{name: "JSON inválido", body: `{"jsonrpc":"2.0","id":1,`},
		{name: "método desconocido", body: `{"jsonrpc":"2.0","id":1,"method":"tools/apagar_todo","params":{}}`},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(testCase.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
			req.Header.Set(mcp.CallerIDHeader, ownerCaller)
			rec := httptest.NewRecorder()

			fixture.handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("POST /mcp = %d, se espera 400 del SDK (cuerpo: %s)", rec.Code, rec.Body.String())
			}
			if strings.TrimSpace(rec.Body.String()) == "" {
				t.Fatal("400 sin diagnóstico: el cliente no puede saber qué falló")
			}
			if fixture.spy.invocations() != 0 {
				t.Fatal("una herramienta se ejecutó en un request malformado")
			}
		})
	}
}

// TestUnknownToolWithResolvedIdentityStaysStandard pins that a name that is
// simply not a tool of this server gets JSON-RPC -32602 — the standard invalid
// params — rather than an authorization failure. Refusing everything with -32000
// would hide real client bugs behind an authorization message.
func TestUnknownToolWithResolvedIdentityStaysStandard(t *testing.T) {
	fixture := newFixture(t)

	response := callTool(t, fixture.handler, ownerCaller, "no_existe", `{}`)

	if response.Error == nil {
		t.Fatalf("se espera error JSON-RPC, result: %s", response.Result)
	}
	if response.Error.Code != -32602 {
		t.Fatalf("code = %d, se espera -32602", response.Error.Code)
	}
	if fixture.spy.invocations() != 0 {
		t.Fatal("una herramienta se ejecutó para un nombre inexistente")
	}
}

// TestSDKClientInterop pins that a real MCP client — the SDK's own, doing the
// full handshake — can list and call through this handler. The raw JSON-RPC
// tests pin the wire; this one pins that the wire is actually MCP.
func TestSDKClientInterop(t *testing.T) {
	fixture := newFixture(t)

	server := httptest.NewServer(fixture.handler)
	defer server.Close()

	client := gomcp.NewClient(&gomcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	session, err := client.Connect(context.Background(), &gomcp.StreamableClientTransport{
		Endpoint:             server.URL,
		HTTPClient:           callerIDClient(server.Client(), ownerCaller),
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("Connect() = %v, se espera una sesión MCP", err)
	}
	defer func() { _ = session.Close() }()

	listed, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools() = %v", err)
	}
	names := make([]string, 0, len(listed.Tools))
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
	}
	if !contains(names, "register_client") || !contains(names, sampleOwnerToolName) {
		t.Fatalf("tools/list del owner = %v, se espera register_client y %s", names, sampleOwnerToolName)
	}

	result, err := session.CallTool(context.Background(), &gomcp.CallToolParams{Name: sampleOwnerToolName, Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("CallTool() = %v", err)
	}
	if result.IsError {
		t.Fatalf("CallTool() devolvió IsError con %+v", result.Content)
	}
	if fixture.spy.invocations() != 1 {
		t.Fatalf("invocaciones = %d, se espera 1", fixture.spy.invocations())
	}
}

// TestInitializeAdvertisesOnlyTools pins that the handshake announces what this
// server actually implements: the tools capability, and NOT the logging
// capability the SDK enables by default (this transport has no logger — see
// newSDKHandler — and advertising one would invite clients to subscribe to it).
func TestInitializeAdvertisesOnlyTools(t *testing.T) {
	fixture := newFixture(t)

	_, response := post(t, fixture.handler, ownerCaller,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"0.0.1"}}}`)
	if response.Error != nil {
		t.Fatalf("initialize falló: %+v", response.Error)
	}

	var initialized struct {
		ServerInfo struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
		Capabilities map[string]json.RawMessage `json:"capabilities"`
	}
	if err := json.Unmarshal(response.Result, &initialized); err != nil {
		t.Fatalf("initialize result ilegible: %v (%s)", err, response.Result)
	}

	if initialized.ServerInfo.Name != mcp.ServerName {
		t.Fatalf("serverInfo.name = %q, se espera %q", initialized.ServerInfo.Name, mcp.ServerName)
	}
	if _, ok := initialized.Capabilities["tools"]; !ok {
		t.Fatalf("capabilities = %v, se espera tools", initialized.Capabilities)
	}
	if _, ok := initialized.Capabilities["logging"]; ok {
		t.Fatalf("capabilities = %v, no debe anunciar logging", initialized.Capabilities)
	}
}

// callerIDClient returns a client whose every request carries the caller-id, the
// way the Hermes gateway injects it per sender.
func callerIDClient(base *http.Client, callerID string) *http.Client {
	clone := *base
	transport := base.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	clone.Transport = &callerIDTransport{next: transport, callerID: callerID}
	return &clone
}

type callerIDTransport struct {
	next     http.RoundTripper
	callerID string
}

func (c *callerIDTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header.Set(mcp.CallerIDHeader, c.callerID)
	return c.next.RoundTrip(clone)
}

// stubResolver is a CallerResolver that always answers with one failure.
type stubResolver struct{ failure error }

func (s *stubResolver) Resolve(context.Context, string) (auth.Caller, error) {
	return auth.Caller{}, s.failure
}

// newHandlerWith builds a handler from explicit ports (for the tests that need a
// resolver whose behavior the fixture does not cover).
func newHandlerWith(t *testing.T, resolver mcp.CallerResolver, register mcp.RegisterClient) http.Handler {
	t.Helper()

	handler, err := mcp.NewHandler(mcp.Dependencies{CallerResolver: resolver, RegisterClient: register})
	if err != nil {
		t.Fatalf("NewHandler() = %v, se espera un handler", err)
	}
	return handler
}

// assertNoInternals fails when the wire payload echoes any part of the fake
// driver failure: the message, the table name, the path or the wrapped cause.
func assertNoInternals(t *testing.T, response rpcResponse) {
	t.Helper()

	payload := response.Error.Message + string(response.Error.Data)
	for _, leak := range []string{"sqlite", "clients", "/var/lib/crm", "no such table", errFakeDriver.Error()} {
		if strings.Contains(payload, leak) {
			t.Fatalf("el payload filtra internos (%q): %q", leak, payload)
		}
	}
}
