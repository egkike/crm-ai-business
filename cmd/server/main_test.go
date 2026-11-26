package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHealthz pins the only surface of the T1 skeleton: the liveness probe
// answers 200 with its JSON body.
func TestHealthz(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()

	newMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("healthz = %d, se espera 200", rec.Code)
	}
	if got := rec.Body.String(); got != `{"status":"ok"}` {
		t.Fatalf("healthz body = %q, se espera el JSON canónico", got)
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
	srv := newServer(4545)
	if !strings.HasPrefix(srv.Addr, "127.0.0.1:") {
		t.Fatalf("server addr = %q; se espera loopback invariante", srv.Addr)
	}
}
