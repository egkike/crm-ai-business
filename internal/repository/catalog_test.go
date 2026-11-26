package repository_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/egkike/crm-ai-business/internal/application/dto"
	"github.com/egkike/crm-ai-business/internal/repository"
)

// newCatalogOf wires the adapter over handle.
func newCatalogOf(t *testing.T, handle *sql.DB) *repository.CatalogRepo {
	t.Helper()
	repo, err := repository.NewCatalogRepo(handle)
	if err != nil {
		t.Fatalf("NewCatalogRepo() error inesperado: %v", err)
	}
	return repo
}

// cents and minutes build the optional pointers of the fixtures.
func cents(value int64) *int64 { return &value }

func minutes(value int) *int { return &value }

// TestNewCatalogRepoRejectsNilHandle pins the fail-closed construction rule.
func TestNewCatalogRepoRejectsNilHandle(t *testing.T) {
	repo, err := repository.NewCatalogRepo(nil)
	if err == nil {
		t.Fatal("NewCatalogRepo(nil) debía fallar (fail-closed)")
	}
	if repo != nil {
		t.Fatalf("NewCatalogRepo(nil) devolvió un adaptador utilizable: %+v", repo)
	}
}

// TestCatalogCreateAndGet pins the round trip of the v1.11 fields: what the use
// case writes is what every reader gets back, including the nullable columns
// (empty text for absent use_cases/limitations, nil for an unpublished price and
// an unstated duration) and money as integer cents.
func TestCatalogCreateAndGet(t *testing.T) {
	handle := openDB(t)
	repo := newCatalogOf(t, handle)
	ctx := context.Background()

	created := dto.NewCatalogItem{
		ID: uuid(400),
		Item: dto.CatalogItemInput{
			Kind:        dto.CatalogKindServicio,
			Name:        "Corte de pelo",
			Description: "Corte y peinado en salón",
			UseCases:    "Flequillo incómodo, cambio de look",
			Limitations: "No incluye coloración",
			PriceCents:  cents(150000),
			SalesURL:    "https://tienda.example/corte",
			DurationMin: minutes(45),
		},
	}
	if err := repo.CreateCatalogItem(ctx, created); err != nil {
		t.Fatalf("CreateCatalogItem() error inesperado: %v", err)
	}

	record, found, err := repo.GetCatalogItem(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetCatalogItem() error inesperado: %v", err)
	}
	if !found {
		t.Fatalf("GetCatalogItem(%s) no encontró el ítem recién creado", created.ID)
	}
	if record.Kind != dto.CatalogKindServicio {
		t.Fatalf("kind = %q, se esperaba %q", record.Kind, dto.CatalogKindServicio)
	}
	if record.Name != "Corte de pelo" || record.Description != "Corte y peinado en salón" {
		t.Fatalf("nombre/descripción = %q/%q, se esperaban los valores escritos", record.Name, record.Description)
	}
	if record.UseCases != "Flequillo incómodo, cambio de look" {
		t.Fatalf("use_cases = %q, se esperaba el texto escrito", record.UseCases)
	}
	if record.Limitations != "No incluye coloración" {
		t.Fatalf("limitations = %q, se esperaba el texto escrito", record.Limitations)
	}
	if record.PriceCents == nil || *record.PriceCents != 150000 {
		t.Fatalf("price_cents = %v, se esperaban 150000 centavos enteros", record.PriceCents)
	}
	if record.DurationMin == nil || *record.DurationMin != 45 {
		t.Fatalf("duration_min = %v, se esperaban 45", record.DurationMin)
	}
	if record.SalesURL != "https://tienda.example/corte" {
		t.Fatalf("sales_url = %q, se esperaba el enlace escrito", record.SalesURL)
	}

	// The base table stores money as INTEGER minor units, never as a float.
	if got := queryScalar(t, handle, `SELECT typeof(price_cents) FROM catalog_items WHERE id = ?`, created.ID); got != "integer" {
		t.Fatalf("typeof(price_cents) = %q, se esperaba integer", got)
	}

	// An absent price is NULL, not zero: "a consultar" and "gratis" are
	// different answers.
	if err := repo.CreateCatalogItem(ctx, dto.NewCatalogItem{
		ID:   uuid(401),
		Item: dto.CatalogItemInput{Kind: dto.CatalogKindProducto, Name: "Repuesto"},
	}); err != nil {
		t.Fatalf("CreateCatalogItem() sin precio error inesperado: %v", err)
	}
	free, found, err := repo.GetCatalogItem(ctx, uuid(401))
	if err != nil || !found {
		t.Fatalf("GetCatalogItem() = %+v, %t, %v", free, found, err)
	}
	if free.PriceCents != nil {
		t.Fatalf("price_cents = %v, se esperaba NULL (a consultar)", *free.PriceCents)
	}
	if free.DurationMin != nil {
		t.Fatalf("duration_min = %v, se esperaba NULL", *free.DurationMin)
	}
	if free.UseCases != "" || free.Limitations != "" {
		t.Fatalf("use_cases/limitations = %q/%q, se esperaban vacíos", free.UseCases, free.Limitations)
	}
}

