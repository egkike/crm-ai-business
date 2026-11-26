// Package usecase holds the application layer of the product: the use cases
// that orchestrate the driven ports, re-assert authorization and turn
// persistence outcomes into the Spanish semantic errors the caller reads
// (AGENTS.md layer map). It imports inward only: internal/application/dto and
// the shared edges (internal/auth); it never speaks SQL and never touches
// net/http.
package usecase

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/egkike/crm-ai-business/internal/application/dto"
	"github.com/egkike/crm-ai-business/internal/auth"
)

// AccountIdentifierChecker is the driven port that answers the FIRST collision
// rule of RF3: does this natural identifier already belong to an internal
// account?
//
// It has to answer for active AND inactive accounts. An inactive account still
// owns its phone or e-mail — the row exists, the operator may switch it back on —
// so registering a client on top of it would forge an identity the operator
// believes to be parked. Inactive is not free.
type AccountIdentifierChecker interface {
	// AccountIdentifierExists reports whether an accounts row carries the
	// identifier on the given channel, regardless of is_active.
	AccountIdentifierExists(ctx context.Context, channel dto.Channel, identifier string) (bool, error)
}

// ClientStore is the driven port of client persistence: read one row by its
// natural identifier, and insert a new one.
//
// The port exposes no update and no delete: the registration seam only ever
// admits a row. Claiming an identifier onto an existing row is a different use
// case (F2), which is why it cannot happen here by accident.
type ClientStore interface {
	// ClientByIdentifier returns the single clients row whose natural
	// identifier matches on the given channel (phone byte-exact, e-mail
	// case-insensitive — the same predicates the §3.8 resolver and the UNIQUE
	// indexes use), or found=false.
	ClientByIdentifier(ctx context.Context, channel dto.Channel, identifier string) (dto.ClientRecord, bool, error)

	// CreateClient inserts the row. A natural identifier that another row
	// already owns MUST be reported as *dto.IdentifierTakenError, never as a
	// driver error: the application layer renders the Spanish rejection and the
	// driver text must not reach a client.
	CreateClient(ctx context.Context, client dto.NewClient) error
}

// DuplicateNotifier is the 'posible-duplicado' SEAM of RF3: the hook that will
// write the merge-suggestion alert for the Owner/Admin.
//
// F1 DELIVERS THE SEAM ONLY. The alerts table arrives with F2, so the default is
// a no-op and no alert row is written anywhere yet; F2 wires this hook to the
// alerts table without touching the use case. The type exists now because the
// call sites, not the storage, are what F2 would otherwise have to redesign.
//
// Arguments:
//
//   - identifier: the natural identifier the attempt claimed, verbatim.
//   - claimingClientID: the internal UUID of the client row the claim was
//     attributed to. THIS SEAM IS ANONYMOUS — the sender has no clients row
//     yet (that is why registration runs at all) — so it passes the empty
//     string, and a rejected claim writes nothing that could carry an id. F2's
//     claim path (a resolved client claiming an e-mail that belongs to another
//     client) passes the claimant's real id. The row that already owns the
//     identifier is one lookup away from `identifier` alone, so nothing is lost.
//
// When it fires: a registration attempt whose identifier turns out to be owned
// by a row the attempt cannot adopt (the fail-closed collision path). It does
// NOT fire on the idempotent path — there the identifier resolves to the very
// row the attempt describes, which is the same client, not a duplicate.
type DuplicateNotifier func(ctx context.Context, identifier, claimingClientID string)

const (
	// MaxIdentifierLen bounds the caller-id before it is treated as a natural
	// identifier. 254 is the RFC 5321 maximum for an e-mail address, the widest
	// identifier the schema stores; a phone is far shorter. The check exists
	// because the caller-id is an input (AGENTS.md: validate every tool arg
	// before persistence) and because the limiter keys a map by it.
	MaxIdentifierLen = 254

	// defaultNamePrefix is the product default of display_name: the client is
	// called by the masked tail of the identifier it arrived on, never by the
	// whole value (AGENTS.md PII masking rule).
	defaultNamePrefix = "Cliente "

	// maskedSuffixDigits is how many trailing characters of a phone the default
	// display name keeps — the same last-4-digits rule audit events use.
	maskedSuffixDigits = 4
)

