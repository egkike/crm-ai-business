package usecase_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/egkike/crm-ai-business/internal/application/dto"
	"github.com/egkike/crm-ai-business/internal/application/usecase"
	"github.com/egkike/crm-ai-business/internal/auth"
)

// fakeClients is the ClientStore double: it records every call and replays the
// configured answer. The use case tests exercise decisions, so the port must be
// answerable — the real SQL statements are pinned by the repository tests.
type fakeClients struct {
	existing  dto.ClientRecord
	found     bool
	lookupErr error
	createErr error

	lookupCalls    int
	createCalls    int
	lastLookupChan dto.Channel
	lastIdentifier string
	created        []dto.NewClient
}

func (f *fakeClients) ClientByIdentifier(_ context.Context, channel dto.Channel, identifier string) (dto.ClientRecord, bool, error) {
	f.lookupCalls++
	f.lastLookupChan = channel
	f.lastIdentifier = identifier
	if f.lookupErr != nil {
		return dto.ClientRecord{}, false, f.lookupErr
	}
	return f.existing, f.found, nil
}

func (f *fakeClients) CreateClient(_ context.Context, client dto.NewClient) error {
	f.createCalls++
	if f.createErr != nil {
		return f.createErr
	}
	f.created = append(f.created, client)
	return nil
}

// fakeAccounts is the AccountIdentifierChecker double.
type fakeAccounts struct {
	taken  bool
	err    error
	calls  int
	lastCh dto.Channel
	lastID string
}

func (f *fakeAccounts) AccountIdentifierExists(_ context.Context, channel dto.Channel, identifier string) (bool, error) {
	f.calls++
	f.lastCh = channel
	f.lastID = identifier
	if f.err != nil {
		return false, f.err
	}
	return f.taken, nil
}

// countingLimiter is the RegistrationLimiter double.
type countingLimiter struct {
	allow bool
	calls int
	last  string
}

func (l *countingLimiter) Allow(identifier string) bool {
	l.calls++
	l.last = identifier
	return l.allow
}

// notifierCall is one recorded invocation of the duplicate seam.
type notifierCall struct {
	identifier       string
	claimingClientID string
}

// recorder is the DuplicateNotifier double.
type recorder struct {
	calls []notifierCall
}

func (r *recorder) notify(_ context.Context, identifier, claimingClientID string) {
	r.calls = append(r.calls, notifierCall{identifier: identifier, claimingClientID: claimingClientID})
}

// newUseCase wires the use case over the three doubles (nil ports are handled by
// the constructor test).
func newUseCase(t *testing.T, clients *fakeClients, accounts *fakeAccounts, limiter usecase.RegistrationLimiter, notify usecase.DuplicateNotifier) *usecase.RegisterClient {
	t.Helper()
	if clients == nil {
		clients = &fakeClients{}
	}
	if accounts == nil {
		accounts = &fakeAccounts{}
	}
	if limiter == nil {
		limiter = &countingLimiter{allow: true}
	}
	uc, err := usecase.NewRegisterClient(clients, accounts, limiter, notify)
	if err != nil {
		t.Fatalf("NewRegisterClient() error inesperado: %v", err)
	}
	return uc
}

// TestNewRegisterClientRejectsNilPorts pins the fail-closed wiring: a missing
// port is not "no limit" and not "nothing to check".
func TestNewRegisterClientRejectsNilPorts(t *testing.T) {
	clients := &fakeClients{}
	accounts := &fakeAccounts{}
	limiter := &countingLimiter{allow: true}

	if uc, err := usecase.NewRegisterClient(nil, accounts, limiter, nil); err == nil || uc != nil {
		t.Fatalf("NewRegisterClient(nil clients) = %+v, %v; se esperaba rechazo", uc, err)
	}
	if uc, err := usecase.NewRegisterClient(clients, nil, limiter, nil); err == nil || uc != nil {
		t.Fatalf("NewRegisterClient(nil accounts) = %+v, %v; se esperaba rechazo", uc, err)
	}
	if uc, err := usecase.NewRegisterClient(clients, accounts, nil, nil); err == nil || uc != nil {
		t.Fatalf("NewRegisterClient(nil limiter) = %+v, %v; se esperaba rechazo", uc, err)
	}

	// A nil notifier is the F1 state (the alerts table lands with F2): it must be
	// replaced by a no-op, not treated as a configuration error.
	if _, err := usecase.NewRegisterClient(clients, accounts, limiter, nil); err != nil {
		t.Fatalf("NewRegisterClient() con notifier nil debía aceptarse: %v", err)
	}
}

