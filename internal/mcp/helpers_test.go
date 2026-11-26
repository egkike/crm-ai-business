package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	gomcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/egkike/crm-ai-business/internal/application/dto"
	"github.com/egkike/crm-ai-business/internal/auth"
	"github.com/egkike/crm-ai-business/internal/mcp"
)

// The identities the fixtures know about. The phone shapes are what the gateway
// injects for WhatsApp; the e-mail shape is the other channel.
const (
	ownerCaller = "+5491100000001"
	staffCaller = "+5491100000002"
	emailCaller = "cliente@ejemplo.com"
)

// fakeResolver is a CallerResolver over an in-memory table of identities. It is
// the transport's view of internal/auth: a port that either resolves, rejects
// semantically, or fails internally.
type fakeResolver struct {
	mu       sync.Mutex
	ids      map[string]auth.Caller
	failWith error
}

func newFakeResolver() *fakeResolver {
	return &fakeResolver{ids: map[string]auth.Caller{
		ownerCaller: {ID: "11111111-1111-4111-8111-111111111111", Name: "Dueña", Role: auth.RoleOwner},
		staffCaller: {ID: "22222222-2222-4222-8222-222222222222", Name: "Staff", Role: auth.RoleStaff},
	}}
}

func (r *fakeResolver) Resolve(_ context.Context, callerID string) (auth.Caller, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.failWith != nil {
		return auth.Caller{}, r.failWith
	}
	if callerID == "" {
		return auth.Caller{}, auth.ErrMissingCallerID
	}
	caller, ok := r.ids[callerID]
	if !ok {
		return auth.Caller{}, auth.ErrUnknownCaller
	}
	return caller, nil
}

// add registers an identity so a later request resolves it.
func (r *fakeResolver) add(callerID string, caller auth.Caller) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ids[callerID] = caller
}

func (r *fakeResolver) fail(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failWith = err
}

// fakeRegisterClient stands in for *usecase.RegisterClient: it records what the
// transport handed it and answers with the same semantic failures the real use
// case produces.
type fakeRegisterClient struct {
	resolver *fakeResolver

	mu       sync.Mutex
	calls    []dto.RegisterClientCommand
	failWith error
}

func (f *fakeRegisterClient) Execute(_ context.Context, cmd dto.RegisterClientCommand) (dto.RegisterClientResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls = append(f.calls, cmd)

	if f.failWith != nil {
		return dto.RegisterClientResult{}, f.failWith
	}
	if cmd.CallerID == "" {
		return dto.RegisterClientResult{}, auth.ErrMissingCallerID
	}

	// Idempotent admission, like the real use case: an identifier that already
	// resolves is the same client, not a new row.
	existing, err := f.resolver.Resolve(context.Background(), cmd.CallerID)
	if err == nil {
		return dto.RegisterClientResult{ClientID: existing.ID, DisplayName: cmd.DisplayName, Created: false}, nil
	}

	const newID = "33333333-3333-4333-8333-333333333333"
	f.resolver.add(cmd.CallerID, auth.Caller{ID: newID, Name: cmd.DisplayName, Role: auth.RoleClient})
	return dto.RegisterClientResult{ClientID: newID, DisplayName: cmd.DisplayName, Created: true}, nil
}

func (f *fakeRegisterClient) recordedCalls() []dto.RegisterClientCommand {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]dto.RegisterClientCommand(nil), f.calls...)
}

func (f *fakeRegisterClient) setFailure(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failWith = err
}

// ports are the two driven ports the transport needs, plus the resolver's
// in-memory identity table (which registration mutates).
type ports struct {
	resolver *fakeResolver
	register *fakeRegisterClient
}

func newPorts() *ports {
	resolver := newFakeResolver()
	return &ports{resolver: resolver, register: &fakeRegisterClient{resolver: resolver}}
}

// handler builds the transport over these ports with extra tools.
func (p *ports) handler(t *testing.T, tools ...mcp.Tool) http.Handler {
	t.Helper()

	handler, err := mcp.NewHandlerWithTools(
		mcp.Dependencies{CallerResolver: p.resolver, RegisterClient: p.register},
		tools...,
	)
	if err != nil {
		t.Fatalf("NewHandlerWithTools() = %v, se espera un handler", err)
	}
	return handler
}

// fixture is a handler plus the ports behind it, so a test can observe both the
// wire and the layer underneath.
type fixture struct {
	*ports
	handler http.Handler
	spy     *spyTool
}

// sampleOwnerTool is the owner-only tool the S2 criterion is pinned against: a
// staff, client or unknown caller must neither see it nor be able to invoke it.
const sampleOwnerToolName = "sample_owner_tool"

// sampleClientTool stands in for the client-facing tools of later phases, so a
// client-role caller has something of its own to see.
const sampleClientToolName = "sample_client_tool"