// RegisterClient is the ANONYMOUS registration seam of RF3: the use case that
// admits a client whose caller-id the §3.8 resolver could not resolve.
//
// It is the SOLE documented exception to the double-RBAC contract of AGENTS.md
// (boundary contract 1): there is no role parameter, no permission-map entry and
// no in-use-case auth.RequireRole call, because the sender by definition has no
// row and therefore no role. The compensating controls are this use case's
// whole design: the per-identifier rate limit, the accounts-collision refusal,
// the fail-closed identifier collision (no auto-linking), the PII-masked default
// display name, and the F2 duplicate-alert hook.
//
// A reviewer finding that register_client skips RBAC is reporting an accepted
// design, not a defect.
type RegisterClient struct {
	clients  ClientStore
	accounts AccountIdentifierChecker
	limiter  RegistrationLimiter
	notify   DuplicateNotifier
}

// NewRegisterClient wires the use case to its ports.
//
// A nil port is a fail-closed configuration error: without a client store the
// use case could only fail, and a limiter that does not exist is not "no limit"
// (an absent budget must never read as unlimited). A nil notifier is accepted
// and replaced by a no-op: F1 ships the seam without the alerts table (F2).
func NewRegisterClient(clients ClientStore, accounts AccountIdentifierChecker, limiter RegistrationLimiter, notify DuplicateNotifier) (*RegisterClient, error) {
	if clients == nil {
		return nil, errors.New("registro de clientes: se requiere un ClientStore")
	}
	if accounts == nil {
		return nil, errors.New("registro de clientes: se requiere un AccountIdentifierChecker")
	}
	if limiter == nil {
		return nil, errors.New("registro de clientes: se requiere un RegistrationLimiter")
	}
	if notify == nil {
		notify = func(context.Context, string, string) {}
	}
	return &RegisterClient{clients: clients, accounts: accounts, limiter: limiter, notify: notify}, nil
}

// Execute admits the sender identified by cmd.CallerID as a client.
//
// The chain, in order, fail-closed at every step:
//
//  1. empty caller-id       → auth.ErrMissingCallerID, before the limiter and
//     before any query (the same failure the §3.8 resolver reports)
//  2. identifier too long   → ErrInvalidIdentifier
//  3. rate limit exhausted  → ErrRateLimited (per-identifier window)
//  4. identifier in accounts→ ErrIdentifierInAccount* (active or inactive)
//  5. identifier on a client row → idempotent admission, Created=false
//  6. identifier free       → the row is inserted, Created=true
//
// A UNIQUE collision at step 6 is the fail-closed rejection of RF3
// (ErrIdentifierTaken): the identifier looked free and was taken in between, so
// the attempt is refused, nothing is linked, and the F2 duplicate hook fires.
// It is never retried: RF3's "retry once" belongs to the gateway (the sender
// repeats its original request after a successful register_client), and
// retrying inside the backend would turn a collision race into a spin.
//
// Every returned error is either a semantic one of this package, the shared
// auth.ErrMissingCallerID, or an internal failure the transport must collapse
// into a generic error.
func (uc *RegisterClient) Execute(ctx context.Context, cmd dto.RegisterClientCommand) (dto.RegisterClientResult, error) {
	// 1. No identity at all. The caller-id is used verbatim: no trimming, for
	// the same reason the resolver does not trim — a padded phone is a
	// different phone.
	callerID := cmd.CallerID
	if callerID == "" {
		return dto.RegisterClientResult{}, auth.ErrMissingCallerID
	}
	if len(callerID) > MaxIdentifierLen {
		return dto.RegisterClientResult{}, ErrInvalidIdentifier
	}

	channel := channelOf(callerID)

	// 3. Rate limit before touching the database: it is the cheapest control and
	// the one that has to absorb a runaway loop.
	if !uc.limiter.Allow(callerID) {
		return dto.RegisterClientResult{}, ErrRateLimited
	}

	// 4. An internal account owns this identifier. Checked before the clients
	// row so an operator's own phone can never be shadowed by a client row, and
	// before any insert so nothing is written for a rejected attempt.
	inAccount, err := uc.accounts.AccountIdentifierExists(ctx, channel, callerID)
	if err != nil {
		return dto.RegisterClientResult{}, fmt.Errorf("registro de clientes: verificar la cuenta del identificador: %w", err)
	}
	if inAccount {
		return dto.RegisterClientResult{}, identifierInAccountError(channel)
	}

	// 5. Idempotent admission: the identifier already resolves to a client. The
	// sender is its own row, so nothing is created and nothing is linked; the
	// caller gets the stored identity back (RF3: the gateway's single retry must
	// work once and only once).
	existing, found, err := uc.clients.ClientByIdentifier(ctx, channel, callerID)
	if err != nil {
		return dto.RegisterClientResult{}, fmt.Errorf("registro de clientes: buscar el cliente del identificador: %w", err)
	}
	if found {
		return dto.RegisterClientResult{
			ClientID:    existing.ID,
			DisplayName: existing.DisplayName,
			Created:     false,
		}, nil
	}

	// 6. First admission.
	clientID, err := newClientID()
	if err != nil {
		return dto.RegisterClientResult{}, err
	}
	row := dto.NewClient{
		ID:          clientID,
		Channel:     channel,
		Identifier:  callerID,
		DisplayName: displayNameOrDefault(cmd.DisplayName, channel, callerID),
	}

	if err := uc.clients.CreateClient(ctx, row); err != nil {
		var taken *dto.IdentifierTakenError
		if errors.As(err, &taken) {
			// Fail-closed: the identifier belongs to another client row and RF3
			// forbids auto-linking. The duplicate hook reports the claim so the
			// Owner/Admin can review a merge afterwards (F2).
			uc.notify(ctx, callerID, "")
			return dto.RegisterClientResult{}, ErrIdentifierTaken
		}
		return dto.RegisterClientResult{}, fmt.Errorf("registro de clientes: crear el cliente: %w", err)
	}

	return dto.RegisterClientResult{
		ClientID:    clientID,
		DisplayName: row.DisplayName,
		Created:     true,
	}, nil
}