// TestExecuteRejectsEmptyCallerIDBeforeAnything pins step 1 of the chain: no
// caller-id is rejected before the limiter and before any query, so a request
// without identity cannot even spend a registration budget (AGENTS.md: "header
// ausente → 401 antes de cualquier handler").
func TestExecuteRejectsEmptyCallerIDBeforeAnything(t *testing.T) {
	clients := &fakeClients{}
	accounts := &fakeAccounts{}
	limiter := &countingLimiter{allow: true}
	notify := &recorder{}
	uc := newUseCase(t, clients, accounts, limiter, notify.notify)

	result, err := uc.Execute(context.Background(), dto.RegisterClientCommand{CallerID: "", DisplayName: "Alguien"})
	if !errors.Is(err, auth.ErrMissingCallerID) {
		t.Fatalf("err = %v, se esperaba auth.ErrMissingCallerID", err)
	}
	if result != (dto.RegisterClientResult{}) {
		t.Fatalf("resultado = %+v, se esperaba el resultado cero", result)
	}
	if limiter.calls != 0 || accounts.calls != 0 || clients.lookupCalls != 0 || clients.createCalls != 0 {
		t.Fatalf("llamadas limiter=%d accounts=%d lookup=%d create=%d, se esperaba 0 en todas (orden de la cadena)",
			limiter.calls, accounts.calls, clients.lookupCalls, clients.createCalls)
	}
	if len(notify.calls) != 0 {
		t.Fatalf("duplicate = %+v, se esperaba ninguna notificación", notify.calls)
	}
}

// TestExecuteRejectsUnstorableIdentifier pins the length guard: a caller-id
// wider than any identifier the schema stores is refused as invalid data, before
// it reaches the limiter's map or the database.
func TestExecuteRejectsUnstorableIdentifier(t *testing.T) {
	limiter := &countingLimiter{allow: true}
	clients := &fakeClients{}
	uc := newUseCase(t, clients, nil, limiter, nil)

	toolong := strings.Repeat("9", usecase.MaxIdentifierLen+1)
	if _, err := uc.Execute(context.Background(), dto.RegisterClientCommand{CallerID: toolong}); !errors.Is(err, usecase.ErrInvalidIdentifier) {
		t.Fatalf("err = %v, se esperaba ErrInvalidIdentifier", err)
	}
	if limiter.calls != 0 || clients.lookupCalls != 0 || clients.createCalls != 0 {
		t.Fatalf("llamadas limiter=%d lookup=%d create=%d, se esperaba 0 (rechazo previo a todo)", limiter.calls, clients.lookupCalls, clients.createCalls)
	}

	// The boundary value is accepted: the guard rejects what cannot be stored,
	// not what is merely long.
	boundary := strings.Repeat("9", usecase.MaxIdentifierLen)
	if _, err := uc.Execute(context.Background(), dto.RegisterClientCommand{CallerID: boundary}); err != nil {
		t.Fatalf("err = %v, el identificador del largo máximo debía admitirse", err)
	}
}

