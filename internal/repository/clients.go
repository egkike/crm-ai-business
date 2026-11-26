package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/egkike/crm-ai-business/internal/application/dto"
)

// This file is the persistence adapter of the anonymous registration seam
// (RF3): the identifier pre-checks and the insert of a new clients row. It
// decides nothing — it reports the row it found, the conflict it hit, or the
// failure of the database.

// The existence and insert statements. Each one is a constant, parameterized
// with '?', and matched exclusively on natural identifiers.
//
// The client queries are the very constants internal/repository/callerlookup.go
// already uses for §3.8 resolution: the registration pre-check and the resolver
// must not be able to disagree about who a caller-id is, and sharing the
// predicate is how that stays true when either side changes.
const (
	queryAccountExistsByPhone = `SELECT EXISTS (SELECT 1 FROM accounts WHERE phone = ?)`
	queryAccountExistsByEmail = `SELECT EXISTS (SELECT 1 FROM accounts WHERE lower(email) = lower(?))`

	insertClient = `INSERT INTO clients (id, phone, email, display_name) VALUES (?, ?, ?, ?)`
)

// SQLite result codes the insert classifier needs. modernc.org/sqlite reports
// extended result codes, so a UNIQUE violation is 2067 (SQLITE_CONSTRAINT |
// (8 << 8)) and a primary-key violation is 1555 (SQLITE_CONSTRAINT | (6 << 8)).
//
// They are matched structurally (see sqliteCoder) instead of through an import
// of the driver: this adapter must not depend on which SQLite binding the
// composition root opened, and the message fallback below covers a driver that
// reports only the primary code.
const (
	sqliteConstraintUnique     = 2067
	sqliteConstraintPrimaryKey = 1555

	// sqliteUniqueMessage is the stable fragment every SQLite build uses for a
	// UNIQUE violation. The message is only consulted to classify an error, and
	// the classified error never reaches a caller (AGENTS.md boundary 2).
	sqliteUniqueMessage = "UNIQUE constraint failed"
)

// ClientsRepo is the SQLite adapter of the registration seam: it answers the
// identifier existence checks and inserts the new clients row.
//
// It implements usecase.ClientStore and usecase.AccountIdentifierChecker; the
// interface declarations stay with the consumer (internal/application/usecase),
// and the compilation of the composition root is what proves the port is
// satisfied.
type ClientsRepo struct {
	db *sql.DB
}

// NewClientsRepo wires the adapter to its database handle. A nil handle is a
// fail-closed configuration error, for the same reason NewCallerLookup refuses
// one: an adapter without a database can only fail, so it refuses to exist.
func NewClientsRepo(handle *sql.DB) (*ClientsRepo, error) {
	if handle == nil {
		return nil, errors.New("repositorio de clientes: se requiere una conexión a la base")
	}
	return &ClientsRepo{db: handle}, nil
}

// AccountIdentifierExists implements usecase.AccountIdentifierChecker: it
// reports whether an accounts row carries the identifier on the given channel,
// whether that account is active or not.
//
// is_active is deliberately absent from the predicate: an inactive account still
// owns its identifier, and a client registered on top of it would forge an
// identity the operator believes to be parked (PRD §3.8, "sin auto-registro de
// internos").
func (r *ClientsRepo) AccountIdentifierExists(ctx context.Context, channel dto.Channel, identifier string) (bool, error) {
	query, err := accountExistsQuery(channel)
	if err != nil {
		return false, err
	}
	if identifier == "" {
		return false, errors.New("repositorio de clientes: se requiere un identificador para verificar la cuenta")
	}

	var exists bool
	if err := r.db.QueryRowContext(ctx, query, identifier).Scan(&exists); err != nil {
		return false, fmt.Errorf("repositorio de clientes: verificar la cuenta: %w", err)
	}
	return exists, nil
}