// TestCatalogCreateRejectsUnknownItem pins the not-found path: an id no row
// carries is reported as found=false, never as an empty record.
func TestCatalogGetUnknownItem(t *testing.T) {
	repo := newCatalogOf(t, openDB(t))

	record, found, err := repo.GetCatalogItem(context.Background(), uuid(999))
	if err != nil {
		t.Fatalf("GetCatalogItem() error inesperado: %v", err)
	}
	if found || record.ID != "" {
		t.Fatalf("GetCatalogItem() = %+v, %t; se esperaba no encontrado", record, found)
	}
	if _, _, err := repo.GetCatalogItem(context.Background(), ""); err == nil {
		t.Fatal("GetCatalogItem(\"\") debía rechazarse: el id es obligatorio")
	}
}

// TestCatalogUpdateReplacesEveryEditableField pins the update semantics: the
// payload replaces the stored item (including clearing the optional columns),
// updated_at moves, and an id no row carries reports found=false without
// touching anything.
func TestCatalogUpdateReplacesEveryEditableField(t *testing.T) {
	handle := openDB(t)
	repo := newCatalogOf(t, handle)
	ctx := context.Background()

	if err := repo.CreateCatalogItem(ctx, dto.NewCatalogItem{
		ID: uuid(410),
		Item: dto.CatalogItemInput{
			Kind:        dto.CatalogKindServicio,
			Name:        "Masaje",
			Description: "Sesión de 45 minutos",
			UseCases:    "Contracturas",
			Limitations: "No cubre lesiones",
			PriceCents:  cents(500000),
			DurationMin: minutes(45),
		},
	}); err != nil {
		t.Fatalf("CreateCatalogItem() error inesperado: %v", err)
	}
	// Age the row first: the timestamp has millisecond precision, and an update
	// inside the same millisecond would be indistinguishable from no update at
	// all — the assertion has to talk about a timestamp that actually moved.
	if _, err := handle.Exec(`UPDATE catalog_items SET updated_at = '2020-01-01T00:00:00.000Z' WHERE id = ?`, uuid(410)); err != nil {
		t.Fatalf("envejecer updated_at: %v", err)
	}
	before := queryScalar(t, handle, `SELECT updated_at FROM catalog_items WHERE id = ?`, uuid(410))

	found, err := repo.UpdateCatalogItem(ctx, dto.CatalogItemUpdate{
		ID: uuid(410),
		Item: dto.CatalogItemInput{
			Kind:        dto.CatalogKindProducto,
			Name:        "Aceite esencial",
			Description: "Frasco de 30 ml",
		},
	})
	if err != nil {
		t.Fatalf("UpdateCatalogItem() error inesperado: %v", err)
	}
	if !found {
		t.Fatal("UpdateCatalogItem() = found=false para un ítem existente")
	}

	record, found, err := repo.GetCatalogItem(ctx, uuid(410))
	if err != nil || !found {
		t.Fatalf("GetCatalogItem() = %+v, %t, %v", record, found, err)
	}
	if record.Kind != dto.CatalogKindProducto || record.Name != "Aceite esencial" {
		t.Fatalf("el ítem no se reemplazó: %+v", record)
	}
	if record.UseCases != "" || record.Limitations != "" || record.PriceCents != nil || record.DurationMin != nil {
		t.Fatalf("los campos opcionales no se limpiaron: %+v", record)
	}
	if after := queryScalar(t, handle, `SELECT updated_at FROM catalog_items WHERE id = ?`, uuid(410)); after == before {
		t.Fatalf("updated_at no cambió (%s): la edición no se registró en el timestamp", after)
	}

	found, err = repo.UpdateCatalogItem(ctx, dto.CatalogItemUpdate{
		ID:   uuid(999),
		Item: dto.CatalogItemInput{Kind: dto.CatalogKindProducto, Name: "Fantasma"},
	})
	if err != nil {
		t.Fatalf("UpdateCatalogItem() error inesperado: %v", err)
	}
	if found {
		t.Fatal("UpdateCatalogItem() = found=true para un id inexistente")
	}
	if got := queryScalar(t, handle, `SELECT COUNT(*) FROM catalog_items`); got != "1" {
		t.Fatalf("filas = %s, se esperaba 1 (la edición inexistente no crea nada)", got)
	}
}