// TestExecuteRateLimit pins step 3 with the product limiter: N attempts admitted,
// N+1 rejected with the semantic retry message, and the rejected attempt writes
// nothing.
func TestExecuteRateLimit(t *testing.T) {
	const limit = 2
	clients := &fakeClients{}
	limiter := usecase.NewMemoryRegistrationLimiter(limit)
	uc := newUseCase(t, clients, nil, limiter, nil)

	ctx := context.Background()
	callerID := "+5491100000001"

	for attempt := 1; attempt <= limit; attempt++ {
		if _, err := uc.Execute(ctx, dto.RegisterClientCommand{CallerID: callerID}); err != nil {
			t.Fatalf("intento %d: err = %v, se esperaba admisión", attempt, err)
		}
	}

	result, err := uc.Execute(ctx, dto.RegisterClientCommand{CallerID: callerID})
	assertSemantic(t, err, usecase.CodeRegistrationRateLimited,
		"se superó el límite de intentos de registro. Volvé a intentarlo más tarde o pedile al negocio que te registre.")
	if result != (dto.RegisterClientResult{}) {
		t.Fatalf("resultado = %+v, se esperaba el resultado cero", result)
	}
	if clients.createCalls != limit {
		t.Fatalf("createCalls = %d, se esperaban %d (el intento rechazado no escribe)", clients.createCalls, limit)
	}

	// The budget is per identifier: another sender is unaffected.
	if _, err := uc.Execute(ctx, dto.RegisterClientCommand{CallerID: "+5491100000002"}); err != nil {
		t.Fatalf("err = %v, otro identificador no debía consumir el presupuesto del primero", err)
	}
}

// TestExecuteRateLimitZeroDisablesRegistration pins the fail-closed reading of an
// absent limit: limit 0 does not mean unlimited.
func TestExecuteRateLimitZeroDisablesRegistration(t *testing.T) {
	for _, limit := range []int{0, -1} {
		clients := &fakeClients{}
		uc := newUseCase(t, clients, nil, usecase.NewMemoryRegistrationLimiter(limit), nil)

		if _, err := uc.Execute(context.Background(), dto.RegisterClientCommand{CallerID: "+5491100000003"}); !errors.Is(err, usecase.ErrRateLimited) {
			t.Fatalf("límite %d: err = %v, se esperaba ErrRateLimited", limit, err)
		}
		if clients.createCalls != 0 {
			t.Fatalf("límite %d: createCalls = %d, se esperaba 0 (registro deshabilitado)", limit, clients.createCalls)
		}
	}
}

// TestExecuteChannelSplit pins step 6's channel rule (RF3): an identifier with an
// '@' is created on the e-mail channel, anything else on the phone channel, and
// the identifier is carried verbatim in both cases.
func TestExecuteChannelSplit(t *testing.T) {
	cases := []struct {
		name        string
		callerID    string
		wantChannel dto.Channel
	}{
		{name: "WhatsApp-first", callerID: "+54 9 11 4444-0700", wantChannel: dto.ChannelPhone},
		{name: "e-mail-first", callerID: "Cliente@Example.COM", wantChannel: dto.ChannelEmail},
		{name: "teléfono sin formato", callerID: "5491144440701", wantChannel: dto.ChannelPhone},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clients := &fakeClients{}
			accounts := &fakeAccounts{}
			uc := newUseCase(t, clients, accounts, nil, nil)

			result, err := uc.Execute(context.Background(), dto.RegisterClientCommand{CallerID: tc.callerID})
			if err != nil {
				t.Fatalf("Execute() error inesperado: %v", err)
			}
			if !result.Created {
				t.Fatal("Created = false, se esperaba la primera admisión")
			}
			if len(clients.created) != 1 {
				t.Fatalf("creados = %d, se esperaba 1", len(clients.created))
			}
			row := clients.created[0]
			if row.Channel != tc.wantChannel {
				t.Fatalf("canal = %q, se esperaba %q", row.Channel, tc.wantChannel)
			}
			if row.Identifier != tc.callerID {
				t.Fatalf("identificador = %q, se esperaba %q verbatim", row.Identifier, tc.callerID)
			}
			if row.ID != result.ClientID {
				t.Fatalf("id de la fila = %q, se esperaba %q", row.ID, result.ClientID)
			}
			// The account check and the client pre-check must have used the same
			// channel the row was created with.
			if accounts.lastCh != tc.wantChannel || clients.lastLookupChan != tc.wantChannel {
				t.Fatalf("canales consultados = %q/%q, se esperaba %q en ambos", accounts.lastCh, clients.lastLookupChan, tc.wantChannel)
			}
		})
	}
}

