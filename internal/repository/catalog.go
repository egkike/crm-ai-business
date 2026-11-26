package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/egkike/crm-ai-business/internal/application/dto"
)

// This file is the persistence adapter of RF4 (catálogo unificado): the CRUD and
// the full-text search over catalog_items. It decides nothing — it stores the
// validated row it is given and reports what it found, including the FTS5
// expression it derives from the caller's words.

// The catalog statements. Each one is a constant, parameterized with '?', and
// the search text never reaches the SQL text: it travels as a MATCH argument.
const (
	insertCatalogItem = `
INSERT INTO catalog_items (id, kind, name, description, use_cases, limitations, price_cents, sales_url, duration_min)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`

	// updateCatalogItem replaces every editable field of the row and refreshes
	// updated_at. is_active and created_at are not editable here: deactivating
	// an item is a separate decision (F4's panel), and created_at is history.
	updateCatalogItem = `
UPDATE catalog_items
SET kind = ?, name = ?, description = ?, use_cases = ?, limitations = ?,
    price_cents = ?, sales_url = ?, duration_min = ?,
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
WHERE id = ?`

	// catalogColumns is the projection every catalog read shares, so the
	// carrier is filled by one scan helper and cannot drift between queries.
	catalogColumns = `id, kind, name, description, use_cases, limitations, price_cents, sales_url, duration_min`

	selectCatalogItemByID = `SELECT ` + catalogColumns + ` FROM catalog_items WHERE id = ?`

	// searchCatalogItems is the RF4 full-text search: the match runs against the
	// FTS5 index over name/description/use_cases/limitations, joined back to the
	// base table for is_active and the row itself.
	//
	// bm25 orders best-first (it returns a negative score, lower is better), so
	// an item whose use_cases match the client's problem outranks an item whose
	// name only shares a word.
	searchCatalogItems = `
SELECT c.id, c.kind, c.name, c.description, c.use_cases, c.limitations, c.price_cents, c.sales_url, c.duration_min
FROM catalog_items_fts
JOIN catalog_items c ON c.rowid = catalog_items_fts.rowid
WHERE catalog_items_fts MATCH ?
  AND c.is_active = 1
ORDER BY bm25(catalog_items_fts), c.name
LIMIT ? OFFSET ?`

	// browseCatalogItems is the empty-query path: "show me what you offer",
	// active items by name. It deliberately keeps the same shape and ordering
	// contract as the search so a caller renders one list either way.
	browseCatalogItems = `
SELECT ` + catalogColumns + ` FROM catalog_items
WHERE is_active = 1
ORDER BY name COLLATE NOCASE
LIMIT ? OFFSET ?`
)

const (
	// maxFTSTokens bounds how many words of a query become search terms. A
	// longer query is a paragraph, and every extra term costs index work for
	// diminishing precision.
	maxFTSTokens = 16

	// maxFTSTokenLen bounds one search term.
	maxFTSTokenLen = 64
)

// CatalogRepo is the SQLite adapter of the catalog (satisfied by
// usecase.CatalogStore).
type CatalogRepo struct {
	db *sql.DB
}

// NewCatalogRepo wires the adapter to its database handle. A nil handle is a
// fail-closed configuration error: an adapter without a database can only fail.
func NewCatalogRepo(handle *sql.DB) (*CatalogRepo, error) {
	if handle == nil {
		return nil, errors.New("repositorio del catálogo: se requiere una conexión a la base")
	}
	return &CatalogRepo{db: handle}, nil
}

// CreateCatalogItem inserts one item. The nullable columns receive SQL NULL
// when the payload does not state them, which is what "not published" means to
// every reader (Hermes reports the price as "a consultar" instead of inventing
// one).
func (r *CatalogRepo) CreateCatalogItem(ctx context.Context, item dto.NewCatalogItem) error {
	if item.ID == "" {
		return errors.New("repositorio del catálogo: el ítem a crear no tiene id interno")
	}

	if _, err := r.db.ExecContext(ctx, insertCatalogItem,
		item.ID,
		string(item.Item.Kind),
		item.Item.Name,
		item.Item.Description,
		nullableText(item.Item.UseCases),
		nullableText(item.Item.Limitations),
		nullableCents(item.Item.PriceCents),
		nullableText(item.Item.SalesURL),
		nullableMinutes(item.Item.DurationMin),
	); err != nil {
		return fmt.Errorf("repositorio del catálogo: insertar el ítem: %w", err)
	}
	return nil
}

