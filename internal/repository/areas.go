package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/egkike/crm-ai-business/internal/domain"
)

// This file is the persistence adapter of the configurable areas (RF7): the areas
// and their N:M membership with the internal accounts. It decides nothing — a name
// arrives already validated by the use case, the DDL enforces its bound and its
// uniqueness — and it never deletes: deleting an area is a destructive operation
// with a double gate (RF6), and it lands with the admin tools that own that gate.

// The area statements. Each one is a constant, parameterized with '?', and no
// caller value ever reaches the SQL text.
const (
	insertArea = `INSERT INTO areas (id, name) VALUES (?, ?)`

	// renameArea refreshes updated_at with the name: the panel shows the areas by
	// last edit, and the column is maintained by the writer (no trigger), like
	// every other updated_at of the schema.
	renameArea = `
UPDATE areas
SET name = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
WHERE id = ?`

	// areaColumns is the projection every area read shares, so the entity is filled
	// by one scan helper and cannot drift between queries.
	areaColumns = `id, name, created_at, updated_at`

	// listAreas orders by name case-insensitively: the areas are read as a menu
	// ("¿a qué área lo paso?"), and NOCASE keeps 'soporte' next to 'Ventas' instead
	// of after every capitalised name.
	listAreas = `SELECT ` + areaColumns + ` FROM areas ORDER BY name COLLATE NOCASE, id`

	// listAreasByAccount is the read behind RF8's staff visibility rule: the areas
	// whose unassigned tickets a staff member may see.
	listAreasByAccount = `
SELECT a.id, a.name, a.created_at, a.updated_at
FROM areas a
JOIN areas_accounts aa ON aa.area_id = a.id
WHERE aa.account_id = ?
ORDER BY a.name COLLATE NOCASE, a.id`

	// assignStaff inserts one membership. The conflict clause makes the assignment
	// a set operation: assigning the same pair twice is a no-op instead of the
	// UNIQUE violation a form submitted twice would otherwise produce (RF7's admin
	// flow has no way to know the row already exists).
	assignStaff = `
INSERT INTO areas_accounts (area_id, account_id) VALUES (?, ?)
ON CONFLICT DO NOTHING`
)

// AreasRepo is the SQLite adapter of the areas and their membership (satisfied by
// domain.AreaStore).
type AreasRepo struct {
	db *sql.DB
}

// NewAreasRepo wires the adapter to its database handle. A nil handle is a
// fail-closed configuration error.
func NewAreasRepo(handle *sql.DB) (*AreasRepo, error) {
	if handle == nil {
		return nil, errors.New("repositorio de áreas: se requiere una conexión a la base")
	}
	return &AreasRepo{db: handle}, nil
}

// CreateArea inserts one area. The id comes from the application layer, which owns
// the identity of every business entity (the same convention the ticket creation
// follows).
func (r *AreasRepo) CreateArea(ctx context.Context, area domain.Area) error {
	if area.ID == "" {
		return errors.New("repositorio de áreas: el área a crear no tiene id interno")
	}

	if _, err := r.db.ExecContext(ctx, insertArea, area.ID, area.Name); err != nil {
		return fmt.Errorf("repositorio de áreas: crear el área: %w", err)
	}
	return nil
}

// RenameArea renames the area id and reports whether a row carried that id.
func (r *AreasRepo) RenameArea(ctx context.Context, id, name string) (bool, error) {
	if id == "" {
		return false, errors.New("repositorio de áreas: se requiere el id del área a renombrar")
	}

	result, err := r.db.ExecContext(ctx, renameArea, name, id)
	if err != nil {
		return false, fmt.Errorf("repositorio de áreas: renombrar el área: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("repositorio de áreas: confirmar el renombrado: %w", err)
	}
	return affected > 0, nil
}

// ListAreas returns every area, ordered by name.
func (r *AreasRepo) ListAreas(ctx context.Context) ([]domain.Area, error) {
	rows, err := r.db.QueryContext(ctx, listAreas)
	if err != nil {
		return nil, fmt.Errorf("repositorio de áreas: listar: %w", err)
	}
	defer func() { _ = rows.Close() }()

	return scanAreas(rows)
}

// AssignStaff makes accountID a member of areaID. It is idempotent: the pair is
// the primary key, so a repeated assignment writes nothing and is not an error.
//
// An unknown area or account is a foreign-key violation, surfaced as an error: the
// caller named a row that does not exist, and silently ignoring it would report a
// membership that was never stored.
func (r *AreasRepo) AssignStaff(ctx context.Context, areaID, accountID string) error {
	if areaID == "" || accountID == "" {
		return errors.New("repositorio de áreas: la asignación requiere área y cuenta")
	}

	if _, err := r.db.ExecContext(ctx, assignStaff, areaID, accountID); err != nil {
		return fmt.Errorf("repositorio de áreas: asignar el staff: %w", err)
	}
	return nil
}

// ListAreasByAccount returns the areas accountID belongs to, ordered by name.
func (r *AreasRepo) ListAreasByAccount(ctx context.Context, accountID string) ([]domain.Area, error) {
	if accountID == "" {
		return nil, errors.New("repositorio de áreas: se requiere el id de la cuenta")
	}

	rows, err := r.db.QueryContext(ctx, listAreasByAccount, accountID)
	if err != nil {
		return nil, fmt.Errorf("repositorio de áreas: listar las del staff: %w", err)
	}
	defer func() { _ = rows.Close() }()

	return scanAreas(rows)
}

// scanAreas fills the entity from a result set of areaColumns.
func scanAreas(rows *sql.Rows) ([]domain.Area, error) {
	areas := make([]domain.Area, 0, 8)
	for rows.Next() {
		var area domain.Area
		if err := rows.Scan(&area.ID, &area.Name, &area.CreatedAt, &area.UpdatedAt); err != nil {
			return nil, fmt.Errorf("repositorio de áreas: leer un área: %w", err)
		}
		areas = append(areas, area)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("repositorio de áreas: leer un área: %w", err)
	}
	return areas, nil
}