// TestExecuteAccountCollision pins step 4 on both channels: an identifier that
// belongs to an internal account — active OR inactive — can never become a client
// identifier, and nothing is written.
func TestExecuteAccountCollision(t *testing.T) {
	cases := []struct {
		name     string
		callerID string
		want     error
		message  string
	}{
		{
			name:     "teléfono de una cuenta",
			callerID: "+5491100000010",
			want:     usecase.ErrIdentifierInAccountPhone,
			message:  "este número ya pertenece a una cuenta del negocio. Contactá al administrador.",
		},
		{
			name:     "email de una cuenta",
			callerID: "operadora@example.com",
			want:     usecase.ErrIdentifierInAccountEmail,
			message:  "este email ya pertenece a una cuenta del negocio. Contactá al administrador.",
		},
	}

	for _, tc := range cases {
		for _, active := range []bool{true, false} {
			name := tc.name + " inactiva"
			if active {
				name = tc.name + " activa"
			}
			t.Run(name, func(t *testing.T) {
				// The port refuses active AND inactive accounts; the double pins
				// that the use case renders the rejection without a second check
				// of its own (is_active is not its business).
				clients := &fakeClients{}
				accounts := &fakeAccounts{taken: true}
				uc := newUseCase(t, clients, accounts, nil, nil)

				result, err := uc.Execute(context.Background(), dto.RegisterClientCommand{CallerID: tc.callerID})
				if !errors.Is(err, tc.want) {
					t.Fatalf("err = %v, se esperaba %v", err, tc.want)
				}
				assertSemantic(t, err, usecase.CodeIdentifierInAccount, tc.message)
				if result != (dto.RegisterClientResult{}) {
					t.Fatalf("resultado = %+v, se esperaba el resultado cero", result)
				}
				if accounts.calls != 1 || accounts.lastID != tc.callerID {
					t.Fatalf("cuenta consultada %d veces (%q), se esperaba exactamente el caller-id", accounts.calls, accounts.lastID)
				}
				if clients.lookupCalls != 0 || clients.createCalls != 0 {
					t.Fatalf("lookup=%d create=%d, se esperaba 0 (la cuenta se verifica primero)", clients.lookupCalls, clients.createCalls)
				}
			})
		}
	}
}

// TestExecuteIdempotentAdmission pins step 5 and the gateway contract of RF3: the
// retry after a successful register_client finds the row, receives Created=false,
// the stored identity, and no write happens.
func TestExecuteIdempotentAdmission(t *testing.T) {
	const existingID = "11111111-1111-4111-8111-000000000042"
	clients := &fakeClients{
		existing: dto.ClientRecord{ID: existingID, DisplayName: "Cliente ****0042"},
		found:    true,
	}
	notify := &recorder{}
	uc := newUseCase(t, clients, nil, nil, notify.notify)

	result, err := uc.Execute(context.Background(), dto.RegisterClientCommand{CallerID: "+5491100000042"})
	if err != nil {
		t.Fatalf("Execute() error inesperado: %v", err)
	}
	if result.Created {
		t.Fatal("Created = true, se esperaba false (admisión idempotente)")
	}
	if result.ClientID != existingID || result.DisplayName != "Cliente ****0042" {
		t.Fatalf("resultado = %+v, se esperaba el cliente existente %s", result, existingID)
	}
	if clients.createCalls != 0 {
		t.Fatalf("createCalls = %d, se esperaba 0 (nada se reescribe)", clients.createCalls)
	}
	if len(notify.calls) != 0 {
		t.Fatalf("duplicate = %+v, se esperaba ninguna notificación: el identificador es el del propio emisor", notify.calls)
	}
}

