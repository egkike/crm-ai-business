package usecase_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/egkike/crm-ai-business/internal/application/dto"
	"github.com/egkike/crm-ai-business/internal/application/usecase"
	"github.com/egkike/crm-ai-business/internal/auth"
)

// fakeCatalog is the CatalogStore double: it records the commands the use cases
// send and replays the configured answers. The use case tests exercise
// decisions (RBAC, validation, semantics), so the port must be answerable; the
// real SQL is pinned by the repository tests.
type fakeCatalog struct {
	createErr error
	updateErr error
	lookupErr error
	searchErr error

	found   bool
	records map[string]dto.CatalogItemRecord

	created  []dto.NewCatalogItem
	updated  []dto.CatalogItemUpdate
	searched []dto.SearchCatalogQuery
	lookups  []string
}

func newFakeCatalog() *fakeCatalog {
	return &fakeCatalog{records: map[string]dto.CatalogItemRecord{}}
}

func (f *fakeCatalog) CreateCatalogItem(_ context.Context, item dto.NewCatalogItem) error {
	if f.createErr != nil {
		return f.createErr
	}
	f.created = append(f.created, item)
	// What the store holds after the write: the use case reads it back.
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

func (f *fakeCatalog) UpdateCatalogItem(_ context.Context, item dto.CatalogItemUpdate) (bool, error) {
	if f.updateErr != nil {
		return false, f.updateErr
	}
	f.updated = append(f.updated, item)
	if !f.found {
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

func (f *fakeCatalog) GetCatalogItem(_ context.Context, id string) (dto.CatalogItemRecord, bool, error) {
	f.lookups = append(f.lookups, id)
	if f.lookupErr != nil {
		return dto.CatalogItemRecord{}, false, f.lookupErr
	}
	record, ok := f.records[id]
	return record, ok, nil
}

func (f *fakeCatalog) SearchCatalog(_ context.Context, query dto.SearchCatalogQuery) ([]dto.CatalogItemRecord, error) {
	if f.searchErr != nil {
		return nil, f.searchErr
	}
	f.searched = append(f.searched, query)
	return []dto.CatalogItemRecord{{
		ID:          "22222222-2222-4222-8222-000000000001",
		Kind:        dto.CatalogKindServicio,
		Name:        "Corte de pelo",
		UseCases:    "Flequillo incómodo",
		Limitations: "No incluye coloración",
	}}, nil
}

// callers of every role, built from auth.Caller so a test states only the role.
func callerWithRole(role auth.Role) auth.Caller {
	return auth.Caller{ID: "33333333-3333-4333-8333-000000000001", Name: "Prueba", Role: role}
}

// TestSearchCatalogAcceptsEveryResolvedRole pins the RF4 search permission: a
// client asking what the business offers, the staff answering and the owner
// reviewing all search; an unresolved caller (the zero role) is refused by the
// use case even though the transport already gated the tool — that is the
// double RBAC of AGENTS.md.
func TestSearchCatalogAcceptsEveryResolvedRole(t *testing.T) {
	for _, role := range []auth.Role{auth.RoleOwner, auth.RoleAdmin, auth.RoleStaff, auth.RoleClient} {
		t.Run(string(role), func(t *testing.T) {
			store := newFakeCatalog()
			uc, err := usecase.NewSearchCatalog(store)
			if err != nil {
				t.Fatalf("NewSearchCatalog() error inesperado: %v", err)
			}

			result, err := uc.Execute(context.Background(), callerWithRole(role), dto.SearchCatalogQuery{Text: "corte"})
			if err != nil {
				t.Fatalf("Execute() error inesperado: %v", err)
			}
			if len(result.Items) != 1 || result.Items[0].UseCases != "Flequillo incómodo" {
				t.Fatalf("resultados = %+v, se esperaba el ítem con su conocimiento", result.Items)
			}
		})
	}

	t.Run("sin rol resuelto", func(t *testing.T) {
		store := newFakeCatalog()
		uc, err := usecase.NewSearchCatalog(store)
		if err != nil {
			t.Fatalf("NewSearchCatalog() error inesperado: %v", err)
		}

		result, err := uc.Execute(context.Background(), auth.Caller{}, dto.SearchCatalogQuery{Text: "corte"})
		assertSemantic(t, err, usecase.CodeRoleNotAllowed, "tu rol no permite consultar el catálogo.")
		if len(result.Items) != 0 {
			t.Fatalf("resultados = %+v, se esperaba ninguno", result.Items)
		}
		if len(store.searched) != 0 {
			t.Fatal("la búsqueda llegó al repositorio sin rol resuelto")
		}
	})
}

// TestSearchCatalogNormalizesPagination pins the page policy: the default is
// small, an oversized request is capped, and a negative offset is zero.
func TestSearchCatalogNormalizesPagination(t *testing.T) {
	cases := []struct {
		name       string
		query      dto.SearchCatalogQuery
		wantLimit  int
		wantOffset int
	}{
		{name: "sin límite usa el default", query: dto.SearchCatalogQuery{Text: "corte"}, wantLimit: usecase.DefaultCatalogSearchLimit},
		{name: "límite negativo usa el default", query: dto.SearchCatalogQuery{Text: "corte", Limit: -3}, wantLimit: usecase.DefaultCatalogSearchLimit},
		{name: "límite excesivo se recorta", query: dto.SearchCatalogQuery{Text: "corte", Limit: 5000}, wantLimit: usecase.MaxCatalogSearchLimit},
		{name: "offset negativo es cero", query: dto.SearchCatalogQuery{Text: "corte", Limit: 5, Offset: -10}, wantLimit: 5, wantOffset: 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeCatalog()
			uc, err := usecase.NewSearchCatalog(store)
			if err != nil {
				t.Fatalf("NewSearchCatalog() error inesperado: %v", err)
			}

			result, err := uc.Execute(context.Background(), callerWithRole(auth.RoleClient), tc.query)
			if err != nil {
				t.Fatalf("Execute() error inesperado: %v", err)
			}
			if len(store.searched) != 1 {
				t.Fatalf("búsquedas = %d, se esperaba 1", len(store.searched))
			}
			got := store.searched[0]
			if got.Limit != tc.wantLimit || got.Offset != tc.wantOffset {
				t.Fatalf("paginación enviada = (%d, %d), se esperaba (%d, %d)", got.Limit, got.Offset, tc.wantLimit, tc.wantOffset)
			}
			// The applied window travels back: the caller learns which page it got.
			if result.Limit != tc.wantLimit || result.Offset != tc.wantOffset {
				t.Fatalf("paginación aplicada = (%d, %d), se esperaba (%d, %d)", result.Limit, result.Offset, tc.wantLimit, tc.wantOffset)
			}
		})
	}
}

// TestCatalogEditingRejectsStaffAndClients pins RF4's "alta/edición por Owner/
// Admin" at the use case layer: the transport already hides the tools, and the
// use case refuses on its own, with Spanish prose and a machine-readable code.
func TestCatalogEditingRejectsStaffAndClients(t *testing.T) {
	item := dto.CatalogItemInput{Kind: dto.CatalogKindServicio, Name: "Corte de pelo"}

	for _, role := range []auth.Role{auth.RoleStaff, auth.RoleClient, ""} {
		t.Run("crear como "+string(role), func(t *testing.T) {
			store := newFakeCatalog()
			uc, err := usecase.NewCreateCatalogItem(store)
			if err != nil {
				t.Fatalf("NewCreateCatalogItem() error inesperado: %v", err)
			}

			record, err := uc.Execute(context.Background(), callerWithRole(role), item)
			assertSemantic(t, err, usecase.CodeRoleNotAllowed, "tu rol no permite administrar el catálogo del negocio.")
			if record != (dto.CatalogItemRecord{}) {
				t.Fatalf("registro = %+v, se esperaba el cero", record)
			}
			if len(store.created) != 0 {
				t.Fatal("la creación llegó al repositorio con un rol sin permiso")
			}
		})

		t.Run("editar como "+string(role), func(t *testing.T) {
			store := newFakeCatalog()
			uc, err := usecase.NewUpdateCatalogItem(store)
			if err != nil {
				t.Fatalf("NewUpdateCatalogItem() error inesperado: %v", err)
			}

			record, err := uc.Execute(context.Background(), callerWithRole(role), "44444444-4444-4444-8444-000000000001", item)
			assertSemantic(t, err, usecase.CodeRoleNotAllowed, "tu rol no permite administrar el catálogo del negocio.")
			if record != (dto.CatalogItemRecord{}) {
				t.Fatalf("registro = %+v, se esperaba el cero", record)
			}
			if len(store.updated) != 0 {
				t.Fatal("la edición llegó al repositorio con un rol sin permiso")
			}
		})
	}
}

// TestCreateCatalogItemPersistsAndReturnsTheStoredRow pins the happy path of the
// Owner/Admin surface: the internal UUID is generated by the use case, the
// payload is persisted and the result is the row read back from the store.
func TestCreateCatalogItemPersistsAndReturnsTheStoredRow(t *testing.T) {
	for _, role := range []auth.Role{auth.RoleOwner, auth.RoleAdmin} {
		t.Run(string(role), func(t *testing.T) {
			store := newFakeCatalog()
			uc, err := usecase.NewCreateCatalogItem(store)
			if err != nil {
				t.Fatalf("NewCreateCatalogItem() error inesperado: %v", err)
			}
			price := int64(150000)
			duration := 45

			record, err := uc.Execute(context.Background(), callerWithRole(role), dto.CatalogItemInput{
				Kind:        dto.CatalogKindServicio,
				Name:        "  Corte de pelo  ",
				Description: "Corte y peinado",
				UseCases:    "Flequillo incómodo",
				Limitations: "No incluye coloración",
				PriceCents:  &price,
				DurationMin: &duration,
			})
			if err != nil {
				t.Fatalf("Execute() error inesperado: %v", err)
			}

			if len(store.created) != 1 {
				t.Fatalf("creados = %d, se esperaba 1", len(store.created))
			}
			row := store.created[0]
			if len(row.ID) != 36 {
				t.Fatalf("id generado = %q, se esperaba un UUID de 36 caracteres", row.ID)
			}
			if row.Item.Name != "Corte de pelo" {
				t.Fatalf("nombre persistido = %q, se esperaba sin espacios alrededor", row.Item.Name)
			}
			if record.ID != row.ID || record.UseCases != "Flequillo incómodo" || record.Limitations != "No incluye coloración" {
				t.Fatalf("registro = %+v, se esperaba la fila leída del repositorio", record)
			}
			if record.PriceCents == nil || *record.PriceCents != price {
				t.Fatalf("price_cents = %v, se esperaban %d centavos", record.PriceCents, price)
			}
		})
	}
}

// TestCatalogValidationIsSpanishAndSpecific pins the RF4 field rules the use case
// enforces before the database: each rejection names the field in Spanish, and
// nothing reaches the repository.
func TestCatalogValidationIsSpanishAndSpecific(t *testing.T) {
	price := int64(1000)
	negative := int64(-1)
	duration := 30
	zeroDuration := 0
	hugeDuration := 2000

	cases := []struct {
		name    string
		item    dto.CatalogItemInput
		code    usecase.ErrorCode
		message string
	}{
		{
			name:    "kind fuera del vocabulario",
			item:    dto.CatalogItemInput{Kind: "alquiler", Name: "Algo"},
			code:    usecase.CodeInvalidCatalogKind,
			message: `el tipo de ítem debe ser "producto" o "servicio".`,
		},
		{
			name:    "sin nombre",
			item:    dto.CatalogItemInput{Kind: dto.CatalogKindProducto, Name: "   "},
			code:    usecase.CodeInvalidCatalogItem,
			message: "el nombre del ítem es obligatorio.",
		},
		{
			name:    "nombre demasiado largo",
			item:    dto.CatalogItemInput{Kind: dto.CatalogKindProducto, Name: strings.Repeat("a", 121)},
			code:    usecase.CodeInvalidCatalogItem,
			message: "el nombre del ítem no puede superar los 120 caracteres.",
		},
		{
			name:    "precio negativo",
			item:    dto.CatalogItemInput{Kind: dto.CatalogKindProducto, Name: "Crema", PriceCents: &negative},
			code:    usecase.CodeInvalidCatalogItem,
			message: "el precio no puede ser negativo (se expresa en centavos); si todavía no tiene precio, omitilo y quedará a consultar.",
		},
		{
			name:    "producto con duración (no reservable)",
			item:    dto.CatalogItemInput{Kind: dto.CatalogKindProducto, Name: "Crema", PriceCents: &price, DurationMin: &duration},
			code:    usecase.CodeInvalidCatalogItem,
			message: "un producto no puede tener duración: solo los servicios son reservables.",
		},
		{
			name:    "duración no positiva",
			item:    dto.CatalogItemInput{Kind: dto.CatalogKindServicio, Name: "Corte", DurationMin: &zeroDuration},
			code:    usecase.CodeInvalidCatalogItem,
			message: "la duración del servicio debe estar entre 1 y 1440 minutos.",
		},
		{
			name:    "duración mayor a un día",
			item:    dto.CatalogItemInput{Kind: dto.CatalogKindServicio, Name: "Corte", DurationMin: &hugeDuration},
			code:    usecase.CodeInvalidCatalogItem,
			message: "la duración del servicio debe estar entre 1 y 1440 minutos.",
		},
		{
			name: "descripción demasiado larga",
			item: dto.CatalogItemInput{
				Kind: dto.CatalogKindProducto, Name: "Crema", Description: strings.Repeat("b", 2001),
			},
			code:    usecase.CodeInvalidCatalogItem,
			message: "la descripción del ítem no puede superar los 2000 caracteres.",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeCatalog()
			uc, err := usecase.NewCreateCatalogItem(store)
			if err != nil {
				t.Fatalf("NewCreateCatalogItem() error inesperado: %v", err)
			}

			record, err := uc.Execute(context.Background(), callerWithRole(auth.RoleOwner), tc.item)
			assertSemantic(t, err, tc.code, tc.message)
			if record != (dto.CatalogItemRecord{}) {
				t.Fatalf("registro = %+v, se esperaba el cero", record)
			}
			if len(store.created) != 0 {
				t.Fatal("la validación no ocurrió antes de escribir")
			}
		})
	}
}

// TestUpdateCatalogItemSemantics pins the edition outcomes: a valid payload
// replaces the row and returns what the store holds, an id no row carries is the
// Spanish "no existe" rejection, and a missing id is an invalid item.
func TestUpdateCatalogItemSemantics(t *testing.T) {
	const id = "44444444-4444-4444-8444-000000000002"

	t.Run("reemplaza y devuelve la fila guardada", func(t *testing.T) {
		store := newFakeCatalog()
		store.found = true
		uc, err := usecase.NewUpdateCatalogItem(store)
		if err != nil {
			t.Fatalf("NewUpdateCatalogItem() error inesperado: %v", err)
		}

		record, err := uc.Execute(context.Background(), callerWithRole(auth.RoleOwner), id,
			dto.CatalogItemInput{Kind: dto.CatalogKindProducto, Name: "Aceite esencial", UseCases: "Piel seca"})
		if err != nil {
			t.Fatalf("Execute() error inesperado: %v", err)
		}
		if len(store.updated) != 1 || store.updated[0].ID != id {
			t.Fatalf("ediciones = %+v, se esperaba una sobre %s", store.updated, id)
		}
		if record.ID != id || record.Kind != dto.CatalogKindProducto || record.UseCases != "Piel seca" {
			t.Fatalf("registro devuelto = %+v, se esperaba la fila guardada", record)
		}
	})

	t.Run("id inexistente", func(t *testing.T) {
		store := newFakeCatalog()
		uc, err := usecase.NewUpdateCatalogItem(store)
		if err != nil {
			t.Fatalf("NewUpdateCatalogItem() error inesperado: %v", err)
		}

		_, err = uc.Execute(context.Background(), callerWithRole(auth.RoleOwner), id,
			dto.CatalogItemInput{Kind: dto.CatalogKindProducto, Name: "Fantasma"})
		assertSemantic(t, err, usecase.CodeCatalogItemNotFound, "no existe un ítem del catálogo con ese identificador.")
	})

	t.Run("id vacío", func(t *testing.T) {
		store := newFakeCatalog()
		uc, err := usecase.NewUpdateCatalogItem(store)
		if err != nil {
			t.Fatalf("NewUpdateCatalogItem() error inesperado: %v", err)
		}

		_, err = uc.Execute(context.Background(), callerWithRole(auth.RoleOwner), "",
			dto.CatalogItemInput{Kind: dto.CatalogKindProducto, Name: "Algo"})
		assertSemantic(t, err, usecase.CodeInvalidCatalogItem, "el identificador del ítem es obligatorio.")
		if len(store.updated) != 0 {
			t.Fatal("la edición llegó al repositorio sin identificador")
		}
	})
}

// TestCatalogUseCasesPropagateInternalFailures pins that a persistence failure
// keeps its class: it is NOT semantic, so the transport collapses it into the
// generic internal error instead of telling the caller something untrue.
func TestCatalogUseCasesPropagateInternalFailures(t *testing.T) {
	cause := errors.New("sqlite: no such table: catalog_items")
	ctx := context.Background()

	searchStore := newFakeCatalog()
	searchStore.searchErr = cause
	search, err := usecase.NewSearchCatalog(searchStore)
	if err != nil {
		t.Fatalf("NewSearchCatalog() error inesperado: %v", err)
	}
	if _, err := search.Execute(ctx, callerWithRole(auth.RoleOwner), dto.SearchCatalogQuery{Text: "corte"}); !errors.Is(err, cause) {
		t.Fatalf("err = %v, se esperaba el error interno propagado", err)
	}

	createStore := newFakeCatalog()
	createStore.createErr = cause
	create, err := usecase.NewCreateCatalogItem(createStore)
	if err != nil {
		t.Fatalf("NewCreateCatalogItem() error inesperado: %v", err)
	}
	if _, err := create.Execute(ctx, callerWithRole(auth.RoleOwner), dto.CatalogItemInput{Kind: dto.CatalogKindProducto, Name: "Crema"}); !errors.Is(err, cause) {
		t.Fatalf("err = %v, se esperaba el error interno propagado", err)
	}

	updateStore := newFakeCatalog()
	updateStore.found = true
	updateStore.updateErr = cause
	update, err := usecase.NewUpdateCatalogItem(updateStore)
	if err != nil {
		t.Fatalf("NewUpdateCatalogItem() error inesperado: %v", err)
	}
	if _, err := update.Execute(ctx, callerWithRole(auth.RoleOwner), "44444444-4444-4444-8444-000000000003",
		dto.CatalogItemInput{Kind: dto.CatalogKindProducto, Name: "Crema"}); !errors.Is(err, cause) {
		t.Fatalf("err = %v, se esperaba el error interno propagado", err)
	}
}

// TestNewCatalogUseCasesRejectNilStore pins the fail-closed construction rule of
// the three use cases: a nil store is a wiring error, never a silent no-op.
func TestNewCatalogUseCasesRejectNilStore(t *testing.T) {
	if uc, err := usecase.NewSearchCatalog(nil); err == nil || uc != nil {
		t.Fatal("NewSearchCatalog(nil) debía fallar (fail-closed)")
	}
	if uc, err := usecase.NewCreateCatalogItem(nil); err == nil || uc != nil {
		t.Fatal("NewCreateCatalogItem(nil) debía fallar (fail-closed)")
	}
	if uc, err := usecase.NewUpdateCatalogItem(nil); err == nil || uc != nil {
		t.Fatal("NewUpdateCatalogItem(nil) debía fallar (fail-closed)")
	}
}
