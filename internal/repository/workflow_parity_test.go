package repository_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/egkike/crm-ai-business/internal/db"
	"github.com/egkike/crm-ai-business/internal/domain"
)

// This file closes rider R3-002 (review facf75c0a9cacc06): the value mapping that
// turns a domain config into columns exists TWICE — internal/db builds the seed
// statements of migration 5 and internal/repository saves a config through
// SaveConfig — and the two must agree, or a saved workflow would read back
// different from the one the migration shipped for the same declaration.
//
// The check is behavioural instead of a shared helper: internal/db is append-only
// (an applied migration is never rewritten), and its mappers are unexported, so
// the honest comparison is the DATA. Each test reads the rows the seed wrote,
// saves the very same domain config through the repository adapter, and requires
// the stored rows to be identical — nil deadlines, empty area ids and false flags
// included, which are exactly the cases where two mappers drift.

// configRowVector is the stored shape of one config row, as plain values.
type configRowVector map[string]string

// readTypeRows reads the type-level declarations of one ticket type.
func readTypeRows(t *testing.T, handle *sql.DB, typeID string) configRowVector {
	t.Helper()

	rows, err := handle.Query(
		`SELECT COALESCE(default_area_id, '<null>'), COALESCE(initial_state, '<null>')
		 FROM ticket_types WHERE id = ?`, typeID)
	if err != nil {
		t.Fatalf("leer el tipo %s: %v", typeID, err)
	}
	defer func() { _ = rows.Close() }()

	vector := configRowVector{}
	for rows.Next() {
		var area, initial string
		if err := rows.Scan(&area, &initial); err != nil {
			t.Fatalf("leer el tipo %s: %v", typeID, err)
		}
		vector["default_area_id"] = area
		vector["initial_state"] = initial
	}
	return vector
}

// readConfigRows reads the states and transitions of one ticket type as one
// comparable vector: every column, in declaration order.
func readConfigRows(t *testing.T, handle *sql.DB, typeID string) configRowVector {
	t.Helper()

	vector := configRowVector{}

	states, err := handle.Query(
		`SELECT name, position, COALESCE(deadline_hours, '<null>'), on_enter_alert, on_exit_alert
		 FROM ticket_states WHERE ticket_type_id = ? ORDER BY position`, typeID)
	if err != nil {
		t.Fatalf("leer los estados de %s: %v", typeID, err)
	}
	defer func() { _ = states.Close() }()
	for states.Next() {
		var (
			name     string
			position int
			deadline string
			onEnter  int
			onExit   int
		)
		if err := states.Scan(&name, &position, &deadline, &onEnter, &onExit); err != nil {
			t.Fatalf("leer un estado de %s: %v", typeID, err)
		}
		vector["state:"+name] = pair(position, deadline, onEnter, onExit)
	}

	transitions, err := handle.Query(
		`SELECT from_state, to_state, position, min_role, round_robin
		 FROM ticket_transitions WHERE ticket_type_id = ? ORDER BY position`, typeID)
	if err != nil {
		t.Fatalf("leer las transiciones de %s: %v", typeID, err)
	}
	defer func() { _ = transitions.Close() }()
	for transitions.Next() {
		var (
			from, to string
			position int
			minRole  string
			round    int
		)
		if err := transitions.Scan(&from, &to, &position, &minRole, &round); err != nil {
			t.Fatalf("leer una transición de %s: %v", typeID, err)
		}
		vector["transition:"+from+"->"+to] = pair(position, minRole, round)
	}

	return vector
}

// pair renders a row vector cell so the comparison is readable in a failure.
func pair(values ...any) string {
	rendered := ""
	for index, value := range values {
		if index > 0 {
			rendered += "|"
		}
		switch typed := value.(type) {
		case string:
			rendered += typed
		case int:
			rendered += itoa(typed)
		}
	}
	return rendered
}

// itoa is a tiny int formatter, kept local so the parity test does not pull
// strconv into a file that reads better without it.
func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}

// assertSameVector pins that the save wrote exactly what the seed had written.
func assertSameVector(t *testing.T, label string, before, after configRowVector) {
	t.Helper()

	for key, want := range before {
		got, present := after[key]
		if !present {
			t.Fatalf("%s: la fila %q del seed desapareció tras SaveConfig", label, key)
		}
		if got != want {
			t.Fatalf("%s: la fila %q quedó %q tras SaveConfig, el seed tenía %q", label, key, got, want)
		}
	}
	if len(after) != len(before) {
		t.Fatalf("%s: filas = %d tras SaveConfig, el seed tenía %d", label, len(after), len(before))
	}
}