// TestCatalogSearchFindsTheAgentKnowledge pins the v1.11 win end to end: a query
// that matches ONLY the use-case text finds the item, and the same is true for
// the limitations and the description.
func TestCatalogSearchFindsTheAgentKnowledge(t *testing.T) {
	repo := newCatalogOf(t, openDB(t))
	ctx := context.Background()

	seedCatalog(t, repo, dto.NewCatalogItem{
		ID: uuid(420),
		Item: dto.CatalogItemInput{
			Kind:        dto.CatalogKindServicio,
			Name:        "Consulta kinesiológica",
			Description: "Evaluación en consultorio",
			UseCases:    "Dolor lumbar y cervical",
			Limitations: "No cubre cirugías",
			PriceCents:  cents(500000),
			DurationMin: minutes(40),
		},
	})
	seedCatalog(t, repo, dto.NewCatalogItem{
		ID: uuid(421),
		Item: dto.CatalogItemInput{
			Kind:        dto.CatalogKindProducto,
			Name:        "Crema regeneradora",
			Description: "Pote de 100 g",
			UseCases:    "Piel seca",
			Limitations: "No cubre psoriasis",
			PriceCents:  cents(150000),
		},
	})

	cases := []struct {
		name  string
		query string
		want  string
	}{
		{name: "por nombre", query: "kinesiologica", want: uuid(420)},
		{name: "por nombre, prefijo y acentos", query: "KINESIOLÓGICA", want: uuid(420)},
		{name: "por descripción", query: "consultorio", want: uuid(420)},
		{name: "por caso de uso (el problema del cliente)", query: "dolor lumbar", want: uuid(420)},
		{name: "por limitación", query: "psoriasis", want: uuid(421)},
		{name: "acentos y mayúsculas", query: "¿Tenés KINESIOLÓGICA?", want: uuid(420)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			items, err := repo.SearchCatalog(ctx, dto.SearchCatalogQuery{Text: tc.query, Limit: 10})
			if err != nil {
				t.Fatalf("SearchCatalog(%q) error inesperado: %v", tc.query, err)
			}
			if !hasCatalogID(items, tc.want) {
				t.Fatalf("SearchCatalog(%q) = %v, se esperaba encontrar %s", tc.query, ids(items), tc.want)
			}
		})
	}

	// The v1.11 fields travel with the result: that IS the agent knowledge.
	items, err := repo.SearchCatalog(ctx, dto.SearchCatalogQuery{Text: "lumbar", Limit: 10})
	if err != nil {
		t.Fatalf("SearchCatalog() error inesperado: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("resultados = %v, se esperaba uno", ids(items))
	}
	if items[0].UseCases != "Dolor lumbar y cervical" || items[0].Limitations != "No cubre cirugías" {
		t.Fatalf("el resultado no lleva el conocimiento del ítem: %+v", items[0])
	}
	if items[0].PriceCents == nil || *items[0].PriceCents != 500000 {
		t.Fatalf("price_cents = %v, se esperaban 500000", items[0].PriceCents)
	}
}

