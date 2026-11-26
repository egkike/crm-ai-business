package mcp_test

import (
	"encoding/json"
	"testing"

	gomcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/egkike/crm-ai-business/internal/auth"
	"github.com/egkike/crm-ai-business/internal/mcp"
)

// TestToolsListFiltersByRole pins RF2's acceptance criterion — "dado un caller
// staff, cuando pide tools/list, entonces no aparecen tools de administración
// del Owner" — for every principal the §3.8 chain can produce.
func TestToolsListFiltersByRole(t *testing.T) {
	fixture := newFixture(t)

	cases := []struct {
		name     string
		callerID string
		want     []string
		notWant  []string
	}{
		{
			name:     "unknown caller sees only the registration seam",
			callerID: "+5491100009999",
			want:     []string{"register_client"},
			notWant:  []string{sampleOwnerToolName, sampleClientToolName, sampleAnonymousToolName},
		},
		{
			// A broken gateway is not an anonymous sender: with no identifier at
			// all there is nothing to register, so nothing is announced either.
			name:     "missing caller-id sees nothing",
			callerID: "",
			want:     nil,
			notWant:  []string{"register_client", sampleOwnerToolName, sampleClientToolName},
		},
		{
			name:     "owner sees the owner-only tool",
			callerID: ownerCaller,
			want:     []string{"register_client", sampleOwnerToolName},
			notWant:  []string{sampleClientToolName, sampleAnonymousToolName},
		},
		{
			name:     "staff does not see the owner-only tool",
			callerID: staffCaller,
			want:     []string{"register_client"},
			notWant:  []string{sampleOwnerToolName, sampleClientToolName, sampleAnonymousToolName},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			names := listTools(t, fixture.handler, testCase.callerID)
			for _, want := range testCase.want {
				if !contains(names, want) {
					t.Fatalf("tools/list = %v, se espera incluir %q", names, want)
				}
			}
			for _, notWant := range testCase.notWant {
				if contains(names, notWant) {
					t.Fatalf("tools/list = %v, no debe incluir %q", names, notWant)
				}
			}
		})
	}
}

// TestToolsListForClientRole pins that a client-role caller sees its own tools:
// the filter is per role, not "internal sees everything, clients see nothing".
func TestToolsListForClientRole(t *testing.T) {
	ports := newPorts()
	spy := &spyTool{}
	handler := ports.handler(t,
		sampleTool(sampleOwnerToolName, mcp.Access{Roles: []auth.Role{auth.RoleOwner}}, spy.handler(), "sólo Owner"),
		sampleTool(sampleClientToolName, mcp.Access{Roles: []auth.Role{auth.RoleClient}}, spy.handler(), "del cliente"),
	)

	// The e-mail channel admits a client: registration through the real HTTP
	// surface is the path a client-role caller takes.
	response := callTool(t, handler, emailCaller, "register_client", `{"display_name":"Ana"}`)
	if response.Error != nil {
		t.Fatalf("register_client = %+v, se espera éxito", response.Error)
	}

	names := listTools(t, handler, emailCaller)
	if !contains(names, sampleClientToolName) {
		t.Fatalf("tools/list del cliente = %v, se espera %q", names, sampleClientToolName)
	}
	if contains(names, sampleOwnerToolName) {
		t.Fatalf("tools/list del cliente = %v, no debe incluir %q", names, sampleOwnerToolName)
	}
}

