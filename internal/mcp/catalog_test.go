package mcp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/egkike/crm-ai-business/internal/application/dto"
	"github.com/egkike/crm-ai-business/internal/application/usecase"
	"github.com/egkike/crm-ai-business/internal/auth"
	"github.com/egkike/crm-ai-business/internal/mcp"
)

// The catalog tool names, spelled here so the tests pin the wire names of RF4
// independently of the constants the package uses.
const (
	searchCatalogTool      = "search_catalog"
	createCatalogItemTool  = "create_catalog_item"
	updateCatalogItemTool  = "update_catalog_item"
	registerClientToolName = "register_client"
)

// fakeCatalogStore is the CatalogStore double behind the REAL catalog use cases:
// the transport tests go through the real RBAC re-assertion and the real Spanish
// validation, and only the persistence is doubled (the SQL is pinned by the
// repository tests).
type fakeCatalogStore struct {
	mu      sync.Mutex
	records map[string]dto.CatalogItemRecord
	search  []dto.SearchCatalogQuery
	created []dto.NewCatalogItem
	updated []dto.CatalogItemUpdate
}

func newFakeCatalogStore() *fakeCatalogStore {
	return &fakeCatalogStore{records: map[string]dto.CatalogItemRecord{
		"aaaaaaaa-aaaa-4aaa-8aaa-000000000001": {
			ID:          "aaaaaaaa-aaaa-4aaa-8aaa-000000000001",
			Kind:        dto.CatalogKindServicio,
			Name:        "Consulta kinesiológica",
			Description: "Evaluación en consultorio",
			UseCases:    "Dolor lumbar",
			Limitations: "No cubre cirugías",
			PriceCents:  func() *int64 { value := int64(500000); return &value }(),
			DurationMin: func() *int { value := 40; return &value }(),
		},
	}}
}

func (f *fakeCatalogStore) CreateCatalogItem(_ context.Context, item dto.NewCatalogItem) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, item)
	f.records[item.ID] = dto.CatalogItemRecord{
		ID:          item.ID,
		Kind:        item.Item.Kind,
		Name:        item.Item.Name,
		Description: item.Item.Description,
		UseCases:    item.Item.UseCases,
		Limitations: item.Item.Limitations,
		PriceCents:  item.Item.PriceCents,
		SalesURL:    item.Item.SalesURL,
		DurationMin: item.Item.DurationMin,
	}
	return nil
}

func (f *fakeCatalogStore) UpdateCatalogItem(_ context.Context, item dto.CatalogItemUpdate) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updated = append(f.updated, item)
	if _, ok := f.records[item.ID]; !ok {
		return false, nil
	}
	f.records[item.ID] = dto.CatalogItemRecord{
		ID:          item.ID,
		Kind:        item.Item.Kind,
		Name:        item.Item.Name,
		Description: item.Item.Description,
		UseCases:    item.Item.UseCases,
		Limitations: item.Item.Limitations,
		PriceCents:  item.Item.PriceCents,
		SalesURL:    item.Item.SalesURL,
		DurationMin: item.Item.DurationMin,
	}
	return true, nil
}

func (f *fakeCatalogStore) GetCatalogItem(_ context.Context, id string) (dto.CatalogItemRecord, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	record, ok := f.records[id]
	return record, ok, nil
}

// SearchCatalog is a substring search over every indexed field, which is enough
// to prove the wire carries what the agent needs (the FTS ranking and the
// tokenizer are pinned by the repository and db tests).
func (f *fakeCatalogStore) SearchCatalog(_ context.Context, query dto.SearchCatalogQuery) ([]dto.CatalogItemRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.search = append(f.search, query)

	items := make([]dto.CatalogItemRecord, 0, len(f.records))
	for _, record := range f.records {
		if query.Text != "" && !searchableFieldsContain(record, query.Text) {
			continue
		}
		items = append(items, record)
	}
	return items, nil
}

func searchableFieldsContain(record dto.CatalogItemRecord, text string) bool {
	needle := strings.ToLower(text)
	for _, field := range []string{record.Name, record.Description, record.UseCases, record.Limitations} {
		if strings.Contains(strings.ToLower(field), needle) {
			return true
		}
	}
	return false
}

