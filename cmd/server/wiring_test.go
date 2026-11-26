package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/egkike/crm-ai-business/internal/db"
)

// procFDs is the kernel's table of this process's open file descriptors.
//
// It is the honest way (on Linux) to prove a handle was RELEASED: an open SQLite
// file can still be renamed or unlinked, so "the temp file is removable" would pass
// whether or not buildMCPHandler leaked the handle. Reading this table is
// read-only and needs no production hook, which keeps the fix under test exactly
// the code that ships.
const procFDs = "/proc/self/fd"

// TestBuildMCPHandlerClosesTheHandleOnFailure pins the committed-flag defer of
// buildMCPHandler (review R3-dbhandle-close-untested): the SQLite handle stays open
// only when the composition fully succeeds, and every intermediate refusal closes
// it.
//
// The failure is made reachable from outside the function: MCP_REGISTRATION_RATE_LIMIT
// is resolved AFTER the database is open and migrated, so an invalid value refuses
// the start with the handle open — which is exactly the leak the fix closes.
func TestBuildMCPHandlerClosesTheHandleOnFailure(t *testing.T) {
	if _, err := os.ReadDir(procFDs); err != nil {
		t.Skipf("%s no está disponible (%v): la liberación del handle no es observable en este sistema", procFDs, err)
	}

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "wiring.db")

	// Control: the detector must SEE a handle that is open. Without it, the
	// assertion below could pass for the wrong reason (a detector that never
	// matches anything).
	controlPath := filepath.Join(dir, "control.db")
	control, err := db.Open(context.Background(), controlPath)
	if err != nil {
		t.Fatalf("db.Open(%q) error inesperado: %v", controlPath, err)
	}
	assertHandleOpen(t, controlPath, true)
	if err := control.Close(); err != nil {
		t.Fatalf("cerrar el handle de control: %v", err)
	}
	assertHandleOpen(t, controlPath, false)

	t.Setenv(db.EnvPath, dbPath)
	t.Setenv(registrationLimitEnv, "no-numérico")

	if handler, err := buildMCPHandler(); err == nil {
		t.Fatalf("buildMCPHandler() = %v, se esperaba el rechazo de %s inválido", handler, registrationLimitEnv)
	}

	// The composition reached the database, so a handle existed: the file and its
	// migrations are on disk. Without this check the close assertion could pass
	// because the composition failed before opening anything.
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("la base %q no existe: la composición no llegó a abrirla (%v)", dbPath, err)
	}
	assertHandleOpen(t, dbPath, false)
}

// assertHandleOpen pins whether this process holds an open descriptor on path or on
// one of its SQLite sidecars.
func assertHandleOpen(t *testing.T, path string, want bool) {
	t.Helper()

	targets := map[string]bool{path: true, path + "-wal": true, path + "-shm": true}
	entries, err := os.ReadDir(procFDs)
	if err != nil {
		t.Fatalf("leer %s: %v", procFDs, err)
	}
	for _, entry := range entries {
		target, err := os.Readlink(filepath.Join(procFDs, entry.Name()))
		if err != nil {
			// A descriptor that is not a path (a socket, an anon inode) or one closed
			// while the table was read: neither is the handle under test.
			continue
		}
		if !targets[target] {
			continue
		}
		if !want {
			t.Fatalf("el handle de %q sigue abierto: fd %s → %s", path, entry.Name(), target)
		}
		return
	}
	if want {
		t.Fatalf("no se encontró un descriptor abierto de %q: el detector no encuentra handles y no sirve de prueba", path)
	}
}