// TestToolsListIsNotPubliclyCacheable pins that a role-filtered list is not
// advertised as public: the SDK defaults the cache scope to "public", and a cache
// that reuses one caller's tool list for another would undo the per-role filter.
func TestToolsListIsNotPubliclyCacheable(t *testing.T) {
	fixture := newFixture(t)

	_, response := post(t, fixture.handler, staffCaller, `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	if response.Error != nil {
		t.Fatalf("tools/list falló: %+v", response.Error)
	}

	var listed struct {
		CacheScope string `json:"cacheScope"`
	}
	if err := json.Unmarshal(response.Result, &listed); err != nil {
		t.Fatalf("tools/list ilegible: %v", err)
	}
	if listed.CacheScope != "private" {
		t.Fatalf("cacheScope = %q, se espera private: la lista depende del caller", listed.CacheScope)
	}
}

// TestHiddenToolIsRefusedWhenNamedDirectly pins the control behind the
// presentation: a tool that tools/list does not announce still refuses to run
// when it is invoked by name. Hiding is not the gate; this is.
func TestHiddenToolIsRefusedWhenNamedDirectly(t *testing.T) {
	cases := []struct {
		name       string
		callerID   string
		wantCode   int64
		wantData   string
		wantPhrase string
	}{
		{
			name:       "staff",
			callerID:   staffCaller,
			wantCode:   -32000,
			wantData:   "role_not_allowed",
			wantPhrase: "tu rol no permite usar esta herramienta.",
		},
		{
			name:       "unknown caller never learns the tool exists",
			callerID:   "+5491100009999",
			wantCode:   -32000,
			wantData:   string(auth.CodeUnknownCaller),
			wantPhrase: "no te reconozco. Por favor regístrate primero.",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			ports := newPorts()
			spy := &spyTool{}
			handler := ports.handler(t,
				sampleTool(sampleOwnerToolName, mcp.Access{Roles: []auth.Role{auth.RoleOwner}}, spy.handler(), "sólo Owner"),
			)

			response := callTool(t, handler, testCase.callerID, sampleOwnerToolName, `{}`)

			if response.Error == nil {
				t.Fatalf("se espera error JSON-RPC, result: %s", response.Result)
			}
			if response.Error.Code != testCase.wantCode {
				t.Fatalf("code = %d, se espera %d", response.Error.Code, testCase.wantCode)
			}
			if response.Error.Message != testCase.wantPhrase {
				t.Fatalf("message = %q, se espera %q", response.Error.Message, testCase.wantPhrase)
			}
			if got := response.dataCode(t); got != testCase.wantData {
				t.Fatalf("data.code = %q, se espera %q", got, testCase.wantData)
			}
			if spy.invocations() != 0 {
				t.Fatal("la herramienta se ejecutó pese a la negativa")
			}
		})
	}
}

// TestOwnerToolRunsForOwner is the positive control of the gate: the same tool
// that is refused above runs for the role it declares, exactly once.
func TestOwnerToolRunsForOwner(t *testing.T) {
	ports := newPorts()
	spy := &spyTool{}
	handler := ports.handler(t,
		sampleTool(sampleOwnerToolName, mcp.Access{Roles: []auth.Role{auth.RoleOwner}}, spy.handler(), "sólo Owner"),
	)

	response := callTool(t, handler, ownerCaller, sampleOwnerToolName, `{}`)

	if response.Error != nil {
		t.Fatalf("se espera éxito, error: %+v", response.Error)
	}
	if spy.invocations() != 1 {
		t.Fatalf("invocaciones = %d, se espera 1", spy.invocations())
	}
}

// TestUndeclaredAccessDeniesEveryone pins the fail-closed default: a tool whose
// author declared no access at all is invisible and refused for every principal,
// including the owner. Silently defaulting to "public" would be an authorization
// hole created by omission.
func TestUndeclaredAccessDeniesEveryone(t *testing.T) {
	fixture := newFixture(t)

	for _, callerID := range []string{ownerCaller, staffCaller, "+5491100009999"} {
		if names := listTools(t, fixture.handler, callerID); contains(names, sampleAnonymousToolName) {
			t.Fatalf("tools/list de %q = %v, no debe anunciar la herramienta sin acceso", callerID, names)
		}
	}

	response := callTool(t, fixture.handler, ownerCaller, sampleAnonymousToolName, `{}`)
	if response.Error == nil || response.Error.Code != -32000 {
		t.Fatalf("se espera negativa -32000, got %+v", response.Error)
	}
	if fixture.spy.invocations() != 0 {
		t.Fatal("la herramienta sin acceso declarado se ejecutó")
	}
}

// TestToolPanicBecomesInternalError pins that a bug inside a tool cannot become
// a stack trace on the wire: the panic is recovered and collapses to the generic
// internal error, with no internals in the payload.
func TestToolPanicBecomesInternalError(t *testing.T) {
	ports := newPorts()
	spy := &spyTool{}
	spy.makePanic()
	handler := ports.handler(t,
		sampleTool(sampleOwnerToolName, mcp.Access{Roles: []auth.Role{auth.RoleOwner}}, spy.handler(), "sólo Owner"),
	)

	response := callTool(t, handler, ownerCaller, sampleOwnerToolName, `{}`)

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
}

// TestRegistryRejectsInvalidWiring pins the fail-closed construction: a tool set
// that cannot be served refuses to build instead of degrading into a partial
// registry.
func TestRegistryRejectsInvalidWiring(t *testing.T) {
	valid := sampleTool("valida", mcp.Access{Roles: []auth.Role{auth.RoleOwner}}, (&spyTool{}).handler(), "ok")

	cases := []struct {
		name  string
		deps  mcp.Dependencies
		tool  mcp.Tool
		extra mcp.Tool
	}{
		{
			name: "sin resolver",
			deps: mcp.Dependencies{RegisterClient: &fakeRegisterClient{resolver: newFakeResolver()}},
		},
		{
			name: "sin registro",
			deps: mcp.Dependencies{CallerResolver: newFakeResolver()},
		},
		{
			name: "herramienta sin definición",
			deps: mcp.Dependencies{CallerResolver: newFakeResolver(), RegisterClient: &fakeRegisterClient{resolver: newFakeResolver()}},
			tool: mcp.Tool{Handler: (&spyTool{}).handler()},
		},
		{
			name: "herramienta sin nombre",
			deps: mcp.Dependencies{CallerResolver: newFakeResolver(), RegisterClient: &fakeRegisterClient{resolver: newFakeResolver()}},
			tool: mcp.Tool{Definition: &gomcp.Tool{}, Handler: (&spyTool{}).handler()},
		},
		{
			name: "herramienta sin handler",
			deps: mcp.Dependencies{CallerResolver: newFakeResolver(), RegisterClient: &fakeRegisterClient{resolver: newFakeResolver()}},
			tool: mcp.Tool{Definition: &gomcp.Tool{Name: "sin_handler"}},
		},
		{
			name: "nombre duplicado con la herramienta base",
			deps: mcp.Dependencies{CallerResolver: newFakeResolver(), RegisterClient: &fakeRegisterClient{resolver: newFakeResolver()}},
			tool: sampleTool("register_client", mcp.Access{Roles: []auth.Role{auth.RoleOwner}}, (&spyTool{}).handler(), "choque"),
		},
		{
			name: "herramienta sin inputSchema",
			deps: mcp.Dependencies{CallerResolver: newFakeResolver(), RegisterClient: &fakeRegisterClient{resolver: newFakeResolver()}},
			tool: mcp.Tool{Definition: &gomcp.Tool{Name: "sin_schema"}, Handler: (&spyTool{}).handler()},
		},
		{
			name: "herramienta extra con acceso anónimo",
			deps: mcp.Dependencies{CallerResolver: newFakeResolver(), RegisterClient: &fakeRegisterClient{resolver: newFakeResolver()}},
			tool: sampleTool("anonima_extra", mcp.Access{Anonymous: true}, (&spyTool{}).handler(), "no permitida"),
		},
		{
			name:  "nombre duplicado entre herramientas",
			deps:  mcp.Dependencies{CallerResolver: newFakeResolver(), RegisterClient: &fakeRegisterClient{resolver: newFakeResolver()}},
			tool:  valid,
			extra: valid,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			handler, err := mcp.NewHandlerWithTools(testCase.deps, append([]mcp.Tool{testCase.extra}, testCase.tool)...)
			if err == nil {
				t.Fatalf("NewHandlerWithTools() = %v, se espera fail-closed", handler)
			}
		})
	}
}

// TestRegistrationWithoutPortFails pins that the package's own wiring refuses a
// missing port: NewHandler is not a constructor that can produce a handler
// which fails per request.
func TestRegistrationWithoutPortFails(t *testing.T) {
	if handler, err := mcp.NewHandler(mcp.Dependencies{}); err == nil {
		t.Fatalf("NewHandler() = %v, se espera fail-closed", handler)
	}
}
