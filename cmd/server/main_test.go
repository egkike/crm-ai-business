package main

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// stubMCP lets the wiring tests observe what the composition mounts at
// /mcp without speaking MCP (the transport semantics are pinned in
// internal/mcp with its own suite).
func stubMCP() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
}

// TestHealthz pins the liveness probe: it answers 200 with its JSON body.
func TestHealthz(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()

	newMux(stubMCP()).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("healthz = %d, se espera 200", rec.Code)
	}
	if got := rec.Body.String(); got != `{"status":"ok"}` {
		t.Fatalf("healthz body = %q, se espera el JSON canónico", got)
	}
}

// TestMcpRouting pins the composition: whatever handler run() builds gets
// mounted at /mcp by newMux. The POST-only 405 contract and the JSON-RPC
// protocol are the handler's own (pinned in internal/mcp).
func TestMcpRouting(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	rec := httptest.NewRecorder()

	newMux(stubMCP()).ServeHTTP(rec, req)

	if rec.Code != http.StatusTeapot {
		t.Fatalf("POST /mcp = %d, se espera que llegue al handler montado (418 del stub)", rec.Code)
	}
}

// TestMcpPostOnly is retired with T6: the 405/Allow and protocol semantics
// moved into internal/mcp's handler (pinned by its own suite); the wiring
// only routes (TestMcpRouting).

// TestResolveRegistrationLimit pins the fail-closed wiring rule for the
// anonymous-registration limiter: empty = default, 0 = disabled, invalid
// refuses to start.
func TestResolveRegistrationLimit(t *testing.T) {
	t.Run("empty usa el default", func(t *testing.T) {
		got, err := resolveRegistrationLimit("")
		if err != nil || got != defaultRegistrationLimit {
			t.Fatalf("resolveRegistrationLimit(\"\") = %d, %v; se espera %d, nil", got, err, defaultRegistrationLimit)
		}
	})

	t.Run("cero deshabilita", func(t *testing.T) {
		got, err := resolveRegistrationLimit("0")
		if err != nil || got != 0 {
			t.Fatalf("resolveRegistrationLimit(\"0\") = %d, %v; se espera 0, nil", got, err)
		}
	})

	t.Run("válido pasa", func(t *testing.T) {
		got, err := resolveRegistrationLimit("25")
		if err != nil || got != 25 {
			t.Fatalf("resolveRegistrationLimit(\"25\") = %d, %v; se espera 25, nil", got, err)
		}
	})

	for _, raw := range []string{"no-numérico", "-1"} {
		t.Run("inválido "+raw+" falla cerrado", func(t *testing.T) {
			if _, err := resolveRegistrationLimit(raw); err == nil {
				t.Fatalf("resolveRegistrationLimit(%q) = nil error; se espera fail-closed", raw)
			}
		})
	}
}

// TestResolvePort pins the fail-closed config rule: invalid values refuse to
// start; empty falls back to the default; valid values pass through.
func TestResolvePort(t *testing.T) {
	t.Run("empty usa el default", func(t *testing.T) {
		got, err := resolvePort("")
		if err != nil || got != defaultPort {
			t.Fatalf("resolvePort(\"\") = %d, %v; se espera %d, nil", got, err, defaultPort)
		}
	})

	t.Run("válido pasa", func(t *testing.T) {
		got, err := resolvePort("3101")
		if err != nil || got != 3101 {
			t.Fatalf("resolvePort(\"3101\") = %d, %v; se espera 3101, nil", got, err)
		}
	})

	// Boundary values pin the exact accepted range (advisory R3-002): a
	// regression flipping the bounds to 2-65534 must be caught here.
	for _, raw := range []string{"1", "65535"} {
		t.Run("boundary válido "+raw, func(t *testing.T) {
			got, err := resolvePort(raw)
			want, _ := strconv.Atoi(raw)
			if err != nil || got != want {
				t.Fatalf("resolvePort(%q) = %d, %v; se espera %d, nil", raw, got, err, want)
			}
		})
	}

	for _, raw := range []string{"no-numérico", "0", "-1", "65536", "1e3"} {
		t.Run("inválido "+raw+" falla cerrado", func(t *testing.T) {
			if _, err := resolvePort(raw); err == nil {
				t.Fatalf("resolvePort(%q) = nil error; se espera fail-closed", raw)
			}
		})
	}
}

// TestLoopbackInvariant pins the AGENTS.md binding rule at the composition
// root: the server address is always loopback, whatever the port is.
func TestLoopbackInvariant(t *testing.T) {
	srv := newServer(4545, stubMCP())
	if !strings.HasPrefix(srv.Addr, "127.0.0.1:") {
		t.Fatalf("server addr = %q; se espera loopback invariante", srv.Addr)
	}
}