// ClientByIdentifier implements usecase.ClientStore: it returns the single
// clients row that carries the identifier on the given channel.
//
// Phone matching is byte-exact (the column's BINARY collation) and e-mail
// matching folds with SQLite's lower(), which is the same expression the UNIQUE
// index enforces — the same predicates internal/repository/callerlookup.go uses,
// so "resolves as this client" and "is this client's identifier" cannot diverge.
//
// Two matching rows are impossible under the UNIQUE indexes; detecting them
// anyway keeps a corrupted or hand-edited file from being read as whichever row
// SQLite returned first (PRD §3.7: "0 o 1, nunca ambiguo").
func (r *ClientsRepo) ClientByIdentifier(ctx context.Context, channel dto.Channel, identifier string) (dto.ClientRecord, bool, error) {
	query, err := clientByIdentifierQuery(channel)
	if err != nil {
		return dto.ClientRecord{}, false, err
	}
	if identifier == "" {
		return dto.ClientRecord{}, false, errors.New("repositorio de clientes: se requiere un identificador para buscar el cliente")
	}

	rows, err := r.db.QueryContext(ctx, query, identifier)
	if err != nil {
		return dto.ClientRecord{}, false, fmt.Errorf("repositorio de clientes: consultar clients: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var found []dto.ClientRecord
	for rows.Next() {
		var record dto.ClientRecord
		if err := rows.Scan(&record.ID, &record.DisplayName); err != nil {
			return dto.ClientRecord{}, false, fmt.Errorf("repositorio de clientes: leer clients: %w", err)
		}
		found = append(found, record)
	}
	if err := rows.Err(); err != nil {
		return dto.ClientRecord{}, false, fmt.Errorf("repositorio de clientes: leer clients: %w", err)
	}

	switch len(found) {
	case 0:
		return dto.ClientRecord{}, false, nil
	case 1:
		return found[0], true, nil
	default:
		return dto.ClientRecord{}, false, fmt.Errorf("clientes ambiguos: %d filas de clients para un identificador natural (se espera 0 o 1)", len(found))
	}
}

// CreateClient implements usecase.ClientStore. The row fills exactly one natural
// identifier column — the channel that admitted the client — and leaves the
// other NULL (PRD RF3: WhatsApp-first creates with phone, e-mail-first creates
// with email and phone NULL).
//
// A UNIQUE violation on a natural identifier is mapped to
// *dto.IdentifierTakenError, which is the application layer's signal to render
// the Spanish rejection and to fire the duplicate hook. Both uniqueness rules
// land there: the column UNIQUE (clients.phone, clients.email) and the
// lower(email) index (clients_email_lower_uniq) report the same result code, so
// the classifier maps by channel rather than by constraint name.
//
// A primary-key violation is NOT an identifier conflict and stays an internal
// error: it means the generated UUID collided, which is a defect of the ID
// generator, not a claim to reject semantically.
func (r *ClientsRepo) CreateClient(ctx context.Context, client dto.NewClient) error {
	if err := validateNewClient(client); err != nil {
		return err
	}

	phone, email := identifierColumns(client)
	if _, err := r.db.ExecContext(ctx, insertClient, client.ID, phone, email, client.DisplayName); err != nil {
		if taken, ok := identifierTaken(client.Channel, err); ok {
			return taken
		}
		return fmt.Errorf("repositorio de clientes: insertar el cliente: %w", err)
	}
	return nil
}

// identifierColumns maps the channel to the column pair of the insert: the
// identifier goes to its column and the other one stays SQL NULL (an empty
// string is not an identifier, so it is never written).
func identifierColumns(client dto.NewClient) (phone, email sql.NullString) {
	identifier := sql.NullString{String: client.Identifier, Valid: true}
	if client.Channel == dto.ChannelEmail {
		return sql.NullString{}, identifier
	}
	return identifier, sql.NullString{}
}

// validateNewClient rejects an incomplete row before it reaches the database.
// These are programming errors (the application layer builds the row), so they
// are internal and stay untranslated — except for the channel, whose validity
// decides which column would be written.
func validateNewClient(client dto.NewClient) error {
	if client.ID == "" {
		return errors.New("repositorio de clientes: el cliente a crear no tiene id interno")
	}
	if client.Identifier == "" {
		return errors.New("repositorio de clientes: el cliente a crear no tiene identificador")
	}
	if !client.Channel.Valid() {
		return fmt.Errorf("repositorio de clientes: canal desconocido %q", string(client.Channel))
	}
	return nil
}

// identifierTaken classifies a failed insert: it returns the typed conflict when
// a natural identifier is already taken, and ok=false when the failure is
// something else (which the caller must keep as an internal error).
func identifierTaken(channel dto.Channel, err error) (*dto.IdentifierTakenError, bool) {
	if !isUniqueViolation(err) {
		return nil, false
	}
	return &dto.IdentifierTakenError{Channel: channel, Constraint: constraintOf(channel)}, true
}

// sqliteCoder is the minimal surface of a driver error this adapter needs: the
// SQLite result code. Matching it structurally keeps the adapter free of a
// driver import while still classifying by code when the driver reports one.
type sqliteCoder interface {
	Code() int
}

// isUniqueViolation reports whether err is a UNIQUE constraint violation of a
// natural identifier, which is the only conflict the seam translates.
func isUniqueViolation(err error) bool {
	var coder sqliteCoder
	if errors.As(err, &coder) {
		switch coder.Code() {
		case sqliteConstraintUnique:
			return true
		case sqliteConstraintPrimaryKey:
			// The generated UUID collided: never an identifier claim.
			return false
		}
	}
	if !strings.Contains(err.Error(), sqliteUniqueMessage) {
		return false
	}
	// A driver that reports only the primary code still names the violated
	// column in its message; the internal id is the one conflict that is not an
	// identifier claim.
	return !strings.Contains(err.Error(), "clients.id")
}

// constraintOf names the schema constraint a channel's uniqueness is enforced
// by. It is diagnostics for the internal error chain and never reaches a client.
func constraintOf(channel dto.Channel) string {
	if channel == dto.ChannelEmail {
		return "clients_email_lower_uniq"
	}
	return "clients.phone"
}

// accountExistsQuery returns the existence statement of a channel, or an error
// for a channel the product does not know (fail-closed).
func accountExistsQuery(channel dto.Channel) (string, error) {
	switch channel {
	case dto.ChannelPhone:
		return queryAccountExistsByPhone, nil
	case dto.ChannelEmail:
		return queryAccountExistsByEmail, nil
	default:
		return "", fmt.Errorf("repositorio de clientes: canal desconocido %q", string(channel))
	}
}

// clientByIdentifierQuery returns the lookup statement of a channel, sharing the
// predicates of the §3.8 resolver (queryClientByPhone / queryClientByEmail).
func clientByIdentifierQuery(channel dto.Channel) (string, error) {
	switch channel {
	case dto.ChannelPhone:
		return queryClientByPhone, nil
	case dto.ChannelEmail:
		return queryClientByEmail, nil
	default:
		return "", fmt.Errorf("repositorio de clientes: canal desconocido %q", string(channel))
	}
}
