package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
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

// connectionParams returns the connection contract of AGENTS.md ("Concurrency
// & SQLite") as DSN parameters. It must be applied through the DSN, not with a
// one-off PRAGMA after sql.Open: journal_mode is a property of the file, but
// busy_timeout and foreign_keys are per-connection, and the pool opens new
// connections at will.
//
// The contract is returned as values rather than as a hand-written query string
// so buildDSN can encode and validate it (R3-001): a future parameter joins by
// adding an entry here, which cannot reintroduce a raw interpolation seam.
func connectionParams() url.Values {
	return url.Values{
		"_busy_timeout": {"5000"},
		"_journal_mode": {"WAL"},
		"_foreign_keys": {"ON"},
	}
}

// Open resolves the database path, creates its directory, opens SQLite with
// the pragmas of the connection contract and applies the pending migrations
// before returning.
//
// rawPath is the value of EnvPath (empty means the XDG default). Any
// resolution failure is fail-closed: the caller gets an error instead of a
// handle on a database the operator did not ask for.
//
// A failed startup leaves nothing behind (R3-004): whatever this call created —
// the directory and the database file with its sidecars — is removed again, so a
// rejected configuration or a refused migration does not litter the data
// directory with an empty database the operator would later mistake for state.
// What already existed before the call is NEVER removed: a pre-existing file
// that failed to migrate is the operator's data, and deleting it would turn a
// startup refusal into data loss.
func Open(ctx context.Context, rawPath string) (*sql.DB, error) {
	path, err := ResolvePath(rawPath)
	if err != nil {
		return nil, err
	}

	dir := filepath.Dir(path)
	created := creationOf(dir, path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("crear el directorio de la base %q: %w", dir, err)
	}

	dsn, err := buildDSN(path, connectionParams())
	if err != nil {
		created.rollback()
		return nil, err
	}

	handle, err := sql.Open(driverName, dsn)
	if err != nil {
		created.rollback()
		return nil, fmt.Errorf("abrir la base SQLite %q: %w", path, err)
	}

	if err := handle.PingContext(ctx); err != nil {
		_ = handle.Close()
		created.rollback()
		return nil, fmt.Errorf("abrir la base SQLite %q: %w", path, err)
	}

	if err := migrate(ctx, handle); err != nil {
		_ = handle.Close()
		created.rollback()
		return nil, fmt.Errorf("migrar la base %q: %w", path, err)
	}

	return handle, nil
}

// buildDSN composes the driver DSN from the resolved file path and the
// connection parameters (R3-001: "harden the DSN builder").
//
// The path is validated as a plain file path and the parameters are encoded by
// url.Values, so no raw query text ever reaches the driver: without this, a
// database path containing '?' or '#' would silently become a different DSN
// (the driver splits the query there) and a future parameter appended as string
// concatenation would be an injection seam.
func buildDSN(path string, params url.Values) (string, error) {
	if path == "" {
		return "", errors.New("DSN inválido: la ruta de la base está vacía")
	}
	if strings.ContainsRune(path, 0) {
		return "", errors.New("DSN inválido: la ruta de la base contiene un byte nulo")
	}
	if strings.ContainsAny(path, "?#") {
		return "", fmt.Errorf("DSN inválido: la ruta de la base %q contiene los separadores de query '?' o '#'", path)
	}

	query, err := encodeParams(params)
	if err != nil {
		return "", err
	}
	if query == "" {
		return path, nil
	}
	return path + "?" + query, nil
}

// encodeParams validates and encodes the connection parameters.
//
// The rules are deliberately narrow: driver parameters are prefixed with '_'
// (modernc.org/sqlite's convention) and neither a key nor a value may contain a
// DSN separator. A parameter that does not comply is a programming error that
// refuses to start, because the alternative — letting it through — is a
// connection string whose meaning depends on what the value happens to contain.
func encodeParams(params url.Values) (string, error) {
	for key, values := range params {
		if !strings.HasPrefix(key, "_") || strings.ContainsAny(key, "&=;?# ") {
			return "", fmt.Errorf("DSN inválido: %q no es un parámetro de conexión admitido", key)
		}
		if len(values) == 0 {
			return "", fmt.Errorf("DSN inválido: el parámetro %q no tiene valor", key)
		}
		for _, value := range values {
			if value == "" || strings.ContainsAny(value, "&=;?#") {
				return "", fmt.Errorf("DSN inválido: el valor del parámetro %q contiene separadores o está vacío", key)
			}
		}
	}
	return params.Encode(), nil
}

// creation tracks what a startup created, so a failure can remove exactly that
// and nothing else.
type creation struct {
	dir         string
	file        string
	dirCreated  bool
	fileCreated bool
}

// creationOf records the current existence of the directory and the database
// file BEFORE the call touches them.
func creationOf(dir, path string) creation {
	return creation{
		dir:         dir,
		file:        path,
		dirCreated:  !exists(dir),
		fileCreated: !exists(path),
	}
}

// rollback removes what the startup created. Sidecars (-wal, -shm, -journal)
// are removed first because SQLite may have opened them next to the file, and
// the directory goes last: os.Remove refuses a non-empty directory, so nothing
// that another process or the operator placed there is ever deleted.
func (c creation) rollback() {
	if c.fileCreated {
		for _, candidate := range []string{c.file + "-wal", c.file + "-shm", c.file + "-journal", c.file} {
			_ = os.Remove(candidate)
		}
	}
	if c.dirCreated {
		_ = os.Remove(c.dir)
	}
}

// exists reports whether path is present. A stat error other than "not found"
// is treated as "exists": the safe direction is to never delete a path this call
// is not certain it created.
func exists(path string) bool {
	_, err := os.Stat(path)
	return !errors.Is(err, os.ErrNotExist)
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