// TestCatalogSearchOnlyActiveItems pins the active-only rule: a deactivated item
// never reaches the agent, neither through the search nor through the browse,
// while the panel can still read it by id.
func TestCatalogSearchOnlyActiveItems(t *testing.T) {
	handle := openDB(t)
	repo := newCatalogOf(t, handle)
	ctx := context.Background()

	seedCatalog(t, repo, dto.NewCatalogItem{
		ID:   uuid(430),
		Item: dto.CatalogItemInput{Kind: dto.CatalogKindServicio, Name: "Manicura", UseCases: "Uñas quebradizas"},
	})
	if _, err := handle.Exec(`UPDATE catalog_items SET is_active = 0 WHERE id = ?`, uuid(430)); err != nil {
		t.Fatalf("desactivar el ítem: %v", err)
	}

	for _, query := range []dto.SearchCatalogQuery{
		{Text: "manicura", Limit: 10},
		{Text: "", Limit: 10},
	} {
		items, err := repo.SearchCatalog(ctx, query)
		if err != nil {
			t.Fatalf("SearchCatalog(%q) error inesperado: %v", query.Text, err)
		}
		if len(items) != 0 {
			t.Fatalf("SearchCatalog(%q) = %v, un ítem desactivado no debe aparecer", query.Text, ids(items))
		}
	}

	if _, found, err := repo.GetCatalogItem(ctx, uuid(430)); err != nil || !found {
		t.Fatalf("GetCatalogItem() de un ítem desactivado = %t, %v; el panel debe poder leerlo", found, err)
	}
}

