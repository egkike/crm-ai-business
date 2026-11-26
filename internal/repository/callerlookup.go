// Package repository holds the persistence adapters of the product: the SQLite
// implementations of the domain ports (AGENTS.md layer map). It is the only
// package allowed to speak SQL, and it speaks it exclusively through
// parameterized statements.
//
// The package intentionally exposes carriers, not decisions: an adapter finds
// the row and maps its columns. Every authorization decision (valid role,
// present internal id, enabled account) belongs to internal/auth, which
// re-asserts it on top of whatever the adapter returned.
package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/egkike/crm-ai-business/internal/auth"
)

// The lookup statements. Each one is a constant, parameterized with '?', and
// matched exclusively on natural identifiers — never on a caller-supplied
// role, mode or hint, because there is no parameter that could carry one.
//
// Phone comparisons use the column's default BINARY collation: the caller-id
// must equal the stored value byte for byte (PRD §3.8, "sin normalización").
// E-mail comparisons fold with SQLite's built-in lower(), which is the same
// expression the UNIQUE index enforces, so "matches the index" and "matches at
// resolution" cannot diverge.
const (
	queryAccountByPhone = `SELECT id, display_name, role, is_active FROM accounts WHERE phone = ? LIMIT 2`
	queryAccountByEmail = `SELECT id, display_name, role, is_active FROM accounts WHERE lower(email) = lower(?) LIMIT 2`
	queryClientByPhone  = `SELECT id, display_name FROM clients WHERE phone = ? LIMIT 2`
	queryClientByEmail  = `SELECT id, display_name FROM clients WHERE lower(email) = lower(?) LIMIT 2`
)

// accountQueries and clientQueries are the resolution order inside each table:
// the WhatsApp-style phone match first, the e-mail match second. Both columns
// are UNIQUE, so at most one of them can answer for a given caller-id.
var (
	accountQueries = []string{queryAccountByPhone, queryAccountByEmail}
	clientQueries  = []string{queryClientByPhone, queryClientByEmail}
)

// CallerLookup is the SQLite adapter of auth.CallerLookup: it resolves a
// verbatim caller-id to the row it belongs to (accounts or clients) and maps
// it to an auth.Identity.
//
// Precedence is accounts first, then clients. The internal team's rows are the
// privileged identities and the internal Telegram bot resolves them by the
// signal-verified phone the operator shares; a clients row carrying the same
// natural identifier is the same human seen as a customer — a merge candidate
// (the RF3 'posible-duplicado' alert), not a competing identity. Resolving the
// account first keeps an operator's own toolset reachable; the inverse would
// silently demote an operator to a client, and an internal phone is not a
// self-registrable identifier (PRD §3.8 forbids internal self-registration),
// so the clients row cannot shadow a real account.
type CallerLookup struct {
	db *sql.DB
}

// NewCallerLookup wires the adapter to its database handle. A nil handle is a
// fail-closed configuration error: an identity adapter without a database can
// only reject, so it refuses to exist instead of pretending.
func NewCallerLookup(handle *sql.DB) (*CallerLookup, error) {
	if handle == nil {
		return nil, errors.New("repositorio de identidad: se requiere una conexión a la base")
	}
	return &CallerLookup{db: handle}, nil
}

// LookupCaller implements auth.CallerLookup.
//
// It returns auth.ErrCallerNotFound when neither table has the row, a wrapped
// internal error for any database failure, ambiguity or corrupt role, and the
// mapped identity otherwise. The adapter never decides whether the identity may
// act: CallerResolver owns that chain.
func (l *CallerLookup) LookupCaller(ctx context.Context, callerID string) (auth.Identity, error) {
	// Defense in depth: the resolver rejects an empty caller-id before it ever
	// queries, and an empty string must never match a malformed row that stored
	// '' instead of NULL.
	if callerID == "" {
		return auth.Identity{}, auth.ErrCallerNotFound
	}

	account, found, err := l.lookupAccount(ctx, callerID)
	if err != nil {
		return auth.Identity{}, err
	}
	if found {
		return accountIdentity(account)
	}

	client, found, err := l.lookupClient(ctx, callerID)
	if err != nil {
		return auth.Identity{}, err
	}
	if found {
		// A clients row has no is_active flag: per the port's contract it is
		// reported as enabled, and the only role it can carry is client.
		return auth.Identity{ID: client.id, Name: client.name, Role: auth.RoleClient, Enabled: true}, nil
	}

	return auth.Identity{}, auth.ErrCallerNotFound
}