// channelOf derives the channel from the caller-id shape: an identifier with an
// '@' is an e-mail address, everything else is a phone.
//
// This mirrors how the gateway injects caller-ids (WhatsApp delivers a phone,
// e-mail delivers an address); it is not a validation rule. The database stays
// the authority: a phone-shaped e-mail claim simply lands in the phone column
// and resolves verbatim, which is exactly what the resolver will do with it.
func channelOf(callerID string) dto.Channel {
	if strings.Contains(callerID, "@") {
		return dto.ChannelEmail
	}
	return dto.ChannelPhone
}

// identifierInAccountError picks the phrasing that names the identifier the
// sender actually used. Both variants carry the same code: a transport must
// branch on the code, never on the prose.
func identifierInAccountError(channel dto.Channel) error {
	if channel == dto.ChannelEmail {
		return ErrIdentifierInAccountEmail
	}
	return ErrIdentifierInAccountPhone
}

// displayNameOrDefault returns the name to store: the requested one (trimmed),
// or the product default when the sender offered none.
func displayNameOrDefault(requested string, channel dto.Channel, identifier string) string {
	if name := strings.TrimSpace(requested); name != "" {
		return name
	}
	return defaultNamePrefix + maskedIdentifier(channel, identifier)
}

// maskedIdentifier derives the display tail of an identifier, following the PII
// rule of AGENTS.md ("phone numbers are masked to their last 4 digits in every
// audit event"): a stored name must not become a way to read a full phone or a
// full e-mail address back out of the panel.
func maskedIdentifier(channel dto.Channel, identifier string) string {
	if channel == dto.ChannelEmail {
		return maskedEmail(identifier)
	}
	return maskedSuffix(identifier, maskedSuffixDigits)
}

// maskedEmail keeps the domain (shared, not personal) and at most the first
// character of the local part.
func maskedEmail(identifier string) string {
	at := strings.LastIndex(identifier, "@")
	if at <= 0 || at == len(identifier)-1 {
		return "****"
	}
	local := []rune(identifier[:at])
	domain := identifier[at+1:]
	if len(local) < 2 {
		return "*@" + domain
	}
	return string(local[0]) + "***@" + domain
}

// maskedSuffix keeps the last n runes of the value. A value short enough to be
// fully revealed by that tail is masked entirely instead: the point of the
// default name is that it never discloses what it was derived from.
func maskedSuffix(value string, n int) string {
	runes := []rune(value)
	if len(runes) <= n {
		return "****"
	}
	return "****" + string(runes[len(runes)-n:])
}

// newClientID generates the internal UUID v4 primary key of the clients row.
//
// It delegates to newUUID (ids.go) so every entity of the product derives its
// primary key from one implementation; the name stays because it documents the
// call site.
func newClientID() (string, error) {
	return newUUID()
}