// catalogFixture is the transport over the real RF4 use cases plus the F1
// registration seam, so both the wire and the layer underneath are observable.
type catalogFixture struct {
	*ports
	handler http.Handler
	store   *fakeCatalogStore
}

// newCatalogFixture wires the catalog tools into the handler's default set (the
// same way the composition root does) and adds a client-role identity, because
// the RF4 search has to be reachable by the end client.
func newCatalogFixture(t *testing.T) *catalogFixture {
	t.Helper()

	ports := newPorts()
	ports.resolver.add(emailCaller, auth.Caller{
		ID:   "44444444-4444-4444-8444-444444444444",
		Name: "Cliente",
		Role: auth.RoleClient,
	})

	store := newFakeCatalogStore()
	search, err := usecase.NewSearchCatalog(store)
	if err != nil {
		t.Fatalf("NewSearchCatalog() error inesperado: %v", err)
	}
	create, err := usecase.NewCreateCatalogItem(store)
	if err != nil {
		t.Fatalf("NewCreateCatalogItem() error inesperado: %v", err)
	}
	update, err := usecase.NewUpdateCatalogItem(store)
	if err != nil {
		t.Fatalf("NewUpdateCatalogItem() error inesperado: %v", err)
	}

	handler, err := mcp.NewHandler(mcp.Dependencies{
		CallerResolver: ports.resolver,
		RegisterClient: ports.register,
		Catalog: &mcp.CatalogDependencies{
			Search: search,
			Create: create,
			Update: update,
		},
	})
	if err != nil {
		t.Fatalf("NewHandler() = %v, se espera un handler", err)
	}

	return &catalogFixture{ports: ports, handler: handler, store: store}
}

// TestCatalogToolsListFiltersByRole pins the RF2 half of RF4: the search is
// announced to every resolved role, and the high/edición tools are announced only
// to Owner/Admin. An unknown sender sees nothing but the registration seam.
func TestCatalogToolsListFiltersByRole(t *testing.T) {
	fixture := newCatalogFixture(t)

	cases := []struct {
		name     string
		callerID string
		want     []string
		notWant  []string
	}{
		{
			name:     "cliente busca pero no administra",
			callerID: emailCaller,
			want:     []string{registerClientToolName, searchCatalogTool},
			notWant:  []string{createCatalogItemTool, updateCatalogItemTool},
		},
		{
			name:     "staff busca pero no administra",
			callerID: staffCaller,
			want:     []string{registerClientToolName, searchCatalogTool},
			notWant:  []string{createCatalogItemTool, updateCatalogItemTool},
		},
		{
			name:     "owner ve las tres",
			callerID: ownerCaller,
			want:     []string{registerClientToolName, searchCatalogTool, createCatalogItemTool, updateCatalogItemTool},
		},
		{
			name:     "desconocido solo se registra",
			callerID: "+5491100009999",
			want:     []string{registerClientToolName},
			notWant:  []string{searchCatalogTool, createCatalogItemTool, updateCatalogItemTool},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			names := listTools(t, fixture.handler, tc.callerID)
			for _, want := range tc.want {
				if !contains(names, want) {
					t.Fatalf("tools/list = %v, falta %q", names, want)
				}
			}
			for _, notWant := range tc.notWant {
				if contains(names, notWant) {
					t.Fatalf("tools/list = %v, no debe anunciar %q", names, notWant)
				}
			}
		})
	}
}