// TestSaveConfigReproducesTheSeededRows is the R3-002 cross-check: the value
// mapping of the repository save agrees with the one the seeding migration uses,
// because saving the same domain config reproduces the seeded rows exactly.
func TestSaveConfigReproducesTheSeededRows(t *testing.T) {
	handle := openDB(t)
	repo := newWorkflowConfigOf(t, handle)
	ctx := context.Background()

	cases := []struct {
		label    string
		typeID   string
		typeName string
		seedArea string
	}{
		{
			label:    "venta (con área por defecto y round-robin)",
			typeID:   db.TicketTypeVentaID,
			typeName: db.TicketTypeVentaName,
			seedArea: db.AreaVentasID,
		},
		{
			label:    "consulta (sin área fija)",
			typeID:   db.TicketTypeConsultaID,
			typeName: db.TicketTypeConsultaName,
			seedArea: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			seeded, found, err := repo.LoadConfig(ctx, tc.typeName)
			if err != nil || !found {
				t.Fatalf("LoadConfig(%q) = %t, %v", tc.typeName, found, err)
			}

			beforeType := readTypeRows(t, handle, tc.typeID)
			beforeConfig := readConfigRows(t, handle, tc.typeID)

			if got := beforeType["default_area_id"]; tc.seedArea == "" && got != "<null>" {
				t.Fatalf("default_area_id del seed = %q, se esperaba NULL", got)
			}
			if tc.seedArea != "" && beforeType["default_area_id"] != tc.seedArea {
				t.Fatalf("default_area_id del seed = %q, se esperaba %q", beforeType["default_area_id"], tc.seedArea)
			}

			// The save writes the SAME declaration through the repository's own
			// mappers (nullText/optionalInt/boolFlag) instead of the seed's
			// (nullableText/optionalHours/flagInt).
			if saved, err := repo.SaveConfig(ctx, seeded); err != nil || !saved {
				t.Fatalf("SaveConfig(%q) = %t, %v", tc.typeName, saved, err)
			}

			assertSameVector(t, "tipo "+tc.typeName, beforeType, readTypeRows(t, handle, tc.typeID))
			assertSameVector(t, "workflow "+tc.typeName, beforeConfig, readConfigRows(t, handle, tc.typeID))
		})
	}
}

// TestSaveConfigStoresOptionalValuesLikeTheSeed pins the three cells where the two
// mappers could actually disagree: an optional deadline (NULL vs 0), the two
// declarative flags (0/1) and the empty default area (NULL vs ""). They are
// asserted on the raw columns, which is the contract the seed and the save share.
func TestSaveConfigStoresOptionalValuesLikeTheSeed(t *testing.T) {
	handle := openDB(t)
	repo := newWorkflowConfigOf(t, handle)
	ctx := context.Background()

	hours := 48
	config := domain.WorkflowConfig{
		TypeName:     db.TicketTypeConsultaName,
		InitialState: "nuevo",
		States: []domain.StateConfig{
			{Name: "nuevo", OnEnterAlert: true},
			{Name: "en-espera", DeadlineHours: &hours, OnExitAlert: true},
			{Name: "resuelto"},
		},
		Transitions: []domain.TransitionConfig{
			{From: "nuevo", To: "en-espera", MinRole: domain.TransitionRoleStaff},
			{From: "en-espera", To: "resuelto", MinRole: domain.TransitionRoleAdmin, RoundRobin: true},
		},
	}
	if err := config.Validate(); err != nil {
		t.Fatalf("la config de prueba no valida: %v", err)
	}
	if saved, err := repo.SaveConfig(ctx, config); err != nil || !saved {
		t.Fatalf("SaveConfig() = %t, %v", saved, err)
	}

	rows := readConfigRows(t, handle, db.TicketTypeConsultaID)
	expected := configRowVector{
		"state:nuevo":                    "0|<null>|1|0",
		"state:en-espera":                "1|48|0|1",
		"state:resuelto":                 "2|<null>|0|0",
		"transition:nuevo->en-espera":    "0|staff|0",
		"transition:en-espera->resuelto": "1|admin|1",
	}
	for key, want := range expected {
		if got := rows[key]; got != want {
			t.Fatalf("%q quedó %q, se esperaba %q", key, got, want)
		}
	}

	// And the config reads back as the declaration that was saved: the two
	// mappers agree in both directions.
	loaded, found, err := repo.LoadConfig(ctx, db.TicketTypeConsultaName)
	if err != nil || !found {
		t.Fatalf("LoadConfig() = %t, %v", found, err)
	}
	if loaded.States[1].DeadlineHours == nil || *loaded.States[1].DeadlineHours != 48 {
		t.Fatalf("deadline leído = %+v, se esperaba 48", loaded.States[1].DeadlineHours)
	}
	if !loaded.States[0].OnEnterAlert || loaded.States[0].OnExitAlert {
		t.Fatalf("flags leídos = %+v, se esperaba on-enter=true/on-exit=false", loaded.States[0])
	}
	if !loaded.Transitions[1].RoundRobin || loaded.Transitions[1].MinRole != domain.TransitionRoleAdmin {
		t.Fatalf("transición leída = %+v, se esperaba admin con round-robin", loaded.Transitions[1])
	}
	if loaded.DefaultAreaID != "" {
		t.Fatalf("default_area_id leído = %q, se esperaba vacío (NULL)", loaded.DefaultAreaID)
	}
}