// TestExecuteIdentifierCollisionIsFailClosed pins step 6's collision path, the
// heart of RF3: the identifier turns out to belong to another client row, so the
// attempt is refused with the Spanish semantic error, nothing is auto-linked, and
// the F2 duplicate hook reports the claim.
func TestExecuteIdentifierCollisionIsFailClosed(t *testing.T) {
	clients := &fakeClients{
		createErr: &dto.IdentifierTakenError{Channel: dto.ChannelEmail, Constraint: "clients_email_lower_uniq"},
	}
	notify := &recorder{}
	uc := newUseCase(t, clients, nil, nil, notify.notify)

	const claimed = "Duenia@Example.COM"
	result, err := uc.Execute(context.Background(), dto.RegisterClientCommand{CallerID: claimed})
	assertSemantic(t, err, usecase.CodeIdentifierTaken, "ya existe un cliente con ese dato. Contactá al negocio.")
	if !errors.Is(err, usecase.ErrIdentifierTaken) {
		t.Fatalf("err = %v, se esperaba ErrIdentifierTaken", err)
	}
	if result != (dto.RegisterClientResult{}) {
		t.Fatalf("resultado = %+v, se esperaba el resultado cero (nada se vincula)", result)
	}

	if len(notify.calls) != 1 {
		t.Fatalf("notificaciones = %+v, se esperaba exactamente el hook de posible-duplicado", notify.calls)
	}
	call := notify.calls[0]
	if call.identifier != claimed {
		t.Fatalf("identificador notificado = %q, se esperaba %q verbatim", call.identifier, claimed)
	}
	if call.claimingClientID != "" {
		t.Fatalf("claimingClientID = %q, se esperaba vacío: el emisor anónimo no tiene fila", call.claimingClientID)
	}
}

// TestExecuteDuplicateNotifierDefaultsToNoOp pins the F1 state of the seam: with
// no notifier wired, an admission and a collision both behave exactly the same
// (the alerts table lands with F2).
func TestExecuteDuplicateNotifierDefaultsToNoOp(t *testing.T) {
	uc := newUseCase(t, &fakeClients{}, nil, nil, nil)

	result, err := uc.Execute(context.Background(), dto.RegisterClientCommand{CallerID: "+5491100000050"})
	if err != nil || !result.Created {
		t.Fatalf("Execute() = %+v, %v; se esperaba admisión", result, err)
	}

	colliding := newUseCase(t, &fakeClients{
		createErr: &dto.IdentifierTakenError{Channel: dto.ChannelPhone, Constraint: "clients.phone"},
	}, nil, nil, nil)
	if _, err := colliding.Execute(context.Background(), dto.RegisterClientCommand{CallerID: "+5491100000051"}); !errors.Is(err, usecase.ErrIdentifierTaken) {
		t.Fatalf("err = %v, se esperaba ErrIdentifierTaken con el hook no-op", err)
	}
}

