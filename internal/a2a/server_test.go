package a2a

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/anatolykoptev/dozor/internal/toolreg"
)

// passthroughHandler is a trivial next handler that records it was reached.
func passthroughHandler(reached *bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*reached = true
		w.WriteHeader(http.StatusOK)
	})
}

// TestBearerAuthMiddleware_WithSecret verifies that a valid Bearer token is
// accepted and an invalid one is rejected (existing behavior, must stay green).
func TestBearerAuthMiddleware_WithSecret(t *testing.T) {
	const secret = "s3cr3t"

	t.Run("valid token passes", func(t *testing.T) {
		reached := false
		h := bearerAuthMiddleware(passthroughHandler(&reached), secret, false)
		req := httptest.NewRequest(http.MethodPost, "/a2a", nil)
		req.Header.Set("Authorization", "Bearer "+secret)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", w.Code)
		}
		if !reached {
			t.Fatal("next handler was not called")
		}
	})

	t.Run("invalid token rejected", func(t *testing.T) {
		reached := false
		h := bearerAuthMiddleware(passthroughHandler(&reached), secret, false)
		req := httptest.NewRequest(http.MethodPost, "/a2a", nil)
		req.Header.Set("Authorization", "Bearer wrong")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", w.Code)
		}
		if reached {
			t.Fatal("next handler must not be called on bad token")
		}
	})

	t.Run("missing auth header rejected", func(t *testing.T) {
		reached := false
		h := bearerAuthMiddleware(passthroughHandler(&reached), secret, false)
		req := httptest.NewRequest(http.MethodPost, "/a2a", nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", w.Code)
		}
	})
}

// TestBearerAuthMiddleware_EmptySecret_FailClosed verifies that when
// DOZOR_A2A_SECRET is empty and DOZOR_A2A_ALLOW_INSECURE is not set,
// the middleware returns 503 Service Unavailable (fail-closed).
func TestBearerAuthMiddleware_EmptySecret_FailClosed(t *testing.T) {
	reached := false
	h := bearerAuthMiddleware(passthroughHandler(&reached), "", false)
	req := httptest.NewRequest(http.MethodPost, "/a2a", nil)
	// No auth header — attacker with localhost access
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 (fail-closed), got %d", w.Code)
	}
	if reached {
		t.Fatal("next handler must not be called when secret is empty (fail-closed)")
	}
}

// TestBearerAuthMiddleware_EmptySecret_AllowInsecure verifies that when
// DOZOR_A2A_ALLOW_INSECURE=true is set, the middleware allows pass-through
// (opt-in escape hatch for local dev).
func TestBearerAuthMiddleware_EmptySecret_AllowInsecure(t *testing.T) {
	reached := false
	h := bearerAuthMiddleware(passthroughHandler(&reached), "", true)
	req := httptest.NewRequest(http.MethodPost, "/a2a", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 in insecure opt-in mode, got %d", w.Code)
	}
	if !reached {
		t.Fatal("next handler must be called when DOZOR_A2A_ALLOW_INSECURE=true")
	}
}

// TestBearerAuthMiddleware_TOCTOU verifies that allowInsecure is captured at
// middleware construction time (Register call), not re-read from the environment
// on every request. A process that starts with DOZOR_A2A_ALLOW_INSECURE=true
// must not become inaccessible if the env var is cleared after startup.
func TestBearerAuthMiddleware_TOCTOU(t *testing.T) {
	// Simulate Register capturing allowInsecure=true at startup (env var was "true" then).
	// The middleware receives the already-captured bool — it never reads the env itself.
	reached := false
	h := bearerAuthMiddleware(passthroughHandler(&reached), "", true)

	// First request: env var still set — must pass.
	req1 := httptest.NewRequest(http.MethodPost, "/a2a", nil)
	w1 := httptest.NewRecorder()
	h.ServeHTTP(w1, req1)
	if w1.Code != http.StatusOK {
		t.Fatalf("first request: expected 200, got %d", w1.Code)
	}

	// Simulate runtime env mutation — attacker or operator unsets the var.
	t.Setenv("DOZOR_A2A_ALLOW_INSECURE", "")

	// Second request: env var now gone — middleware must still pass because the
	// allowInsecure bool was captured at construction time, not re-read per-request.
	reached = false
	req2 := httptest.NewRequest(http.MethodPost, "/a2a", nil)
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("TOCTOU: second request (env cleared after startup) expected 200, got %d — middleware must use captured bool, not re-read os.Getenv", w2.Code)
	}
	if !reached {
		t.Fatal("TOCTOU: next handler not called after env var was cleared — captured bool not working")
	}
}

// TestRegister_EmptySecret_NoOptIn verifies that Register returns an error
// when DOZOR_A2A_SECRET is empty and DOZOR_A2A_ALLOW_INSECURE is not set.
func TestRegister_EmptySecret_NoOptIn(t *testing.T) {
	t.Setenv("DOZOR_A2A_ALLOW_INSECURE", "")

	mux := http.NewServeMux()
	err := Register(mux, nil, toolreg.NewRegistry(), "http://localhost:8765", "test", "")
	if err == nil {
		t.Fatal("expected error when registering A2A without secret (fail-closed), got nil")
	}
}

// TestRegister_EmptySecret_WithOptIn verifies that Register succeeds when
// DOZOR_A2A_ALLOW_INSECURE=true is explicitly set.
func TestRegister_EmptySecret_WithOptIn(t *testing.T) {
	t.Setenv("DOZOR_A2A_ALLOW_INSECURE", "true")

	mux := http.NewServeMux()
	err := Register(mux, nil, toolreg.NewRegistry(),"http://localhost:8765", "test", "")
	if err != nil {
		t.Fatalf("expected no error with DOZOR_A2A_ALLOW_INSECURE=true, got %v", err)
	}
}

// TestRegister_WithSecret verifies that Register succeeds when a non-empty
// secret is provided (normal production path).
func TestRegister_WithSecret(t *testing.T) {
	t.Setenv("DOZOR_A2A_ALLOW_INSECURE", "")

	mux := http.NewServeMux()
	err := Register(mux, nil, toolreg.NewRegistry(),"http://localhost:8765", "test", "mysecret")
	if err != nil {
		t.Fatalf("expected no error with non-empty secret, got %v", err)
	}
}