// accountRow is the column carrier of an accounts match.
type accountRow struct {
	id     string
	name   string
	role   string
	active int64
}

// clientRow is the column carrier of a clients match.
type clientRow struct {
	id   string
	name string
}

// lookupAccount tries each account query in order and reports whether one
// matched.
func (l *CallerLookup) lookupAccount(ctx context.Context, callerID string) (accountRow, bool, error) {
	for _, query := range accountQueries {
		row, err := l.findAccount(ctx, query, callerID)
		switch {
		case err == nil:
			return row, true, nil
		case errors.Is(err, auth.ErrCallerNotFound):
			continue
		default:
			return accountRow{}, false, err
		}
	}
	return accountRow{}, false, nil
}

// lookupClient tries each client query in order and reports whether one
// matched.
func (l *CallerLookup) lookupClient(ctx context.Context, callerID string) (clientRow, bool, error) {
	for _, query := range clientQueries {
		row, err := l.findClient(ctx, query, callerID)
		switch {
		case err == nil:
			return row, true, nil
		case errors.Is(err, auth.ErrCallerNotFound):
			continue
		default:
			return clientRow{}, false, err
		}
	}
	return clientRow{}, false, nil
}

// findAccount runs one account query and returns the single matching row, or
// auth.ErrCallerNotFound. Two rows are impossible under the UNIQUE indexes;
// detecting them anyway keeps a corrupted or hand-edited file from being
// resolved to whichever row SQLite happened to return first (PRD §3.7: "0 o 1,
// nunca ambiguo").
func (l *CallerLookup) findAccount(ctx context.Context, query, callerID string) (accountRow, error) {
	rows, err := l.db.QueryContext(ctx, query, callerID)
	if err != nil {
		return accountRow{}, fmt.Errorf("repositorio de identidad: consultar accounts: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var found []accountRow
	for rows.Next() {
		var row accountRow
		if err := rows.Scan(&row.id, &row.name, &row.role, &row.active); err != nil {
			return accountRow{}, fmt.Errorf("repositorio de identidad: leer accounts: %w", err)
		}
		found = append(found, row)
	}
	if err := rows.Err(); err != nil {
		return accountRow{}, fmt.Errorf("repositorio de identidad: leer accounts: %w", err)
	}

	switch len(found) {
	case 0:
		return accountRow{}, auth.ErrCallerNotFound
	case 1:
		return found[0], nil
	default:
		return accountRow{}, fmt.Errorf("identidad ambigua: %d filas de accounts para un identificador natural (se espera 0 o 1)", len(found))
	}
}

// findClient is findAccount for the clients table.
func (l *CallerLookup) findClient(ctx context.Context, query, callerID string) (clientRow, error) {
	rows, err := l.db.QueryContext(ctx, query, callerID)
	if err != nil {
		return clientRow{}, fmt.Errorf("repositorio de identidad: consultar clients: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var found []clientRow
	for rows.Next() {
		var row clientRow
		if err := rows.Scan(&row.id, &row.name); err != nil {
			return clientRow{}, fmt.Errorf("repositorio de identidad: leer clients: %w", err)
		}
		found = append(found, row)
	}
	if err := rows.Err(); err != nil {
		return clientRow{}, fmt.Errorf("repositorio de identidad: leer clients: %w", err)
	}

	switch len(found) {
	case 0:
		return clientRow{}, auth.ErrCallerNotFound
	case 1:
		return found[0], nil
	default:
		return clientRow{}, fmt.Errorf("identidad ambigua: %d filas de clients para un identificador natural (se espera 0 o 1)", len(found))
	}
}

// accountIdentity maps an accounts row to an auth.Identity.
//
// The role is taken from the database verbatim and only accepted if auth.Role
// recognizes it (T3 advisory R1-lookup: the adapter is the last line of
// defense, so it never invents a role and never defaults one). The CHECK
// constraint of the DDL makes an unknown value unreachable through normal
// writes; a hand-edited file is caught here.
//
// is_active is mapped strictly: only the value 1 means enabled, so any other
// stored value fails closed into a disabled account instead of a grant.
func accountIdentity(row accountRow) (auth.Identity, error) {
	role := auth.Role(row.role)
	if !role.Valid() {
		return auth.Identity{}, fmt.Errorf("repositorio de identidad: la cuenta resuelta tiene un rol desconocido (%q)", row.role)
	}
	return auth.Identity{
		ID:      row.id,
		Name:    row.name,
		Role:    role,
		Enabled: row.active == 1,
	}, nil
}