// TestExecuteDefaultDisplayName pins the PII-masked default: an anonymous client
// is named by the tail of its identifier, never by the whole phone or address,
// and an offered name is stored trimmed.
func TestExecuteDefaultDisplayName(t *testing.T) {
	cases := []struct {
		name      string
		callerID  string
		requested string
		want      string
	}{
		{name: "teléfono sin nombre", callerID: "+5491100000123", want: "Cliente ****0123"},
		{name: "email sin nombre", callerID: "cliente@example.com", want: "Cliente c***@example.com"},
		{name: "email local de un carácter", callerID: "c@example.com", want: "Cliente *@example.com"},
		{name: "identificador corto", callerID: "1234", want: "Cliente ****"},
		{name: "email malformado", callerID: "sin-arroba@", want: "Cliente ****"},
		{name: "nombre ofrecido", callerID: "+5491100000123", requested: "  Ana Gómez  ", want: "Ana Gómez"},
		{name: "nombre en blanco", callerID: "+5491100000123", requested: "   ", want: "Cliente ****0123"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clients := &fakeClients{}
			uc := newUseCase(t, clients, nil, nil, nil)

			result, err := uc.Execute(context.Background(), dto.RegisterClientCommand{CallerID: tc.callerID, DisplayName: tc.requested})
			if err != nil {
				t.Fatalf("Execute() error inesperado: %v", err)
			}
			if result.DisplayName != tc.want {
				t.Fatalf("display_name = %q, se esperaba %q", result.DisplayName, tc.want)
			}
			if len(clients.created) != 1 || clients.created[0].DisplayName != tc.want {
				t.Fatalf("fila creada = %+v, se esperaba display_name %q", clients.created, tc.want)
			}
		})
	}
}

// TestExecuteGeneratesUUIDv4 pins the internal primary key: the DDL enforces a
// 36-character shape, and the value must be a version-4 UUID (the variant and
// version bits are what make it interchangeable with the seeded fixed ids).
func TestExecuteGeneratesUUIDv4(t *testing.T) {
	clients := &fakeClients{}
	uc := newUseCase(t, clients, nil, nil, nil)

	seen := make(map[string]struct{})
	for attempt := 0; attempt < 8; attempt++ {
		result, err := uc.Execute(context.Background(), dto.RegisterClientCommand{CallerID: "+549110000000" + string(rune('0'+attempt))})
		if err != nil {
			t.Fatalf("Execute() error inesperado: %v", err)
		}
		if _, duplicate := seen[result.ClientID]; duplicate {
			t.Fatalf("id repetido %q", result.ClientID)
		}
		seen[result.ClientID] = struct{}{}

		if len(result.ClientID) != 36 {
			t.Fatalf("id = %q (len %d), se esperaba el largo de la DDL (36)", result.ClientID, len(result.ClientID))
		}
		parts := strings.Split(result.ClientID, "-")
		if len(parts) != 5 || len(parts[0]) != 8 || len(parts[1]) != 4 || len(parts[2]) != 4 || len(parts[3]) != 4 || len(parts[4]) != 12 {
			t.Fatalf("id = %q, no tiene la forma 8-4-4-4-12", result.ClientID)
		}
		if !strings.HasPrefix(parts[2], "4") {
			t.Fatalf("id = %q, se esperaba la versión 4 en el tercer grupo", result.ClientID)
		}
		if !strings.ContainsAny(parts[3][:1], "89ab") {
			t.Fatalf("id = %q, se esperaba la variante RFC 4122 en el cuarto grupo", result.ClientID)
		}
	}
}

// TestExecutePropagatesInternalFailures pins the sanitization boundary: a database
// failure is not a semantic rejection, so the transport can collapse it instead of
// showing it to the client; and nothing is written.
func TestExecutePropagatesInternalFailures(t *testing.T) {
	internal := errors.New("disco lleno")

	t.Run("falla la verificación de cuenta", func(t *testing.T) {
		accounts := &fakeAccounts{err: internal}
		clients := &fakeClients{}
		uc := newUseCase(t, clients, accounts, nil, nil)

		_, err := uc.Execute(context.Background(), dto.RegisterClientCommand{CallerID: "+5491100000060"})
		assertInternal(t, err, internal)
		if clients.createCalls != 0 {
			t.Fatalf("createCalls = %d, se esperaba 0", clients.createCalls)
		}
	})

	t.Run("falla la búsqueda del cliente", func(t *testing.T) {
		clients := &fakeClients{lookupErr: internal}
		uc := newUseCase(t, clients, nil, nil, nil)

		_, err := uc.Execute(context.Background(), dto.RegisterClientCommand{CallerID: "+5491100000061"})
		assertInternal(t, err, internal)
		if clients.createCalls != 0 {
			t.Fatalf("createCalls = %d, se esperaba 0", clients.createCalls)
		}
	})

	t.Run("falla la inserción", func(t *testing.T) {
		clients := &fakeClients{createErr: internal}
		notify := &recorder{}
		uc := newUseCase(t, clients, nil, nil, notify.notify)

		_, err := uc.Execute(context.Background(), dto.RegisterClientCommand{CallerID: "+5491100000062"})
		assertInternal(t, err, internal)
		if len(notify.calls) != 0 {
			t.Fatalf("notificaciones = %+v, se esperaba ninguna: un fallo de base no es un duplicado", notify.calls)
		}
	})
}

