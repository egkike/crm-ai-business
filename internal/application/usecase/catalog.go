package usecase

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/egkike/crm-ai-business/internal/application/dto"
	"github.com/egkike/crm-ai-business/internal/auth"
)

// This file is the application layer of RF4 (catálogo unificado): full-text
// search for everyone the identity chain resolved, and high/edición restricted
// to Owner/Admin.
//
// Every use case re-asserts its own authorization (AGENTS.md boundary contract
// 1, double RBAC): the MCP transport already filtered the tool by role, and the
// re-assertion is the independent barrier that still holds when the same use
// case is reached from another adapter — the internal panel of F4 — or when the
// transport's permission map is edited by mistake.

// CatalogStore is the driven port of catalog persistence.
//
// It exposes the four operations the use cases need and nothing else: no delete
// (an item is deactivated, never destroyed, so historical references stay
// valid) and no batch write.
type CatalogStore interface {
	// CreateCatalogItem inserts one item. The row is fully described by the
	// carrier: the application layer owns the id and the validation.
	CreateCatalogItem(ctx context.Context, item dto.NewCatalogItem) error

	// UpdateCatalogItem replaces the editable fields of the row id. It reports
	// found=false when no row carries that id, which the use case renders as
	// the Spanish "no existe" rejection.
	UpdateCatalogItem(ctx context.Context, item dto.CatalogItemUpdate) (bool, error)

	// GetCatalogItem returns one stored item, or found=false.
	GetCatalogItem(ctx context.Context, id string) (dto.CatalogItemRecord, bool, error)

	// SearchCatalog returns the items matching query. An empty query is the
	// browse path (active items by name); a non-empty one is full-text over the
	// v1.11 agent knowledge.
	SearchCatalog(ctx context.Context, query dto.SearchCatalogQuery) ([]dto.CatalogItemRecord, error)
}

const (
	// DefaultCatalogSearchLimit is the page size when the caller sends none: a
	// small page keeps the agent's context short, which is the point of a
	// lightweight search (RF4).
	DefaultCatalogSearchLimit = 10

	// MaxCatalogSearchLimit bounds one page. The limit is an input, and an
	// unbounded page is a way to pull the whole catalog into an LLM context.
	MaxCatalogSearchLimit = 50

	// maxCatalogNameLen bounds the item's name.
	maxCatalogNameLen = 120

	// maxCatalogTextLen bounds description, use_cases and limitations. A long
	// limitation is a legitimate paragraph, not a place to hide a payload.
	maxCatalogTextLen = 2000

	// maxCatalogSalesURLLen bounds the online purchase deep-link.
	maxCatalogSalesURLLen = 500

	// maxCatalogDurationMin bounds a service duration: a booking that lasts more
	// than a day is a data error, not a service.
	maxCatalogDurationMin = 24 * 60
)

// requireCatalogReader re-asserts the search permission: every RESOLVED role
// may search — a client asks what the business offers, the staff answers, the
// owner reviews. The zero role (no resolved caller) is refused: an anonymous
// sender has to register first, which is the gateway's RF3 loop, not a catalog
// read.
func requireCatalogReader(caller auth.Caller) error {
	if caller.Role.Valid() {
		return nil
	}
	return newSemanticError(CodeRoleNotAllowed,
		"tu rol no permite consultar el catálogo.")
}

// requireCatalogEditor re-asserts the configuration permission of RF4 ("alta/
// edición por Owner/Admin"). An invalid role answers false in
// CanManageWorkflows, so an unrecognized row cannot edit the catalog.
func requireCatalogEditor(caller auth.Caller) error {
	if caller.Role.CanManageWorkflows() {
		return nil
	}
	return newSemanticError(CodeRoleNotAllowed,
		"tu rol no permite administrar el catálogo del negocio.")
}

// SearchCatalog is the RF4 search use case behind search_catalog.
type SearchCatalog struct {
	catalog CatalogStore
}

// NewSearchCatalog wires the use case. A nil store is a fail-closed wiring
// error: without persistence the use case could only fail per request.
func NewSearchCatalog(catalog CatalogStore) (*SearchCatalog, error) {
	if catalog == nil {
		return nil, errors.New("catálogo: se requiere un CatalogStore para la búsqueda")
	}
	return &SearchCatalog{catalog: catalog}, nil
}

