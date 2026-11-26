package repository_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/egkike/crm-ai-business/internal/application/dto"
	"github.com/egkike/crm-ai-business/internal/repository"
)

// TestNewClientsRepoRejectsNilHandle pins the fail-closed construction rule.
func TestNewClientsRepoRejectsNilHandle(t *testing.T) {
	repo, err := repository.NewClientsRepo(nil)
	if err == nil {
		t.Fatal("NewClientsRepo(nil) debía fallar (fail-closed)")
	}
	if repo != nil {
		t.Fatalf("NewClientsRepo(nil) devolvió un adaptador utilizable: %+v", repo)
	}
}

// TestCreateClientChannels pins the RF3 admission shape: the row fills exactly
// one natural identifier column — the channel that admitted the client — stores
// the identifier verbatim and leaves the other column NULL.
func TestCreateClientChannels(t *testing.T) {
	cases := []struct {
		name       string
		newClient  dto.NewClient
		wantPhone  string
		wantEmail  string
		wantStored string
	}{
		{
			name: "WhatsApp-first: phone, email NULL",
			// A phone is never normalized: the spaces and dashes arrive verbatim
			// from the channel and stay verbatim in the column.
			newClient:  dto.NewClient{ID: uuid(900), Channel: dto.ChannelPhone, Identifier: "+54 9 11 4444-0900", DisplayName: "Cliente ****0900"},
			wantPhone:  "+54 9 11 4444-0900",
			wantEmail:  "",
			wantStored: "+54 9 11 4444-0900",
		},
		{
			name:       "email-first: email, phone NULL",
			newClient:  dto.NewClient{ID: uuid(901), Channel: dto.ChannelEmail, Identifier: "Cliente@Example.COM", DisplayName: "Cliente c***@example.com"},
			wantPhone:  "",
			wantEmail:  "Cliente@Example.COM",
			wantStored: "Cliente@Example.COM",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handle := openDB(t)
			repo, err := repository.NewClientsRepo(handle)
			if err != nil {
				t.Fatalf("NewClientsRepo() error inesperado: %v", err)
			}

			if err := repo.CreateClient(context.Background(), tc.newClient); err != nil {
				t.Fatalf("CreateClient() error inesperado: %v", err)
			}

			var phone, email, name *string
			var id string
			if err := handle.QueryRow(
				`SELECT id, phone, email, display_name FROM clients WHERE id = ?`, tc.newClient.ID,
			).Scan(&id, &phone, &email, &name); err != nil {
				t.Fatalf("leer el cliente creado: %v", err)
			}

			if id != tc.newClient.ID {
				t.Fatalf("id = %q, se esperaba %q", id, tc.newClient.ID)
			}
			if got := deref(phone); got != tc.wantPhone {
				t.Fatalf("phone = %q, se esperaba %q", got, tc.wantPhone)
			}
			if got := deref(email); got != tc.wantEmail {
				t.Fatalf("email = %q, se esperaba %q", got, tc.wantEmail)
			}
			if name == nil || *name != tc.newClient.DisplayName {
				t.Fatalf("display_name = %v, se esperaba %q", name, tc.newClient.DisplayName)
			}

			// The identifier must also be the one the pre-check finds: the write
			// and the read share predicates (phone byte-exact, e-mail folded), so
			// the registration seam and the §3.8 resolver cannot disagree.
			record, found, err := repo.ClientByIdentifier(context.Background(), tc.newClient.Channel, tc.wantStored)
			if err != nil {
				t.Fatalf("ClientByIdentifier() error inesperado: %v", err)
			}
			if !found || record.ID != tc.newClient.ID {
				t.Fatalf("ClientByIdentifier(%q) = %+v, %t; se esperaba el cliente %s", tc.wantStored, record, found, tc.newClient.ID)
			}
		})
	}
}