// TestCatalogEditingIsRefusedForStaffAndClients pins the other half of RF2: the
// tools are not only hidden, they are refused before any handler runs, with the
// Spanish role denial of the registry (-32000 / role_not_allowed).
func TestCatalogEditingIsRefusedForStaffAndClients(t *testing.T) {
	fixture := newCatalogFixture(t)
	arguments := `{"kind":"servicio","name":"Corte de pelo"}`

	for _, tc := range []struct {
		name     string
		callerID string
	}{
		{name: "staff", callerID: staffCaller},
		{name: "cliente", callerID: emailCaller},
		{name: "desconocido", callerID: "+5491100009999"},
	} {
		for _, tool := range []string{createCatalogItemTool, updateCatalogItemTool} {
			t.Run(tc.name+" "+tool, func(t *testing.T) {
				response := callTool(t, fixture.handler, tc.callerID, tool, arguments)

				if response.Error == nil {
					t.Fatalf("se esperaba error JSON-RPC, result: %s", response.Result)
				}
				if response.Error.Code != -32000 {
					t.Fatalf("code = %d, se espera -32000", response.Error.Code)
				}
				if got := response.dataCode(t); got != "role_not_allowed" && got != string(auth.CodeUnknownCaller) {
					t.Fatalf("data.code = %q, se esperaba role_not_allowed o unknown_caller", got)
				}
				if len(fixture.store.created) != 0 || len(fixture.store.updated) != 0 {
					t.Fatal("la herramienta se ejecutó: el repositorio registró escrituras")
				}
			})
		}
	}
}

// TestSearchCatalogOver HTTP pins the RF4 search end to end: a client-role caller
// searches with its own words and receives the v1.11 fields (use_cases and
// limitations) plus money as integer cents.
func TestSearchCatalogOverHTTP(t *testing.T) {
	fixture := newCatalogFixture(t)

	response := callTool(t, fixture.handler, emailCaller, searchCatalogTool, `{"query":"lumbar"}`)
	if response.Error != nil {
		t.Fatalf("search_catalog falló: %+v", response.Error)
	}

	var result callToolResult
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatalf("tools/call result ilegible: %v (%s)", err, response.Result)
	}
	if result.IsError {
		t.Fatalf("search_catalog devolvió isError: %+v", result.Content)
	}

	var output struct {
		Items []struct {
			ID          string `json:"id"`
			Kind        string `json:"kind"`
			Name        string `json:"name"`
			UseCases    string `json:"use_cases"`
			Limitations string `json:"limitations"`
			PriceCents  *int64 `json:"price_cents"`
			DurationMin *int   `json:"duration_min"`
		} `json:"items"`
		Limit  int `json:"limit"`
		Offset int `json:"offset"`
	}
	if err := json.Unmarshal(result.StructuredContent, &output); err != nil {
		t.Fatalf("structuredContent ilegible: %v (%s)", err, result.StructuredContent)
	}

	if len(output.Items) != 1 {
		t.Fatalf("items = %+v, se esperaba el ítem encontrado", output.Items)
	}
	item := output.Items[0]
	if item.UseCases != "Dolor lumbar" || item.Limitations != "No cubre cirugías" {
		t.Fatalf("el resultado no lleva el conocimiento v1.11: %+v", item)
	}
	if item.PriceCents == nil || *item.PriceCents != 500000 {
		t.Fatalf("price_cents = %v, se esperaban 500000 centavos enteros", item.PriceCents)
	}
	if item.DurationMin == nil || *item.DurationMin != 40 {
		t.Fatalf("duration_min = %v, se esperaban 40", item.DurationMin)
	}
	if output.Limit != usecase.DefaultCatalogSearchLimit || output.Offset != 0 {
		t.Fatalf("paginación = (%d, %d), se esperaba el default", output.Limit, output.Offset)
	}

	// The page policy also travels back: an oversized limit is capped by the use
	// case, and the answer says which page was actually served.
	response = callTool(t, fixture.handler, emailCaller, searchCatalogTool, `{"query":"lumbar","limit":500}`)
	if response.Error != nil {
		t.Fatalf("search_catalog falló: %+v", response.Error)
	}
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatalf("tools/call result ilegible: %v", err)
	}
	if err := json.Unmarshal(result.StructuredContent, &output); err != nil {
		t.Fatalf("structuredContent ilegible: %v", err)
	}
	if output.Limit != usecase.MaxCatalogSearchLimit {
		t.Fatalf("limit = %d, se esperaba el tope %d", output.Limit, usecase.MaxCatalogSearchLimit)
	}
}

