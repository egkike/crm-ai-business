// Command crm-ai-business is the composition root of the CRM-AI-Business
// backend. Phase 1 (core-mcp) grows the MCP server here; the internal panel
// (Phase 4) joins later on its own loopback listener.
//
// Binding invariant (AGENTS.md): every listener is loopback-only. The port is
// configurable; the address is not.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/egkike/crm-ai-business/internal/application/usecase"
	crmauth "github.com/egkike/crm-ai-business/internal/auth"
	"github.com/egkike/crm-ai-business/internal/db"
	"github.com/egkike/crm-ai-business/internal/mcp"
	"github.com/egkike/crm-ai-business/internal/repository"
)

const (
	defaultPort = 3001

	// registrationLimitEnv bounds anonymous registrations per identifier
	// (the T5 usecase owns the window semantics; this is only wiring).
	registrationLimitEnv     = "MCP_REGISTRATION_RATE_LIMIT"
	defaultRegistrationLimit = 10

	shutdownTimeout = 5 * time.Second
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("crm-ai-business: %v", err)
	}
}

func run() error {
	port, err := resolvePort(os.Getenv("MCP_PORT"))
	if err != nil {
		return err
	}

	// Composition root: DB + adapters + usecases + transport. No business
	// rules live here — each piece refuses its own invalid configuration
	// (fail-closed) and run() propagates.
	handler, err := buildMCPHandler()
	if err != nil {
		return err
	}

	srv := newServer(port, handler)

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-stop:
		log.Printf("crm-ai-business: señal %s, cerrando", sig)
		ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		// DeadlineExceeded means some connections outlived the window; the
		// listener is closed either way — exit clean, not as a failure.
		return nil
	case err := <-errCh:
		// ErrServerClosed only means Shutdown won the race — never a failure.
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// buildMCPHandler composes the F1 application: SQLite (canonical DDL +
// migrations), the identity chain (CallerLookup adapter + resolver), the
// anonymous registration seam (clients repo + rate limiter) and the MCP
// transport. Every constructor failure refuses to start the server.
func buildMCPHandler() (http.Handler, error) {
	dbPath, err := db.ResolvePath(os.Getenv(db.EnvPath))
	if err != nil {
		return nil, err
	}
	handle, err := db.Open(context.Background(), dbPath)
	if err != nil {
		return nil, err
	}
	// The handle stays open only when the composition fully succeeds; every
	// intermediate failure closes it (review-f90b8b8884381ad5 R3-dbhandle-leak).
	committed := false
	defer func() {
		if !committed {
			_ = handle.Close()
		}
	}()

	lookup, err := repository.NewCallerLookup(handle)
	if err != nil {
		return nil, err
	}
	resolver, err := crmauth.NewCallerResolver(lookup)
	if err != nil {
		return nil, err
	}

	clients, err := repository.NewClientsRepo(handle)
	if err != nil {
		return nil, err
	}
	limit, err := resolveRegistrationLimit(os.Getenv(registrationLimitEnv))
	if err != nil {
		return nil, err
	}
	// clients implements both ClientStore and AccountIdentifierChecker; the
	// duplicate notifier is the F2 seam (default no-op inside the usecase).
	registerClient, err := usecase.NewRegisterClient(clients, clients, usecase.NewMemoryRegistrationLimiter(limit), nil)
	if err != nil {
		return nil, err
	}

	// Catalog (T7): repo + usecases; the handler refuses partial catalog
	// wiring, so all three must be present or none.
	catalogRepo, err := repository.NewCatalogRepo(handle)
	if err != nil {
		return nil, err
	}
	searchCatalog, err := usecase.NewSearchCatalog(catalogRepo)
	if err != nil {
		return nil, err
	}
	createCatalogItem, err := usecase.NewCreateCatalogItem(catalogRepo)
	if err != nil {
		return nil, err
	}
	updateCatalogItem, err := usecase.NewUpdateCatalogItem(catalogRepo)
	if err != nil {
		return nil, err
	}

	// Tickets (T8): repo + five usecases; the handler refuses partial ticket
	// wiring, so all five must be present or none.
	ticketsRepo, err := repository.NewTicketsRepo(handle)
	if err != nil {
		return nil, err
	}

	// The workflow engine (F2 T3): the configured pipeline of the ticket's type
	// plus the round-robin queue of its area. Both are fail-closed adapters, and
	// the use cases refuse to be built without them — the engine has no fixed
	// vocabulary to fall back to.
	workflows, err := repository.NewWorkflowConfigRepo(handle)
	if err != nil {
		return nil, err
	}
	rotation, err := repository.NewRoundRobinRepo(handle)
	if err != nil {
		return nil, err
	}

	createTicket, err := usecase.NewCreateTicket(ticketsRepo, clients, workflows)
	if err != nil {
		return nil, err
	}
	listMyTickets, err := usecase.NewListMyTickets(ticketsRepo)
	if err != nil {
		return nil, err
	}
	listTickets, err := usecase.NewListTickets(ticketsRepo)
	if err != nil {
		return nil, err
	}
	addTicketNote, err := usecase.NewAddTicketNote(ticketsRepo, workflows)
	if err != nil {
		return nil, err
	}
	transitionTicket, err := usecase.NewTransitionTicket(ticketsRepo, workflows, rotation)
	if err != nil {
		return nil, err
	}

	handler, err := mcp.NewHandler(mcp.Dependencies{
		CallerResolver: resolver,
		RegisterClient: registerClient,
		Catalog: &mcp.CatalogDependencies{
			Search: searchCatalog,
			Create: createCatalogItem,
			Update: updateCatalogItem,
		},
		Tickets: &mcp.TicketDependencies{
			Create:     createTicket,
			ListMine:   listMyTickets,
			ListAll:    listTickets,
			AddNote:    addTicketNote,
			Transition: transitionTicket,
		},
	})
	if err != nil {
		return nil, err
	}
	committed = true
	return handler, nil
}

// resolveRegistrationLimit parses MCP_REGISTRATION_RATE_LIMIT: empty means
// the default; 0 disables anonymous registration entirely (fail-closed by
// design); anything non-numeric or negative refuses to start.
func resolveRegistrationLimit(raw string) (int, error) {
	if raw == "" {
		return defaultRegistrationLimit, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 0 {
		return 0, fmt.Errorf("MCP_REGISTRATION_RATE_LIMIT inválido: %q (se espera 0 o un entero positivo; 0 deshabilita el registro)", raw)
	}
	return limit, nil
}

// newServer builds the loopback HTTP server. The address is a constant
// invariant (AGENTS.md: loopback-only); only the port varies.
func newServer(port int, mcpHandler http.Handler) *http.Server {
	return &http.Server{
		Addr:              fmt.Sprintf("127.0.0.1:%d", port),
		Handler:           newMux(mcpHandler),
		ReadHeaderTimeout: 5 * time.Second,
	}
}

// newMux builds the HTTP surface: /healthz liveness and the MCP handler
// mounted at /mcp (the handler owns the POST-only 405 semantics — T6).
func newMux(mcpHandler http.Handler) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", healthHandler)
	mux.Handle("/mcp", mcpHandler)
	return mux
}

// resolvePort parses MCP_PORT; empty means the default. An invalid value is a
// fail-closed startup error — the server never guesses a port.
func resolvePort(raw string) (int, error) {
	if raw == "" {
		return defaultPort, nil
	}
	port, err := strconv.Atoi(raw)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("MCP_PORT inválido: %q (se espera 1-65535)", raw)
	}
	return port, nil
}

func healthHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}