// UpdateCatalogItem replaces the editable fields and reports whether the id
// existed. Updating a missing row is NOT an error here: the use case owns the
// Spanish "no existe" rejection, so the adapter only reports the fact.
func (r *CatalogRepo) UpdateCatalogItem(ctx context.Context, item dto.CatalogItemUpdate) (bool, error) {
	if item.ID == "" {
		return false, errors.New("repositorio del catálogo: el ítem a editar no tiene id interno")
	}

	result, err := r.db.ExecContext(ctx, updateCatalogItem,
		string(item.Item.Kind),
		item.Item.Name,
		item.Item.Description,
		nullableText(item.Item.UseCases),
		nullableText(item.Item.Limitations),
		nullableCents(item.Item.PriceCents),
		nullableText(item.Item.SalesURL),
		nullableMinutes(item.Item.DurationMin),
		item.ID,
	)
	if err != nil {
		return false, fmt.Errorf("repositorio del catálogo: editar el ítem: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("repositorio del catálogo: confirmar la edición: %w", err)
	}
	return affected > 0, nil
}

// GetCatalogItem returns one stored item, active or not: the panel of F4 must be
// able to edit a deactivated item, and the use cases that care about activity
// filter it (the search does).
func (r *CatalogRepo) GetCatalogItem(ctx context.Context, id string) (dto.CatalogItemRecord, bool, error) {
	if id == "" {
		return dto.CatalogItemRecord{}, false, errors.New("repositorio del catálogo: se requiere el id del ítem")
	}

	record, err := scanCatalogItem(r.db.QueryRowContext(ctx, selectCatalogItemByID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return dto.CatalogItemRecord{}, false, nil
	}
	if err != nil {
		return dto.CatalogItemRecord{}, false, fmt.Errorf("repositorio del catálogo: leer el ítem: %w", err)
	}
	return record, true, nil
}

// SearchCatalog runs the RF4 search: full-text when the query has words,
// browse when it does not.
//
// The strategy, and why:
//
//   - The text is reduced to safe FTS5 terms (ftsExpression). The raw string is
//     never interpolated: FTS5 has its own query grammar (AND/OR/NEAR, quotes,
//     column filters), and pasting the client's prose into it would be both an
//     injection seam and a source of syntax errors on perfectly valid input.
//   - Terms are prefix-matched and OR-combined, ranked by bm25. A client asks
//     "¿tenés corte de pelo?" and the useful terms ("corte", "pelo") must each
//     be able to find the item, while the ranking still prefers the item that
//     matches more of them, in the indexed text of higher weight.
//   - is_active = 1 is enforced in SQL, so a deactivated item is never returned
//     to the agent even if the caller asks for it.
func (r *CatalogRepo) SearchCatalog(ctx context.Context, query dto.SearchCatalogQuery) ([]dto.CatalogItemRecord, error) {
	if query.Limit <= 0 {
		return nil, errors.New("repositorio del catálogo: la búsqueda requiere un límite positivo")
	}
	if query.Offset < 0 {
		return nil, errors.New("repositorio del catálogo: la búsqueda requiere un desplazamiento no negativo")
	}

	expression := ftsExpression(query.Text)
	if expression == "" {
		// No searchable words: either the caller wants the catalog (empty text)
		// or sent only punctuation. An empty text browses; punctuation-only
		// text matches nothing instead of dumping the whole catalog on the
		// agent, which would be a false "everything matches" answer.
		if strings.TrimSpace(query.Text) != "" {
			return []dto.CatalogItemRecord{}, nil
		}
		return r.browse(ctx, query)
	}

	rows, err := r.db.QueryContext(ctx, searchCatalogItems, expression, query.Limit, query.Offset)
	if err != nil {
		return nil, fmt.Errorf("repositorio del catálogo: buscar: %w", err)
	}
	defer func() { _ = rows.Close() }()

	items := make([]dto.CatalogItemRecord, 0, query.Limit)
	for rows.Next() {
		record, err := scanCatalogItem(rows)
		if err != nil {
			return nil, fmt.Errorf("repositorio del catálogo: leer resultados: %w", err)
		}
		items = append(items, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("repositorio del catálogo: leer resultados: %w", err)
	}
	return items, nil
}

// browse lists active items by name.
func (r *CatalogRepo) browse(ctx context.Context, query dto.SearchCatalogQuery) ([]dto.CatalogItemRecord, error) {
	rows, err := r.db.QueryContext(ctx, browseCatalogItems, query.Limit, query.Offset)
	if err != nil {
		return nil, fmt.Errorf("repositorio del catálogo: listar: %w", err)
	}
	defer func() { _ = rows.Close() }()

	items := make([]dto.CatalogItemRecord, 0, query.Limit)
	for rows.Next() {
		record, err := scanCatalogItem(rows)
		if err != nil {
			return nil, fmt.Errorf("repositorio del catálogo: leer resultados: %w", err)
		}
		items = append(items, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("repositorio del catálogo: leer resultados: %w", err)
	}
	return items, nil
}

// ftsExpression turns free text into a safe FTS5 expression: every word becomes
// a quoted prefix term, and the terms are OR-combined so one strong word still
// finds the item.
//
// Quoting each term is what makes the input safe: inside double quotes FTS5
// reads everything literally, so a caller cannot inject an operator, a column
// filter or an unbalanced quote. Words are cut on anything that is not a letter
// or a digit, which drops punctuation and emoji (kept out of the query, not
// escaped into it).
//
// The result is empty when the text carries no usable word; the caller decides
// what that means.
func ftsExpression(text string) string {
	terms := make([]string, 0, maxFTSTokens)
	seen := make(map[string]struct{}, maxFTSTokens)

	for _, word := range strings.FieldsFunc(text, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		word = strings.ToLower(word)
		if len([]rune(word)) > maxFTSTokenLen {
			word = string([]rune(word)[:maxFTSTokenLen])
		}
		if _, duplicate := seen[word]; duplicate {
			continue
		}
		seen[word] = struct{}{}
		terms = append(terms, `"`+word+`"*`)
		if len(terms) == maxFTSTokens {
			break
		}
	}

	return strings.Join(terms, " OR ")
}

// scanCatalogItem fills the carrier from one row. The nullable columns become
// empty text or nil, so no consumer ever sees a sql.Null* type.
func scanCatalogItem(row scanner) (dto.CatalogItemRecord, error) {
	var (
		record      dto.CatalogItemRecord
		kind        string
		useCases    sql.NullString
		limitations sql.NullString
		priceCents  sql.NullInt64
		salesURL    sql.NullString
		durationMin sql.NullInt64
	)

	if err := row.Scan(
		&record.ID,
		&kind,
		&record.Name,
		&record.Description,
		&useCases,
		&limitations,
		&priceCents,
		&salesURL,
		&durationMin,
	); err != nil {
		return dto.CatalogItemRecord{}, err
	}

	record.Kind = dto.CatalogKind(kind)
	record.UseCases = useCases.String
	record.Limitations = limitations.String
	record.SalesURL = salesURL.String
	if priceCents.Valid {
		price := priceCents.Int64
		record.PriceCents = &price
	}
	if durationMin.Valid {
		duration := int(durationMin.Int64)
		record.DurationMin = &duration
	}
	return record, nil
}

// scanner is the minimal surface shared by *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

// nullableText maps an empty optional text to SQL NULL: an empty string and NULL
// mean the same thing in this catalog ("nothing stated"), and storing one of them
// consistently keeps the FTS index and the readers in agreement.
func nullableText(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

// nullableCents maps an absent price to SQL NULL ("a consultar").
func nullableCents(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

// nullableMinutes maps an absent duration to SQL NULL.
func nullableMinutes(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}
