package db

import (
	"database/sql"
	"testing"
)

// This file pins migration 3 (T7, RF4): the FTS5 search index over the catalog
// and the triggers that keep it in sync with catalog_items. The index is the
// reason the product can answer "¿tenés corte de pelo?" from the description of
// the problem instead of from a name match, so the tests assert what a search
// can find — not how the virtual table is stored.

// insertCatalogItem inserts one item with the v1.11 fields and returns its
// internal id. Values are bound, never interpolated (the repository owns the
// product queries; this is fixture SQL).
func insertCatalogItem(t *testing.T, handle *sql.DB, id, kind, name, description string, useCases, limitations any) {
	t.Helper()
	if _, err := handle.Exec(
		`INSERT INTO catalog_items (id, kind, name, description, use_cases, limitations) VALUES (?, ?, ?, ?, ?, ?)`,
		id, kind, name, description, useCases, limitations,
	); err != nil {
		t.Fatalf("insertar el ítem %q: %v", name, err)
	}
}

// ftsMatches returns the names that match the FTS expression.
func ftsMatches(t *testing.T, handle *sql.DB, expression string) []string {
	t.Helper()

	rows, err := handle.Query(
		`SELECT c.name FROM catalog_items_fts JOIN catalog_items c ON c.rowid = catalog_items_fts.rowid
		 WHERE catalog_items_fts MATCH ? ORDER BY c.name`, expression,
	)
	if err != nil {
		t.Fatalf("buscar %q en el índice FTS: %v", expression, err)
	}
	defer func() { _ = rows.Close() }()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("leer el resultado de la búsqueda: %v", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("leer el resultado de la búsqueda: %v", err)
	}
	return names
}

func containsName(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}

// TestCatalogFTSMigrationIsApplied pins that step 3 landed: the virtual table
// and its three sync triggers exist after Open, and no catalog data was seeded
// (the Owner creates it through the RF4 tools).
func TestCatalogFTSMigrationIsApplied(t *testing.T) {
	handle := openTempDB(t)

	for _, virtualTable := range []string{"catalog_items_fts"} {
		if got := queryValue(t, handle,
			`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, virtualTable); got != "1" {
			t.Errorf("falta la tabla FTS %q de la migración 3", virtualTable)
		}
	}
	for _, trigger := range []string{"catalog_items_fts_ai", "catalog_items_fts_au", "catalog_items_fts_ad"} {
		if got := queryValue(t, handle,
			`SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger' AND name = ?`, trigger); got != "1" {
			t.Errorf("falta el trigger %q que mantiene el índice en sincronía", trigger)
		}
	}
	if got := queryValue(t, handle, `SELECT COUNT(*) FROM catalog_items`); got != "0" {
		t.Fatalf("ítems de catálogo sembrados = %s, se esperaba 0 (los crea el Owner)", got)
	}
	if got := queryValue(t, handle, `SELECT COUNT(*) FROM schema_migrations WHERE version = 3`); got != "1" {
		t.Fatalf("la migración 3 no quedó registrada")
	}
}

// TestCatalogFTSIndexesTheAgentKnowledge pins the v1.11 win: a query that only
// matches the use-case or limitation text finds the item, so the match can be
// born from the problem the client describes.
func TestCatalogFTSIndexesTheAgentKnowledge(t *testing.T) {
	handle := openTempDB(t)

	insertCatalogItem(t, handle, uuidLike(100), kindServicio, "Consulta kinesiológica",
		"Evaluación y tratamiento en consultorio",
		"Dolor lumbar y cervical", "No cubre cirugías")
	insertCatalogItem(t, handle, uuidLike(101), kindProducto, "Crema regeneradora",
		"Pote de 100 g", "Piel seca", "No cubre psoriasis")

	cases := []struct {
		name       string
		expression string
		want       string
	}{
		{name: "por nombre", expression: "kinesiologica", want: "Consulta kinesiológica"},
		{name: "por descripción", expression: "consultorio", want: "Consulta kinesiológica"},
		{name: "por caso de uso (el problema del cliente)", expression: "lumbar", want: "Consulta kinesiológica"},
		{name: "por limitación", expression: "psoriasis", want: "Crema regeneradora"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			names := ftsMatches(t, handle, tc.expression)
			if !containsName(names, tc.want) {
				t.Fatalf("MATCH %q = %v, se esperaba encontrar %q", tc.expression, names, tc.want)
			}
		})
	}
}