// sampleAnonymousTool declares no access at all — the zero Access — to pin the
// fail-closed default: invisible to everyone, refused on invocation.
const sampleAnonymousToolName = "sample_undeclared_tool"

// spyTool records whether its handler ever ran, which is what "refused before
// executing anything" means in practice.
type spyTool struct {
	mu     sync.Mutex
	called int
	panics bool
}

func (s *spyTool) handler() gomcp.ToolHandler {
	return func(context.Context, *gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		s.mu.Lock()
		s.called++
		panics := s.panics
		s.mu.Unlock()
		if panics {
			panic("falla simulada dentro de la herramienta")
		}
		return &gomcp.CallToolResult{Content: []gomcp.Content{&gomcp.TextContent{Text: "ok"}}}, nil
	}
}

func (s *spyTool) invocations() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.called
}

func (s *spyTool) makePanic() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.panics = true
}

func sampleTool(name string, access mcp.Access, handler gomcp.ToolHandler, description string) mcp.Tool {
	return mcp.Tool{
		Definition: &gomcp.Tool{
			Name:        name,
			Description: description,
			InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		},
		Handler: handler,
		Access:  access,
	}
}

// newFixture builds the transport with the F1 tool set plus the sample tools.
func newFixture(t *testing.T) *fixture {
	t.Helper()

	ports := newPorts()
	spy := &spyTool{}

	return &fixture{
		ports: ports,
		handler: ports.handler(t,
			sampleTool(sampleOwnerToolName, mcp.Access{Roles: []auth.Role{auth.RoleOwner}}, spy.handler(), "herramienta sólo del Owner"),
			sampleTool(sampleClientToolName, mcp.Access{Roles: []auth.Role{auth.RoleClient}}, spy.handler(), "herramienta del cliente"),
			sampleTool(sampleAnonymousToolName, mcp.Access{}, spy.handler(), "herramienta sin acceso declarado"),
		),
		spy: spy,
	}
}

// rpcError is the JSON-RPC error object as it appears on the wire.
type rpcError struct {
	Code    int64           `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// rpcResponse is the JSON-RPC response envelope.
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

// dataCode decodes `error.data.code`.
func (r rpcResponse) dataCode(t *testing.T) string {
	t.Helper()
	if r.Error == nil || len(r.Error.Data) == 0 {
		return ""
	}
	var data struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(r.Error.Data, &data); err != nil {
		t.Fatalf("error.data no es JSON: %v (%s)", err, r.Error.Data)
	}
	return data.Code
}

// toolsListResult is the tools/list payload.
type toolsListResult struct {
	Tools []struct {
		Name string `json:"name"`
	} `json:"tools"`
}

func (r toolsListResult) names() []string {
	names := make([]string, 0, len(r.Tools))
	for _, tool := range r.Tools {
		names = append(names, tool.Name)
	}
	return names
}

// callToolResult is the tools/call payload.
type callToolResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StructuredContent json.RawMessage `json:"structuredContent"`
	IsError           bool            `json:"isError"`
}

// post sends one JSON-RPC message over the real HTTP surface. callerID is
// omitted when empty, which is how "the gateway forgot the identity" looks.
func post(t *testing.T, handler http.Handler, callerID, body string) (*httptest.ResponseRecorder, rpcResponse) {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// The Streamable HTTP contract requires both media types in Accept.
	req.Header.Set("Accept", "application/json, text/event-stream")
	if callerID != "" {
		req.Header.Set(mcp.CallerIDHeader, callerID)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /mcp = %d, se espera 200 (cuerpo: %s)", rec.Code, rec.Body.String())
	}

	var response rpcResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("la respuesta no es un mensaje JSON-RPC: %v (cuerpo: %s)", err, rec.Body.String())
	}
	return rec, response
}

// callTool posts a tools/call for name with the given raw arguments.
func callTool(t *testing.T, handler http.Handler, callerID, name, arguments string) rpcResponse {
	t.Helper()

	params := `{"name":"` + name + `"}`
	if arguments != "" {
		params = `{"name":"` + name + `","arguments":` + arguments + `}`
	}
	_, response := post(t, handler, callerID, `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":`+params+`}`)
	return response
}

// listTools posts tools/list and returns the announced tool names.
func listTools(t *testing.T, handler http.Handler, callerID string) []string {
	t.Helper()

	_, response := post(t, handler, callerID, `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	if response.Error != nil {
		t.Fatalf("tools/list falló: %+v", response.Error)
	}

	var result toolsListResult
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatalf("tools/list result ilegible: %v (%s)", err, response.Result)
	}
	return result.names()
}

// contains reports whether names includes want.
func contains(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}

// errFakeDriver is the internal failure the tests inject. Its text carries the
// kind of detail that must never reach the wire, so the sanitization assertions
// look for exactly this string.
var errFakeDriver = errors.New("sqlite: no such table: clients (código 1); /var/lib/crm/db.sqlite")
