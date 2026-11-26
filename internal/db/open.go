package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite" // pure-Go SQLite driver: no CGo in the product binary
)

const (
	// EnvPath is the environment variable that overrides the database
	// location. The composition root passes it verbatim:
	// db.Open(ctx, os.Getenv(db.EnvPath)).
	EnvPath = "MCP_DB_PATH"

	// envXDGDataHome is the XDG base directory the default path derives from.
	envXDGDataHome = "XDG_DATA_HOME"

	appDirName = "crm-ai-business"
	dbFileName = "crm.db"
	driverName = "sqlite"
)

// dsnParams carries the connection contract of AGENTS.md ("Concurrency &
// SQLite") into every pooled connection. It must be applied through the DSN,
// not with a one-off PRAGMA after sql.Open: journal_mode is a property of the
// file, but busy_timeout and foreign_keys are per-connection, and the pool
// opens new connections at will.
const dsnParams = "?_busy_timeout=5000&_journal_mode=WAL&_foreign_keys=ON"

// Open resolves the database path, creates its directory, opens SQLite with
// the pragmas of the connection contract and applies the pending migrations
// before returning.
//
// rawPath is the value of EnvPath (empty means the XDG default). Any
// resolution failure is fail-closed: the caller gets an error instead of a
// handle on a database the operator did not ask for.
func Open(ctx context.Context, rawPath string) (*sql.DB, error) {
	path, err := ResolvePath(rawPath)
	if err != nil {
		return nil, err
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("crear el directorio de la base %q: %w", dir, err)
	}

	handle, err := sql.Open(driverName, path+dsnParams)
	if err != nil {
		return nil, fmt.Errorf("abrir la base SQLite %q: %w", path, err)
	}

	if err := handle.PingContext(ctx); err != nil {
		_ = handle.Close()
		return nil, fmt.Errorf("abrir la base SQLite %q: %w", path, err)
	}

	if err := migrate(ctx, handle); err != nil {
		_ = handle.Close()
		return nil, fmt.Errorf("migrar la base %q: %w", path, err)
	}

	return handle, nil
}

// ResolvePath validates rawPath and returns the absolute file the database
// lives in. It mirrors the fail-closed rule of the composition root's
// MCP_PORT resolution: empty means "the documented default", anything else
// must be exactly right — the process never guesses.
//
// Accepted: "" (XDG default) or an absolute file path. Rejected: a
// whitespace-only value, a relative path (a daemon has no meaningful working
// directory to resolve against), a path containing '?' or '#' (they are the
// DSN separators the driver splits on, so accepting them would silently point
// at another file), a NUL byte, or an existing directory.
func ResolvePath(raw string) (string, error) {
	if raw == "" {
		return defaultPath()
	}
	if strings.TrimSpace(raw) == "" {
		return "", fmt.Errorf("%s inválido: %q (se espera una ruta absoluta al archivo de la base)", EnvPath, raw)
	}
	if strings.ContainsRune(raw, 0) {
		return "", fmt.Errorf("%s inválido: la ruta contiene un byte nulo", EnvPath)
	}
	if strings.ContainsAny(raw, "?#") {
		return "", fmt.Errorf("%s inválido: %q (la ruta no puede contener '?' ni '#' porque son separadores del DSN)", EnvPath, raw)
	}
	if !filepath.IsAbs(raw) {
		return "", fmt.Errorf("%s inválido: %q (se espera una ruta absoluta)", EnvPath, raw)
	}
	if info, err := os.Stat(raw); err == nil && info.IsDir() {
		return "", fmt.Errorf("%s inválido: %q es un directorio, se espera el archivo de la base", EnvPath, raw)
	}
	return raw, nil
}

// defaultPath is the XDG data location of RF1: $XDG_DATA_HOME/crm-ai-business,
// falling back to ~/.local/share/crm-ai-business. A relative XDG_DATA_HOME is
// ignored, as the XDG base-directory spec requires.
func defaultPath() (string, error) {
	if dir := os.Getenv(envXDGDataHome); dir != "" && filepath.IsAbs(dir) {
		return filepath.Join(dir, appDirName, dbFileName), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("no se pudo determinar el directorio de datos; definí %s: %w", EnvPath, err)
	}
	return filepath.Join(home, ".local", "share", appDirName, dbFileName), nil
}