// TestSemanticErrorExposesCodeAndMessage pins the transport contract: the code is
// what a transport branches on and the message is the only text it may render.
func TestSemanticErrorExposesCodeAndMessage(t *testing.T) {
	cases := []struct {
		err     error
		code    usecase.ErrorCode
		message string
	}{
		{usecase.ErrInvalidIdentifier, usecase.CodeInvalidIdentifier, "el identificador recibido no es válido. Contactá al negocio."},
		{usecase.ErrRateLimited, usecase.CodeRegistrationRateLimited, "se superó el límite de intentos de registro. Volvé a intentarlo más tarde o pedile al negocio que te registre."},
		{usecase.ErrIdentifierInAccountPhone, usecase.CodeIdentifierInAccount, "este número ya pertenece a una cuenta del negocio. Contactá al administrador."},
		{usecase.ErrIdentifierInAccountEmail, usecase.CodeIdentifierInAccount, "este email ya pertenece a una cuenta del negocio. Contactá al administrador."},
		{usecase.ErrIdentifierTaken, usecase.CodeIdentifierTaken, "ya existe un cliente con ese dato. Contactá al negocio."},
	}

	for _, tc := range cases {
		var semantic *usecase.SemanticError
		if !errors.As(tc.err, &semantic) {
			t.Fatalf("err = %v (%T), se esperaba *usecase.SemanticError", tc.err, tc.err)
		}
		if semantic.Code() != tc.code {
			t.Fatalf("código = %q, se esperaba %q", semantic.Code(), tc.code)
		}
		if semantic.Message() != tc.message || semantic.Error() != tc.message {
			t.Fatalf("mensaje = %q, se esperaba %q", semantic.Message(), tc.message)
		}
	}
}

// assertSemantic pins a use case rejection: the Spanish message, the code and the
// fact that it is a semantic error (never a raw internal one).
func assertSemantic(t *testing.T, err error, code usecase.ErrorCode, message string) {
	t.Helper()
	if err == nil {
		t.Fatalf("err = nil, se esperaba el rechazo semántico %q", code)
	}
	var semantic *usecase.SemanticError
	if !errors.As(err, &semantic) {
		t.Fatalf("err = %v (%T), se esperaba *usecase.SemanticError", err, err)
	}
	if semantic.Code() != code {
		t.Fatalf("código = %q, se esperaba %q", semantic.Code(), code)
	}
	if semantic.Message() != message {
		t.Fatalf("mensaje = %q, se esperaba %q", semantic.Message(), message)
	}
}

// assertInternal pins an internal failure: the cause is preserved for the logs and
// the error is NOT semantic, so it can never be rendered to a client.
func assertInternal(t *testing.T, err, cause error) {
	t.Helper()
	if err == nil {
		t.Fatal("err = nil, se esperaba un fallo interno")
	}
	if !errors.Is(err, cause) {
		t.Fatalf("err = %v, se esperaba envolver %v", err, cause)
	}
	var semantic *usecase.SemanticError
	if errors.As(err, &semantic) {
		t.Fatalf("err = %v, un fallo de base no debe ser semántico", err)
	}
}
