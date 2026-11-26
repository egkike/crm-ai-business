// Command crm-ai-business is the composition root of the CRM-AI-Business
// backend. Phase 1 (core-mcp) grows the MCP server here; the internal panel
// (Phase 4) joins later on its own loopback listener.
//
// Binding invariant (AGENTS.md): every listener is loopback-only. The port is
// configurable; the address is not.
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

const defaultPort = 3001

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

	srv := newServer(port)

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-stop:
		log.Printf("crm-ai-business: señal %s, cerrando", sig)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(ctx)
	case err := <-errCh:
		return err
	}
}

// newServer builds the loopback HTTP server. The address is a constant
// invariant (AGENTS.md: loopback-only); only the port varies.
func newServer(port int) *http.Server {
	return &http.Server{
		Addr:              fmt.Sprintf("127.0.0.1:%d", port),
		Handler:           newMux(),
		ReadHeaderTimeout: 5 * time.Second,
	}
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

// newMux builds the HTTP surface. Phase 1 T1 ships only the liveness probe;
// /mcp (POST-only, 405 by design) lands with the MCP server task.
func newMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", healthHandler)
	return mux
}

func healthHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}
