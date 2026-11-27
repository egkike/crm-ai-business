package repository_test

import (
	"context"
	"database/sql"
	"reflect"
	"strings"
	"testing"

	"github.com/egkike/crm-ai-business/internal/db"
	"github.com/egkike/crm-ai-business/internal/domain"
	"github.com/egkike/crm-ai-business/internal/repository"
)

// newAreasOf wires the adapter over handle.
func newAreasOf(t *testing.T, handle *sql.DB) *repository.AreasRepo {
	t.Helper()
	repo, err := repository.NewAreasRepo(handle)
	if err != nil {
		t.Fatalf("NewAreasRepo() error inesperado: %v", err)
	}
	return repo
}

// areaNames renders the names of a list of areas, which is what the ordering
// assertions compare.
func areaNames(areas []domain.Area) []string {
	names := make([]string, 0, len(areas))
	for _, area := range areas {
		names = append(names, area.Name)
	}
	return names
}

// TestNewAreasRepoRejectsNilHandle pins the fail-closed construction rule.
func TestNewAreasRepoRejectsNilHandle(t *testing.T) {
	repo, err := repository.NewAreasRepo(nil)
	if err == nil {
		t.Fatal("NewAreasRepo(nil) debía fallar (fail-closed)")
	}
	if repo != nil {
		t.Fatalf("NewAreasRepo(nil) devolvió un adaptador utilizable: %+v", repo)
	}
}

// TestAreasCRUD pins RF7's create/rename/list through the port: the seeded area
// is there, a new area is created with the id the application layer owns, the
// list is ordered by name, and a rename reports whether it found the row.
func TestAreasCRUD(t *testing.T) {
	handle := openDB(t)
	repo := newAreasOf(t, handle)
	ctx := context.Background()

	t.Run("el área sembrada está y el listado ordena por nombre", func(t *testing.T) {
		areas, err := repo.ListAreas(ctx)
		if err != nil {
			t.Fatalf("ListAreas() error inesperado: %v", err)
		}
		if got, want := areaNames(areas), []string{db.AreaVentasName}; !reflect.DeepEqual(got, want) {
			t.Fatalf("areas = %v, se esperaba %v", got, want)
		}
		if areas[0].ID != db.AreaVentasID {
			t.Fatalf("id del área sembrada = %q, se esperaba %q", areas[0].ID, db.AreaVentasID)
		}
		if areas[0].CreatedAt == "" {
			t.Fatal("el área sembrada no trae created_at")
		}
	})

	t.Run("crear un área la deja listada en orden alfabético", func(t *testing.T) {
		if err := repo.CreateArea(ctx, domain.Area{ID: uuid(500), Name: "soporte"}); err != nil {
			t.Fatalf("CreateArea() error inesperado: %v", err)
		}
		areas, err := repo.ListAreas(ctx)
		if err != nil {
			t.Fatalf("ListAreas() error inesperado: %v", err)
		}
		// Case-insensitive order: 'soporte' antes que 'Ventas'.
		if got, want := areaNames(areas), []string{"soporte", db.AreaVentasName}; !reflect.DeepEqual(got, want) {
			t.Fatalf("areas = %v, se esperaba %v", got, want)
		}
	})

	t.Run("renombrar un área existente lo confirma", func(t *testing.T) {
		found, err := repo.RenameArea(ctx, uuid(500), "Soporte técnico")
		if err != nil {
			t.Fatalf("RenameArea() error inesperado: %v", err)
		}
		if !found {
			t.Fatal("RenameArea() found=false para un área existente")
		}
		if got := queryScalar(t, handle, `SELECT name FROM areas WHERE id = ?`, uuid(500)); got != "Soporte técnico" {
			t.Fatalf("nombre tras renombrar = %q", got)
		}
	})

	t.Run("renombrar un área inexistente no escribe nada", func(t *testing.T) {
		found, err := repo.RenameArea(ctx, uuid(999), "Fantasma")
		if err != nil {
			t.Fatalf("RenameArea() error inesperado: %v", err)
		}
		if found {
			t.Fatal("RenameArea() found=true para un área inexistente")
		}
		if got := queryScalar(t, handle, `SELECT COUNT(*) FROM areas WHERE name = ?`, "Fantasma"); got != "0" {
			t.Fatalf("se creó el área de un renombrado fallido (count=%s)", got)
		}
	})

	t.Run("nombre duplicado es rechazado por el esquema", func(t *testing.T) {
		err := repo.CreateArea(ctx, domain.Area{ID: uuid(501), Name: db.AreaVentasName})
		if err == nil {
			t.Fatal("CreateArea() con un nombre repetido debía fallar")
		}
		if !strings.Contains(strings.ToUpper(err.Error()), "UNIQUE") {
			t.Fatalf("err = %v, se esperaba una violación de UNIQUE", err)
		}
	})

	t.Run("nombre vacío es rechazado por el esquema", func(t *testing.T) {
		if err := repo.CreateArea(ctx, domain.Area{ID: uuid(502), Name: ""}); err == nil {
			t.Fatal("CreateArea() con nombre vacío debía fallar")
		}
	})
}