// Execute returns the page of items that match the caller's words.
//
// The text is free-form on purpose: Hermes searches with the client's own
// phrasing ("¿tenés corte de pelo?"), and the repository turns it into a safe
// FTS5 expression. Pagination is normalized here so a caller cannot ask for an
// unbounded page or a negative offset.
func (uc *SearchCatalog) Execute(ctx context.Context, caller auth.Caller, query dto.SearchCatalogQuery) (dto.CatalogSearchResult, error) {
	if err := requireCatalogReader(caller); err != nil {
		return dto.CatalogSearchResult{}, err
	}

	query.Limit = normalizeSearchLimit(query.Limit)
	if query.Offset < 0 {
		query.Offset = 0
	}

	items, err := uc.catalog.SearchCatalog(ctx, query)
	if err != nil {
		return dto.CatalogSearchResult{}, fmt.Errorf("catálogo: buscar ítems: %w", err)
	}

	return dto.CatalogSearchResult{Items: items, Limit: query.Limit, Offset: query.Offset}, nil
}

// normalizeSearchLimit applies the product page policy: absent (or nonsense)
// means the default, and an oversized request is capped instead of refused — a
// caller asking for more results than the product serves gets the largest page
// the product serves.
func normalizeSearchLimit(limit int) int {
	if limit <= 0 {
		return DefaultCatalogSearchLimit
	}
	if limit > MaxCatalogSearchLimit {
		return MaxCatalogSearchLimit
	}
	return limit
}

// CreateCatalogItem is the RF4 creation use case behind create_catalog_item.
type CreateCatalogItem struct {
	catalog CatalogStore
}

// NewCreateCatalogItem wires the use case.
func NewCreateCatalogItem(catalog CatalogStore) (*CreateCatalogItem, error) {
	if catalog == nil {
		return nil, errors.New("catálogo: se requiere un CatalogStore para crear ítems")
	}
	return &CreateCatalogItem{catalog: catalog}, nil
}

// Execute validates the item, assigns the internal UUID, persists it and returns
// the stored row.
//
// The returned record is read back from the store instead of being echoed from
// the input: what the caller sees is what the database holds.
func (uc *CreateCatalogItem) Execute(ctx context.Context, caller auth.Caller, item dto.CatalogItemInput) (dto.CatalogItemRecord, error) {
	if err := requireCatalogEditor(caller); err != nil {
		return dto.CatalogItemRecord{}, err
	}

	normalized, err := validateCatalogItem(item)
	if err != nil {
		return dto.CatalogItemRecord{}, err
	}

	id, err := newUUID()
	if err != nil {
		return dto.CatalogItemRecord{}, fmt.Errorf("catálogo: crear el ítem: %w", err)
	}

	if err := uc.catalog.CreateCatalogItem(ctx, dto.NewCatalogItem{ID: id, Item: normalized}); err != nil {
		return dto.CatalogItemRecord{}, fmt.Errorf("catálogo: crear el ítem: %w", err)
	}
	return uc.stored(ctx, id)
}

// stored reads back the row the use case just wrote.
func (uc *CreateCatalogItem) stored(ctx context.Context, id string) (dto.CatalogItemRecord, error) {
	record, found, err := uc.catalog.GetCatalogItem(ctx, id)
	if err != nil {
		return dto.CatalogItemRecord{}, fmt.Errorf("catálogo: leer el ítem creado: %w", err)
	}
	if !found {
		return dto.CatalogItemRecord{}, fmt.Errorf("catálogo: el ítem %s no se encontró después de crearlo", id)
	}
	return record, nil
}

// UpdateCatalogItem is the RF4 edition use case behind update_catalog_item.
type UpdateCatalogItem struct {
	catalog CatalogStore
}

// NewUpdateCatalogItem wires the use case.
func NewUpdateCatalogItem(catalog CatalogStore) (*UpdateCatalogItem, error) {
	if catalog == nil {
		return nil, errors.New("catálogo: se requiere un CatalogStore para editar ítems")
	}
	return &UpdateCatalogItem{catalog: catalog}, nil
}