// TestCreateClientUniqueConflictsAreTyped pins the two uniqueness rules of the
// schema: the column UNIQUE (clients.phone / clients.email) and the
// lower(email) index. Both must surface as *dto.IdentifierTakenError — the
// application layer's signal to render the Spanish rejection and to fire the
// duplicate hook — and neither may leak the driver's text.
func TestCreateClientUniqueConflictsAreTyped(t *testing.T) {
	t.Run("teléfono exacto (UNIQUE clients.phone)", func(t *testing.T) {
		handle := openDB(t)
		seedClient(t, handle, client{id: uuid(910), phone: "+5491100000910", name: "Existente"})
		repo, err := repository.NewClientsRepo(handle)
		if err != nil {
			t.Fatalf("NewClientsRepo() error inesperado: %v", err)
		}

		conflict := repo.CreateClient(context.Background(), dto.NewClient{
			ID: uuid(911), Channel: dto.ChannelPhone, Identifier: "+5491100000910", DisplayName: "Nuevo",
		})
		assertIdentifierTaken(t, conflict, dto.ChannelPhone)
		if got := countClients(t, handle); got != 1 {
			t.Fatalf("filas de clients = %d, se esperaba 1 (la fila existente intacta)", got)
		}
	})

	t.Run("email con la misma capitalización (UNIQUE clients.email)", func(t *testing.T) {
		handle := openDB(t)
		seedClient(t, handle, client{id: uuid(920), email: "exacto@example.com", name: "Existente"})
		repo, err := repository.NewClientsRepo(handle)
		if err != nil {
			t.Fatalf("NewClientsRepo() error inesperado: %v", err)
		}

		conflict := repo.CreateClient(context.Background(), dto.NewClient{
			ID: uuid(921), Channel: dto.ChannelEmail, Identifier: "exacto@example.com", DisplayName: "Nuevo",
		})
		assertIdentifierTaken(t, conflict, dto.ChannelEmail)
	})

	t.Run("email con otra capitalización (índice lower(email))", func(t *testing.T) {
		handle := openDB(t)
		seedClient(t, handle, client{id: uuid(930), email: "Duenio@Example.Com", name: "Existente"})
		repo, err := repository.NewClientsRepo(handle)
		if err != nil {
			t.Fatalf("NewClientsRepo() error inesperado: %v", err)
		}

		conflict := repo.CreateClient(context.Background(), dto.NewClient{
			ID: uuid(931), Channel: dto.ChannelEmail, Identifier: "duenio@example.com", DisplayName: "Nuevo",
		})
		assertIdentifierTaken(t, conflict, dto.ChannelEmail)
		if got := countClients(t, handle); got != 1 {
			t.Fatalf("filas de clients = %d, se esperaba 1 (nada se auto-vincula ni se duplica)", got)
		}
	})
}

// TestCreateClientPrimaryKeyCollisionIsInternal pins the classifier's boundary:
// a colliding internal UUID is a defect of the id generator, not a claim to
// reject, so it must NOT be reported as an identifier conflict (the use case
// would otherwise tell the client "ya existe un cliente con ese dato").
func TestCreateClientPrimaryKeyCollisionIsInternal(t *testing.T) {
	handle := openDB(t)
	seedClient(t, handle, client{id: uuid(940), phone: "+5491100000940", name: "Existente"})
	repo, err := repository.NewClientsRepo(handle)
	if err != nil {
		t.Fatalf("NewClientsRepo() error inesperado: %v", err)
	}

	collision := repo.CreateClient(context.Background(), dto.NewClient{
		ID: uuid(940), Channel: dto.ChannelPhone, Identifier: "+5491100000941", DisplayName: "Nuevo",
	})
	if collision == nil {
		t.Fatal("CreateClient() debía fallar con el id repetido")
	}
	var taken *dto.IdentifierTakenError
	if errors.As(collision, &taken) {
		t.Fatalf("err = %v, una colisión de PK no es un identificador tomado", collision)
	}
}

// TestCreateClientRejectsIncompleteRow pins the validation of the carrier before
// it reaches the database: these are programming errors, so they are internal
// errors and nothing is written.
func TestCreateClientRejectsIncompleteRow(t *testing.T) {
	handle := openDB(t)
	repo, err := repository.NewClientsRepo(handle)
	if err != nil {
		t.Fatalf("NewClientsRepo() error inesperado: %v", err)
	}

	cases := map[string]dto.NewClient{
		"sin id interno":    {Channel: dto.ChannelPhone, Identifier: "+5491100000950"},
		"sin identificador": {ID: uuid(951), Channel: dto.ChannelPhone},
		"canal desconocido": {ID: uuid(952), Channel: dto.Channel("telegram"), Identifier: "+5491100000952"},
	}
	for name, invalid := range cases {
		t.Run(name, func(t *testing.T) {
			err := repo.CreateClient(context.Background(), invalid)
			if err == nil {
				t.Fatal("CreateClient() debía rechazar la fila incompleta")
			}
			var taken *dto.IdentifierTakenError
			if errors.As(err, &taken) {
				t.Fatalf("err = %v, una fila inválida no es un identificador tomado", err)
			}
		})
	}
	if got := countClients(t, handle); got != 0 {
		t.Fatalf("filas de clients = %d, se esperaba 0", got)
	}
}