// TestCreateCatalogItemOverHTTP pins the Owner path: the tool creates the item
// and answers with the stored row, including a null price ("a consultar").
func TestCreateCatalogItemOverHTTP(t *testing.T) {
	fixture := newCatalogFixture(t)

	response := callTool(t, fixture.handler, ownerCaller, createCatalogItemTool,
		`{"kind":"servicio","name":"Corte de pelo","description":"Corte y peinado",`+
			`"use_cases":"Flequillo incómodo","limitations":"No incluye coloración","duration_min":45}`)
	if response.Error != nil {
		t.Fatalf("create_catalog_item falló: %+v", response.Error)
	}

	var result callToolResult
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatalf("tools/call result ilegible: %v", err)
	}
	var output struct {
		ID          string `json:"id"`
		Kind        string `json:"kind"`
		Name        string `json:"name"`
		UseCases    string `json:"use_cases"`
		Limitations string `json:"limitations"`
		PriceCents  *int64 `json:"price_cents"`
	}
	if err := json.Unmarshal(result.StructuredContent, &output); err != nil {
		t.Fatalf("structuredContent ilegible: %v", err)
	}

	if len(output.ID) != 36 {
		t.Fatalf("id = %q, se esperaba un UUID interno", output.ID)
	}
	if output.Kind != string(dto.CatalogKindServicio) || output.Name != "Corte de pelo" {
		t.Fatalf("ítem devuelto = %+v, se esperaba el creado", output)
	}
	if output.PriceCents != nil {
		t.Fatalf("price_cents = %v, se esperaba null (a consultar)", *output.PriceCents)
	}
	if len(fixture.store.created) != 1 {
		t.Fatalf("escrituras = %d, se esperaba 1", len(fixture.store.created))
	}
}

// TestCatalogSemanticRejectionsOverHTTP pins that the business refusals of RF4
// travel as -32002 with the Spanish text of the use case: an unknown kind, a
// product with a duration (the RF4 acceptance criterion) and an update of an id
// that does not exist.
func TestCatalogSemanticRejectionsOverHTTP(t *testing.T) {
	fixture := newCatalogFixture(t)

	cases := []struct {
		name      string
		tool      string
		arguments string
		wantCode  string
		wantText  string
	}{
		{
			name:      "kind fuera del vocabulario",
			tool:      createCatalogItemTool,
			arguments: `{"kind":"alquiler","name":"Algo"}`,
			wantCode:  string(usecase.CodeInvalidCatalogKind),
			wantText:  `el tipo de ítem debe ser "producto" o "servicio".`,
		},
		{
			// RF4 acceptance criterion: a product is not bookable, so it cannot
			// carry a duration.
			name:      "producto con duración",
			tool:      createCatalogItemTool,
			arguments: `{"kind":"producto","name":"Crema","duration_min":30}`,
			wantCode:  string(usecase.CodeInvalidCatalogItem),
			wantText:  "un producto no puede tener duración: solo los servicios son reservables.",
		},
		{
			name:      "edición de un ítem inexistente",
			tool:      updateCatalogItemTool,
			arguments: `{"id":"bbbbbbbb-bbbb-4bbb-8bbb-000000000009","kind":"producto","name":"Fantasma"}`,
			wantCode:  string(usecase.CodeCatalogItemNotFound),
			wantText:  "no existe un ítem del catálogo con ese identificador.",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response := callTool(t, fixture.handler, ownerCaller, tc.tool, tc.arguments)

			if response.Error == nil {
				t.Fatalf("se esperaba error JSON-RPC, result: %s", response.Result)
			}
			if response.Error.Code != -32002 {
				t.Fatalf("code = %d, se espera -32002", response.Error.Code)
			}
			if got := response.dataCode(t); got != tc.wantCode {
				t.Fatalf("data.code = %q, se esperaba %q", got, tc.wantCode)
			}
			if response.Error.Message != tc.wantText {
				t.Fatalf("message = %q, se esperaba %q", response.Error.Message, tc.wantText)
			}
		})
	}
}