// Execute replaces the editable fields of an existing item.
//
// Replacement, not patching: the payload carries every field, so a caller never
// has to distinguish "leave as is" from "clear it" — and a nullable text column
// has no third state a JSON argument could express anyway. An id no row carries
// is the semantic "no existe" rejection, never a silent no-op.
func (uc *UpdateCatalogItem) Execute(ctx context.Context, caller auth.Caller, id string, item dto.CatalogItemInput) (dto.CatalogItemRecord, error) {
	if err := requireCatalogEditor(caller); err != nil {
		return dto.CatalogItemRecord{}, err
	}

	if id == "" {
		return dto.CatalogItemRecord{}, invalidCatalogItemError("el identificador del ítem es obligatorio.")
	}

	normalized, err := validateCatalogItem(item)
	if err != nil {
		return dto.CatalogItemRecord{}, err
	}

	found, err := uc.catalog.UpdateCatalogItem(ctx, dto.CatalogItemUpdate{ID: id, Item: normalized})
	if err != nil {
		return dto.CatalogItemRecord{}, fmt.Errorf("catálogo: editar el ítem: %w", err)
	}
	if !found {
		return dto.CatalogItemRecord{}, newSemanticError(CodeCatalogItemNotFound,
			"no existe un ítem del catálogo con ese identificador.")
	}

	record, found, err := uc.catalog.GetCatalogItem(ctx, id)
	if err != nil {
		return dto.CatalogItemRecord{}, fmt.Errorf("catálogo: leer el ítem editado: %w", err)
	}
	if !found {
		return dto.CatalogItemRecord{}, fmt.Errorf("catálogo: el ítem %s no se encontró después de editarlo", id)
	}
	return record, nil
}

// validateCatalogItem applies the RF4 field rules and returns the payload with
// its text trimmed. It mirrors the schema constraints in Spanish: the database
// is the last barrier, and a CHECK violation must never be what the caller
// sees.
func validateCatalogItem(item dto.CatalogItemInput) (dto.CatalogItemInput, error) {
	normalized := dto.CatalogItemInput{
		Kind:        item.Kind,
		Name:        trimText(item.Name),
		Description: trimText(item.Description),
		UseCases:    trimText(item.UseCases),
		Limitations: trimText(item.Limitations),
		SalesURL:    trimText(item.SalesURL),
		PriceCents:  item.PriceCents,
		DurationMin: item.DurationMin,
	}

	if !normalized.Kind.Valid() {
		return dto.CatalogItemInput{}, newSemanticError(CodeInvalidCatalogKind,
			`el tipo de ítem debe ser "producto" o "servicio".`)
	}
	if normalized.Name == "" {
		return dto.CatalogItemInput{}, invalidCatalogItemError("el nombre del ítem es obligatorio.")
	}
	if err := checkTextLength("el nombre del ítem", normalized.Name, maxCatalogNameLen); err != nil {
		return dto.CatalogItemInput{}, err
	}
	if err := checkTextLength("la descripción del ítem", normalized.Description, maxCatalogTextLen); err != nil {
		return dto.CatalogItemInput{}, err
	}
	if err := checkTextLength("los casos de uso del ítem", normalized.UseCases, maxCatalogTextLen); err != nil {
		return dto.CatalogItemInput{}, err
	}
	if err := checkTextLength("las limitaciones del ítem", normalized.Limitations, maxCatalogTextLen); err != nil {
		return dto.CatalogItemInput{}, err
	}
	if err := checkTextLength("el enlace de venta", normalized.SalesURL, maxCatalogSalesURLLen); err != nil {
		return dto.CatalogItemInput{}, err
	}

	if normalized.PriceCents != nil && *normalized.PriceCents < 0 {
		return dto.CatalogItemInput{}, invalidCatalogItemError(
			"el precio no puede ser negativo (se expresa en centavos); si todavía no tiene precio, omitilo y quedará a consultar.")
	}

	if normalized.DurationMin != nil {
		// The bookable invariant of RF4 (and the CHECK of catalog_items): only a
		// service can be booked, so only a service can carry a duration.
		if !normalized.Kind.Bookable() {
			return dto.CatalogItemInput{}, invalidCatalogItemError(
				"un producto no puede tener duración: solo los servicios son reservables.")
		}
		if *normalized.DurationMin <= 0 || *normalized.DurationMin > maxCatalogDurationMin {
			return dto.CatalogItemInput{}, invalidCatalogItemError(
				"la duración del servicio debe estar entre 1 y 1440 minutos.")
		}
	}

	return normalized, nil
}

// checkTextLength rejects a field that exceeds its bound, naming it in Spanish.
func checkTextLength(label, value string, limit int) error {
	if utf8.RuneCountInString(value) > limit {
		return invalidCatalogItemError(fmt.Sprintf("%s no puede superar los %d caracteres.", label, limit))
	}
	return nil
}

// invalidCatalogItemError builds a field-level rejection with the shared
// invalid-item class: the caller branches on the code, a human reads the prose.
func invalidCatalogItemError(message string) error {
	return newSemanticError(CodeInvalidCatalogItem, message)
}

// trimText trims the surrounding whitespace of an optional text field. It does
// not touch the interior: what the Owner wrote is what Hermes quotes.
func trimText(value string) string {
	return strings.TrimSpace(value)
}