// TestClientByIdentifier pins the two predicates: a phone matches byte-exact (a
// normalized variant is a different identifier) and an e-mail matches
// case-insensitively (the expression the UNIQUE index enforces).
func TestClientByIdentifier(t *testing.T) {
	handle := openDB(t)
	const verbatim = "+54 9 11 4444-0960"
	seedClient(t, handle, client{id: uuid(960), phone: verbatim, name: "Cliente WhatsApp"})
	seedClient(t, handle, client{id: uuid(961), email: "Cliente@Example.COM", name: "Cliente email"})
	repo, err := repository.NewClientsRepo(handle)
	if err != nil {
		t.Fatalf("NewClientsRepo() error inesperado: %v", err)
	}
	ctx := context.Background()

	t.Run("teléfono byte-exacto", func(t *testing.T) {
		record, found, err := repo.ClientByIdentifier(ctx, dto.ChannelPhone, verbatim)
		if err != nil {
			t.Fatalf("ClientByIdentifier() error inesperado: %v", err)
		}
		if !found || record.ID != uuid(960) || record.DisplayName != "Cliente WhatsApp" {
			t.Fatalf("registro = %+v, %t; se esperaba el cliente %s", record, found, uuid(960))
		}

		for _, normalized := range []string{
			"+5491144440960",
			"+54 9 11 4444-0960 ",
			" 5491144440960",
		} {
			if _, found, err := repo.ClientByIdentifier(ctx, dto.ChannelPhone, normalized); err != nil {
				t.Fatalf("ClientByIdentifier(%q) error inesperado: %v", normalized, err)
			} else if found {
				t.Fatalf("ClientByIdentifier(%q) encontró un cliente; el teléfono no se normaliza", normalized)
			}
		}
	})

	t.Run("email case-insensitive", func(t *testing.T) {
		for _, identifier := range []string{"cliente@example.com", "CLIENTE@EXAMPLE.COM", "Cliente@Example.COM"} {
			record, found, err := repo.ClientByIdentifier(ctx, dto.ChannelEmail, identifier)
			if err != nil {
				t.Fatalf("ClientByIdentifier(%q) error inesperado: %v", identifier, err)
			}
			if !found || record.ID != uuid(961) {
				t.Fatalf("registro para %q = %+v, %t; se esperaba el cliente %s", identifier, record, found, uuid(961))
			}
		}
	})

	t.Run("desconocido", func(t *testing.T) {
		for _, identifier := range []string{"+5491100000999", "nadie@example.com"} {
			record, found, err := repo.ClientByIdentifier(ctx, dto.ChannelPhone, identifier)
			if err != nil {
				t.Fatalf("ClientByIdentifier(%q) error inesperado: %v", identifier, err)
			}
			if found || record != (dto.ClientRecord{}) {
				t.Fatalf("registro = %+v, %t; se esperaba no encontrado", record, found)
			}
		}
	})

	t.Run("canal desconocido", func(t *testing.T) {
		if _, _, err := repo.ClientByIdentifier(ctx, dto.Channel("telegram"), "+5491100000960"); err == nil {
			t.Fatal("ClientByIdentifier() debía rechazar el canal desconocido")
		}
	})
}

// TestClientByIdentifierAmbiguousIsRejected pins the "0 o 1, nunca ambiguo" rule
// of PRD §3.7 even without the uniqueness guarantee: two rows for one identifier
// must fail closed instead of answering with whichever row came first.
func TestClientByIdentifierAmbiguousIsRejected(t *testing.T) {
	handle := openDB(t)
	if _, err := handle.Exec(`DROP INDEX clients_email_lower_uniq`); err != nil {
		t.Fatalf("quitar el índice de unicidad: %v", err)
	}
	seedClient(t, handle, client{id: uuid(970), email: "duplicado@example.com", name: "Uno"})
	seedClient(t, handle, client{id: uuid(971), email: "Duplicado@Example.com", name: "Dos"})
	repo, err := repository.NewClientsRepo(handle)
	if err != nil {
		t.Fatalf("NewClientsRepo() error inesperado: %v", err)
	}

	record, found, err := repo.ClientByIdentifier(context.Background(), dto.ChannelEmail, "duplicado@example.com")
	if err == nil {
		t.Fatalf("ClientByIdentifier() = %+v, %t, nil; se esperaba un error de ambigüedad", record, found)
	}
	if !strings.Contains(err.Error(), "ambiguos") {
		t.Fatalf("err = %v, se esperaba el error de clientes ambiguos", err)
	}
}