// TestAreaStaffMembership pins the N:M membership of RF7 and the read behind the
// RF8 visibility rule: a staff member of two areas sees both, the assignment is a
// set (assigning twice is a no-op), and an unknown area cannot be assigned.
func TestAreaStaffMembership(t *testing.T) {
	handle := openDB(t)
	repo := newAreasOf(t, handle)
	ctx := context.Background()

	seedAccount(t, handle, account{id: uuid(600), email: "ana@example.com", role: "staff", active: true, name: "Ana"})
	seedAccount(t, handle, account{id: uuid(601), email: "beto@example.com", role: "staff", active: true, name: "Beto"})

	if err := repo.CreateArea(ctx, domain.Area{ID: uuid(610), Name: "Soporte"}); err != nil {
		t.Fatalf("CreateArea() error inesperado: %v", err)
	}

	if err := repo.AssignStaff(ctx, db.AreaVentasID, uuid(600)); err != nil {
		t.Fatalf("AssignStaff() error inesperado: %v", err)
	}
	if err := repo.AssignStaff(ctx, uuid(610), uuid(600)); err != nil {
		t.Fatalf("AssignStaff() error inesperado: %v", err)
	}

	t.Run("asignar dos veces no duplica la membresía", func(t *testing.T) {
		if err := repo.AssignStaff(ctx, uuid(610), uuid(600)); err != nil {
			t.Fatalf("AssignStaff() repetido error inesperado: %v", err)
		}
		if got := queryScalar(t, handle, `SELECT COUNT(*) FROM areas_accounts WHERE account_id = ?`, uuid(600)); got != "2" {
			t.Fatalf("membresías = %s, se esperaban 2", got)
		}
	})

	t.Run("staff en dos áreas ve las dos, ordenadas por nombre", func(t *testing.T) {
		areas, err := repo.ListAreasByAccount(ctx, uuid(600))
		if err != nil {
			t.Fatalf("ListAreasByAccount() error inesperado: %v", err)
		}
		want := []string{"Soporte", db.AreaVentasName}
		if got := areaNames(areas); !reflect.DeepEqual(got, want) {
			t.Fatalf("áreas del staff = %v, se esperaba %v", got, want)
		}
	})

	t.Run("staff sin áreas devuelve una lista vacía", func(t *testing.T) {
		areas, err := repo.ListAreasByAccount(ctx, uuid(601))
		if err != nil {
			t.Fatalf("ListAreasByAccount() error inesperado: %v", err)
		}
		if len(areas) != 0 {
			t.Fatalf("áreas de un staff sin membresías = %v, se esperaba vacío", areaNames(areas))
		}
	})

	t.Run("asignar a un área inexistente falla y no escribe", func(t *testing.T) {
		if err := repo.AssignStaff(ctx, uuid(999), uuid(601)); err == nil {
			t.Fatal("AssignStaff() con un área inexistente debía fallar (FOREIGN KEY)")
		}
		if got := queryScalar(t, handle, `SELECT COUNT(*) FROM areas_accounts WHERE account_id = ?`, uuid(601)); got != "0" {
			t.Fatalf("membresías del staff = %s, se esperaba 0", got)
		}
	})
}