// TestCatalogArgumentValidation pins the protocol-level rejections: arguments
// that do not fit the schema are -32602 with a Spanish message, and nothing
// reaches the use cases.
func TestCatalogArgumentValidation(t *testing.T) {
	fixture := newCatalogFixture(t)

	cases := []struct {
		name      string
		tool      string
		arguments string
		wantText  string
	}{
		{
			name:      "parámetro desconocido en la búsqueda",
			tool:      searchCatalogTool,
			arguments: `{"query":"corte","role":"owner"}`,
			wantText:  "search_catalog no acepta parámetros adicionales.",
		},
		{
			name:      "límite no entero",
			tool:      searchCatalogTool,
			arguments: `{"query":"corte","limit":2.5}`,
			wantText:  "limit debe ser un número entero.",
		},
		{
			name:      "offset negativo",
			tool:      searchCatalogTool,
			arguments: `{"query":"corte","offset":-1}`,
			wantText:  "offset no puede ser negativo.",
		},
		{
			name:      "precio con decimales",
			tool:      createCatalogItemTool,
			arguments: `{"kind":"producto","name":"Crema","price_cents":10.5}`,
			wantText:  "price_cents debe ser un número entero de centavos (sin decimales).",
		},
		{
			name:      "id en el alta",
			tool:      createCatalogItemTool,
			arguments: `{"id":"cccccccc-cccc-4ccc-8ccc-000000000001","kind":"producto","name":"Crema"}`,
			wantText:  "create_catalog_item genera un id nuevo: no acepta el parámetro id.",
		},
		{
			name:      "edición sin id",
			tool:      updateCatalogItemTool,
			arguments: `{"kind":"producto","name":"Crema"}`,
			wantText:  "update_catalog_item requiere el id del ítem a editar.",
		},
		{
			name:      "parámetro desconocido en el alta",
			tool:      createCatalogItemTool,
			arguments: `{"kind":"producto","name":"Crema","is_active":0}`,
			wantText:  "create_catalog_item no acepta parámetros adicionales.",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response := callTool(t, fixture.handler, ownerCaller, tc.tool, tc.arguments)

			if response.Error == nil {
				t.Fatalf("se esperaba error JSON-RPC, result: %s", response.Result)
			}
			if response.Error.Code != -32602 {
				t.Fatalf("code = %d, se espera -32602", response.Error.Code)
			}
			if got := response.dataCode(t); got != "invalid_arguments" {
				t.Fatalf("data.code = %q, se esperaba invalid_arguments", got)
			}
			if response.Error.Message != tc.wantText {
				t.Fatalf("message = %q, se esperaba %q", response.Error.Message, tc.wantText)
			}
		})
	}

	if len(fixture.store.created) != 0 || len(fixture.store.updated) != 0 || len(fixture.store.search) != 0 {
		t.Fatal("un argumento inválido llegó a los casos de uso")
	}
}

// TestCatalogDependenciesMustBeComplete pins the construction rule: a partially
// wired catalog refuses to build instead of announcing a tool set the
// composition root never intended.
func TestCatalogDependenciesMustBeComplete(t *testing.T) {
	ports := newPorts()
	store := newFakeCatalogStore()
	search, err := usecase.NewSearchCatalog(store)
	if err != nil {
		t.Fatalf("NewSearchCatalog() error inesperado: %v", err)
	}

	for name, catalog := range map[string]*mcp.CatalogDependencies{
		"solo búsqueda": {Search: search},
		"sin edición":   {Search: search, Create: mustCreateUseCase(t, store)},
		"vacío":         {},
	} {
		t.Run(name, func(t *testing.T) {
			handler, err := mcp.NewHandler(mcp.Dependencies{
				CallerResolver: ports.resolver,
				RegisterClient: ports.register,
				Catalog:        catalog,
			})
			if err == nil {
				t.Fatalf("NewHandler() = %v, se esperaba el rechazo de un catálogo incompleto", handler)
			}
		})
	}
}

// mustCreateUseCase builds the creation use case for the partial-wiring test.
func mustCreateUseCase(t *testing.T, store usecase.CatalogStore) *usecase.CreateCatalogItem {
	t.Helper()
	create, err := usecase.NewCreateCatalogItem(store)
	if err != nil {
		t.Fatalf("NewCreateCatalogItem() error inesperado: %v", err)
	}
	return create
}