// TestAccountIdentifierExists pins the collision rule of RF3: an identifier
// belongs to an account whether the account is active or not, and an identifier
// that only a client owns does not.
func TestAccountIdentifierExists(t *testing.T) {
	handle := openDB(t)
	seedAccount(t, handle, account{id: uuid(980), phone: "+5491100000980", email: "activa@example.com", role: "staff", active: true, name: "Activa"})
	seedAccount(t, handle, account{id: uuid(981), phone: "+5491100000981", email: "Inactiva@Example.com", role: "staff", active: false, name: "Inactiva"})
	seedClient(t, handle, client{id: uuid(982), phone: "+5491100000982", email: "cliente@example.com", name: "Cliente"})
	repo, err := repository.NewClientsRepo(handle)
	if err != nil {
		t.Fatalf("NewClientsRepo() error inesperado: %v", err)
	}
	ctx := context.Background()

	cases := []struct {
		name       string
		channel    dto.Channel
		identifier string
		want       bool
	}{
		{name: "teléfono de cuenta activa", channel: dto.ChannelPhone, identifier: "+5491100000980", want: true},
		{name: "teléfono de cuenta inactiva", channel: dto.ChannelPhone, identifier: "+5491100000981", want: true},
		{name: "email de cuenta activa", channel: dto.ChannelEmail, identifier: "ACTIVA@example.com", want: true},
		{name: "email de cuenta inactiva", channel: dto.ChannelEmail, identifier: "inactiva@example.com", want: true},
		{name: "teléfono solo de cliente", channel: dto.ChannelPhone, identifier: "+5491100000982", want: false},
		{name: "email solo de cliente", channel: dto.ChannelEmail, identifier: "cliente@example.com", want: false},
		{name: "desconocido", channel: dto.ChannelPhone, identifier: "+5491100000999", want: false},
		{name: "teléfono no se normaliza", channel: dto.ChannelPhone, identifier: "+5491100000980 ", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := repo.AccountIdentifierExists(ctx, tc.channel, tc.identifier)
			if err != nil {
				t.Fatalf("AccountIdentifierExists() error inesperado: %v", err)
			}
			if got != tc.want {
				t.Fatalf("AccountIdentifierExists(%q, %q) = %t, se esperaba %t", tc.channel, tc.identifier, got, tc.want)
			}
		})
	}

	t.Run("canal desconocido", func(t *testing.T) {
		if _, err := repo.AccountIdentifierExists(ctx, dto.Channel("telegram"), "+5491100000980"); err == nil {
			t.Fatal("AccountIdentifierExists() debía rechazar el canal desconocido")
		}
	})
}

// TestClientsRepoPropagatesDatabaseFailure pins the distinction the use case
// depends on: an infrastructure failure is not an identifier conflict, so it must
// not be rendered to the client as "ya existe un cliente con ese dato".
func TestClientsRepoPropagatesDatabaseFailure(t *testing.T) {
	handle := openDB(t)
	repo, err := repository.NewClientsRepo(handle)
	if err != nil {
		t.Fatalf("NewClientsRepo() error inesperado: %v", err)
	}
	ctx := context.Background()

	if _, err := handle.Exec(`DROP TABLE clients`); err != nil {
		t.Fatalf("romper la tabla clients: %v", err)
	}

	createErr := repo.CreateClient(ctx, dto.NewClient{ID: uuid(990), Channel: dto.ChannelPhone, Identifier: "+5491100000990"})
	if createErr == nil {
		t.Fatal("CreateClient() debía fallar con la tabla ausente")
	}
	var taken *dto.IdentifierTakenError
	if errors.As(createErr, &taken) {
		t.Fatalf("err = %v, un fallo de base no es un identificador tomado", createErr)
	}

	if _, _, err := repo.ClientByIdentifier(ctx, dto.ChannelPhone, "+5491100000990"); err == nil {
		t.Fatal("ClientByIdentifier() debía fallar con la tabla ausente")
	}
	if _, err := repo.AccountIdentifierExists(ctx, dto.ChannelPhone, "+5491100000990"); err != nil {
		t.Fatalf("AccountIdentifierExists() no depende de clients y no debía fallar: %v", err)
	}
}

// assertIdentifierTaken pins the typed conflict and its channel.
func assertIdentifierTaken(t *testing.T, err error, want dto.Channel) {
	t.Helper()
	if err == nil {
		t.Fatal("CreateClient() debía rechazar el identificador ya tomado")
	}
	var taken *dto.IdentifierTakenError
	if !errors.As(err, &taken) {
		t.Fatalf("err = %v (%T), se esperaba *dto.IdentifierTakenError", err, err)
	}
	if taken.Channel != want {
		t.Fatalf("canal del conflicto = %q, se esperaba %q", taken.Channel, want)
	}
	if taken.Constraint == "" {
		t.Error("el conflicto debe nombrar la restricción que lo rechazó (diagnóstico interno)")
	}
}

// countClients is how many rows the clients table holds. Rejections are pinned
// on stored data, not on the error shape alone: a refused attempt must leave the
// table untouched.
func countClients(t *testing.T, handle *sql.DB) int {
	t.Helper()
	var rows int
	if err := handle.QueryRow(`SELECT COUNT(*) FROM clients`).Scan(&rows); err != nil {
		t.Fatalf("contar clientes: %v", err)
	}
	return rows
}

// deref returns the value behind a nullable string column.
func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