// TestCatalogFTSNoDiacritics pins the tokenizer choice documented next to the
// DDL: Spanish is written with and without accents, and both spellings must hit
// the same item.
func TestCatalogFTSNoDiacritics(t *testing.T) {
	handle := openTempDB(t)
	insertCatalogItem(t, handle, uuidLike(110), kindServicio, "Corte de pelo", "Corte y peinado", nil, nil)

	for _, expression := range []string{"pelo", "corté", "peinado"} {
		if names := ftsMatches(t, handle, expression); !containsName(names, "Corte de pelo") {
			t.Fatalf("MATCH %q = %v, se esperaba encontrar el corte (remove_diacritics)", expression, names)
		}
	}
}

// TestCatalogFTSTriggersKeepTheIndexInSync pins the synchronization contract of
// the three triggers: insert → searchable, update → reindexed (the old text
// stops matching), delete → gone.
func TestCatalogFTSTriggersKeepTheIndexInSync(t *testing.T) {
	handle := openTempDB(t)
	id := uuidLike(120)

	insertCatalogItem(t, handle, id, kindServicio, "Masaje descontracturante", "Sesión de 45 minutos", "Contracturas", nil)
	if names := ftsMatches(t, handle, "contracturas"); !containsName(names, "Masaje descontracturante") {
		t.Fatalf("el ítem recién insertado no es buscable: %v", names)
	}

	if _, err := handle.Exec(
		`UPDATE catalog_items SET name = ?, description = ?, use_cases = ? WHERE id = ?`,
		"Masaje relajante", "Sesión de 60 minutos", "Estrés", id,
	); err != nil {
		t.Fatalf("actualizar el ítem: %v", err)
	}
	if names := ftsMatches(t, handle, "estres"); !containsName(names, "Masaje relajante") {
		t.Fatalf("el texto nuevo no quedó indexado: %v", names)
	}
	for _, stale := range []string{"contracturas", "descontracturante"} {
		if names := ftsMatches(t, handle, stale); len(names) != 0 {
			t.Fatalf("MATCH %q = %v, el texto viejo debía desaparecer del índice", stale, names)
		}
	}

	if _, err := handle.Exec(`DELETE FROM catalog_items WHERE id = ?`, id); err != nil {
		t.Fatalf("borrar el ítem: %v", err)
	}
	if names := ftsMatches(t, handle, "relajante"); len(names) != 0 {
		t.Fatalf("MATCH %q = %v, el ítem borrado seguía en el índice", "relajante", names)
	}
}

// TestCatalogFTSIgnoresNonIndexedUpdates pins the UPDATE OF list of the trigger:
// flipping is_active or changing the price must not disturb the indexed text
// (no reindex, and the item stays findable).
func TestCatalogFTSIgnoresNonIndexedUpdates(t *testing.T) {
	handle := openTempDB(t)
	id := uuidLike(130)
	insertCatalogItem(t, handle, id, kindServicio, "Manicura", "Esmaltado semipermanente", nil, nil)

	if _, err := handle.Exec(`UPDATE catalog_items SET is_active = 0, price_cents = ? WHERE id = ?`, int64(150000), id); err != nil {
		t.Fatalf("actualizar columnas no indexadas: %v", err)
	}

	if names := ftsMatches(t, handle, "manicura"); !containsName(names, "Manicura") {
		t.Fatalf("MATCH %q = %v, el ítem debía seguir indexado", "manicura", names)
	}
}