// TestCatalogSearchBrowseAndPagination pins the empty-text path and the page
// window: browse lists active items by name, and limit/offset walk the list.
func TestCatalogSearchBrowseAndPagination(t *testing.T) {
	repo := newCatalogOf(t, openDB(t))
	ctx := context.Background()

	for i, name := range []string{"Corte", "Barba", "Color"} {
		seedCatalog(t, repo, dto.NewCatalogItem{
			ID:   uuid(440 + i),
			Item: dto.CatalogItemInput{Kind: dto.CatalogKindServicio, Name: name},
		})
	}

	all, err := repo.SearchCatalog(ctx, dto.SearchCatalogQuery{Limit: 10})
	if err != nil {
		t.Fatalf("SearchCatalog() error inesperado: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("resultados = %v, se esperaban los tres ítems activos", ids(all))
	}
	if all[0].Name != "Barba" || all[2].Name != "Corte" {
		t.Fatalf("orden = %v, se esperaba por nombre", names(all))
	}

	page, err := repo.SearchCatalog(ctx, dto.SearchCatalogQuery{Limit: 1, Offset: 1})
	if err != nil {
		t.Fatalf("SearchCatalog() error inesperado: %v", err)
	}
	if len(page) != 1 || page[0].Name != "Color" {
		t.Fatalf("página = %v, se esperaba solo Color", names(page))
	}

	// Punctuation-only text matches nothing instead of dumping the catalog.
	empty, err := repo.SearchCatalog(ctx, dto.SearchCatalogQuery{Text: "¿?!", Limit: 10})
	if err != nil {
		t.Fatalf("SearchCatalog() error inesperado: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("resultados = %v, se esperaba ninguno", ids(empty))
	}

	for _, invalid := range []dto.SearchCatalogQuery{{Text: "corte"}, {Text: "corte", Limit: 10, Offset: -1}} {
		if _, err := repo.SearchCatalog(ctx, invalid); err == nil {
			t.Fatalf("SearchCatalog(%+v) debía rechazarse: la paginación es obligatoria", invalid)
		}
	}
}

// TestCatalogSearchNeutralizesFTSGrammar pins that the caller's text can never
// become FTS5 syntax: operators, quotes and column filters are searched for as
// literal words (and simply do not match), instead of erroring out or filtering
// by a column the caller chose.
func TestCatalogSearchNeutralizesFTSGrammar(t *testing.T) {
	repo := newCatalogOf(t, openDB(t))
	ctx := context.Background()

	seedCatalog(t, repo, dto.NewCatalogItem{
		ID:   uuid(450),
		Item: dto.CatalogItemInput{Kind: dto.CatalogKindProducto, Name: "Crema", Description: "Pote de 100 g"},
	})

	for _, hostile := range []string{
		`"crema" OR name:crema`,
		`crema NEAR/1 potes`,
		`crema") --`,
		`name:crema`,
		`*`,
		`^crema`,
	} {
		items, err := repo.SearchCatalog(ctx, dto.SearchCatalogQuery{Text: hostile, Limit: 10})
		if err != nil {
			t.Fatalf("SearchCatalog(%q) error inesperado: %v (la gramática FTS5 no debe ser interpretable)", hostile, err)
		}
		// Whatever the words are, an FTS syntax term must not error and must not
		// be able to select rows by column.
		if hasCatalogID(items, uuid(450)) && !strings.Contains(hostile, "crema") {
			t.Fatalf("SearchCatalog(%q) encontró el ítem sin la palabra buscada", hostile)
		}
	}

	// The legitimate word still works even when wrapped in hostile punctuation.
	items, err := repo.SearchCatalog(ctx, dto.SearchCatalogQuery{Text: `"crema"`, Limit: 10})
	if err != nil {
		t.Fatalf("SearchCatalog() error inesperado: %v", err)
	}
	if !hasCatalogID(items, uuid(450)) {
		t.Fatalf("SearchCatalog(%q) = %v, se esperaba encontrar el ítem", `"crema"`, ids(items))
	}
}

// TestCatalogRepoPropagatesDatabaseFailure pins that an infrastructure failure is
// reported as an error and never as an empty result: an empty catalog and a
// broken database must not look the same to the agent.
func TestCatalogRepoPropagatesDatabaseFailure(t *testing.T) {
	handle := openDB(t)
	repo := newCatalogOf(t, handle)
	ctx := context.Background()

	if _, err := handle.Exec(`DROP TABLE catalog_items_fts`); err != nil {
		t.Fatalf("romper el índice FTS: %v", err)
	}
	if _, err := repo.SearchCatalog(ctx, dto.SearchCatalogQuery{Text: "crema", Limit: 10}); err == nil {
		t.Fatal("SearchCatalog() debía fallar con el índice ausente")
	}

	if _, err := handle.Exec(`DROP TABLE catalog_items`); err != nil {
		t.Fatalf("romper la tabla: %v", err)
	}
	if _, _, err := repo.GetCatalogItem(ctx, uuid(460)); err == nil {
		t.Fatal("GetCatalogItem() debía fallar con la tabla ausente")
	}
	if err := repo.CreateCatalogItem(ctx, dto.NewCatalogItem{ID: uuid(461), Item: dto.CatalogItemInput{Kind: dto.CatalogKindProducto, Name: "X"}}); err == nil {
		t.Fatal("CreateCatalogItem() debía fallar con la tabla ausente")
	}
	if _, err := repo.UpdateCatalogItem(ctx, dto.CatalogItemUpdate{ID: uuid(462), Item: dto.CatalogItemInput{Kind: dto.CatalogKindProducto, Name: "X"}}); err == nil {
		t.Fatal("UpdateCatalogItem() no debe confundir una base rota con una fila inexistente")
	}
}

// seedCatalog inserts one item through the adapter (the tests exercise the
// adapter, so the fixtures use it instead of raw SQL).
func seedCatalog(t *testing.T, repo *repository.CatalogRepo, item dto.NewCatalogItem) {
	t.Helper()
	if err := repo.CreateCatalogItem(context.Background(), item); err != nil {
		t.Fatalf("sembrar el ítem %q: %v", item.Item.Name, err)
	}
}

// queryScalar reads one string value (typeof/COUNT/updated_at assertions).
func queryScalar(t *testing.T, handle *sql.DB, query string, args ...any) string {
	t.Helper()
	var value string
	if err := handle.QueryRow(query, args...).Scan(&value); err != nil {
		t.Fatalf("consulta %q: %v", query, err)
	}
	return value
}

func hasCatalogID(items []dto.CatalogItemRecord, want string) bool {
	for _, item := range items {
		if item.ID == want {
			return true
		}
	}
	return false
}

func ids(items []dto.CatalogItemRecord) []string {
	values := make([]string, 0, len(items))
	for _, item := range items {
		values = append(values, item.ID)
	}
	return values
}

func names(items []dto.CatalogItemRecord) []string {
	values := make([]string, 0, len(items))
	for _, item := range items {
		values = append(values, item.Name)
	}
	return values
}
