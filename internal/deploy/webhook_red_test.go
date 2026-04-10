package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// --- Missing method / routing edge cases ---

func TestHandler_GetMethod_Returns405(t *testing.T) {
	t.Parallel()

	cfg := testConfig("")
	q, _ := newTestQueue()
	h := NewHandler(cfg, q, func(string) {})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/deploy/github", http.NoBody)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: expected 405, got %d", w.Code)
	}
}

func TestHandler_PutMethod_Returns405(t *testing.T) {
	t.Parallel()

	cfg := testConfig("")
	q, _ := newTestQueue()
	h := NewHandler(cfg, q, func(string) {})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPut, "/deploy/github", strings.NewReader("{}"))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("PUT: expected 405, got %d", w.Code)
	}
}

// --- Oversized body ---

func TestHandler_OversizedBody_IsHandledGracefully(t *testing.T) {
	t.Parallel()

	// Body larger than maxWebhookBody (64KB) — LimitReader should prevent OOM
	// and the remainder of the (truncated) body will fail JSON parse → 400.
	cfg := testConfig("")
	q, _ := newTestQueue()
	h := NewHandler(cfg, q, func(string) {})

	// Build a body larger than maxWebhookBody: valid JSON prefix + garbage.
	const oversize = maxWebhookBody + 1024
	large := make([]byte, oversize)
	for i := range large {
		large[i] = 'x'
	}

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/deploy/github", bytes.NewReader(large))
	req.Header.Set("X-GitHub-Event", "push")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	// Body is not valid JSON after truncation → 400. The key invariant is that
	// the server does not block forever or OOM — it responds within the test.
	if w.Code != http.StatusBadRequest {
		t.Errorf("oversized body: expected 400 (invalid JSON after truncation), got %d", w.Code)
	}
}

// --- Missing signature when secret configured ---

func TestHandler_MissingSignature_WhenSecretConfigured_Returns401(t *testing.T) {
	t.Parallel()

	cfg := testConfig("my-secret")
	q, _ := newTestQueue()
	h := NewHandler(cfg, q, func(string) {})

	body := pushPayload("anatolykoptev/dozor", "refs/heads/main", "abc123")

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/deploy/github", strings.NewReader(body))
	req.Header.Set("X-GitHub-Event", "push")
	// Deliberately omit X-Hub-Signature-256.

	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("missing sig with secret configured: expected 401, got %d", w.Code)
	}
}

// --- No secret configured — signature header is ignored ---

func TestHandler_WrongSignature_WhenNoSecretConfigured_IsAccepted(t *testing.T) {
	t.Parallel()

	// When config.Secret == "", signature verification is skipped entirely.
	cfg := testConfig("")
	q, _ := newTestQueue()
	h := NewHandler(cfg, q, func(string) {})

	body := pushPayload("anatolykoptev/dozor", "refs/heads/main", "abc123")

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/deploy/github", strings.NewReader(body))
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-Hub-Signature-256", "sha256=wrong")

	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	// Should proceed normally (200), not 401.
	if w.Code != http.StatusOK {
		t.Errorf("no-secret mode: expected 200 regardless of sig, got %d", w.Code)
	}
}

// --- Concurrent deduplication: second push for same repo returns "deduplicated" ---

func TestHandler_ConcurrentDuplicatePush_SecondIsDeduped(t *testing.T) {
	t.Parallel()

	cfg := testConfig("")
	q, _ := newTestQueue()
	h := NewHandler(cfg, q, func(string) {})

	body := pushPayload("anatolykoptev/dozor", "refs/heads/main", "abc1234567890")

	makeRequest := func() *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/deploy/github", strings.NewReader(body))
		req.Header.Set("X-GitHub-Event", "push")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}

	w1 := makeRequest()
	if w1.Code != http.StatusOK {
		t.Fatalf("first request: expected 200, got %d", w1.Code)
	}

	var r1 map[string]string
	if err := json.NewDecoder(w1.Body).Decode(&r1); err != nil {
		t.Fatalf("decode first response: %v", err)
	}
	if r1["status"] != "queued" {
		t.Errorf("first push: expected 'queued', got %q", r1["status"])
	}

	// Second identical push while first is still in queue.
	w2 := makeRequest()
	if w2.Code != http.StatusOK {
		t.Fatalf("second request: expected 200, got %d", w2.Code)
	}
	var r2 map[string]string
	if err := json.NewDecoder(w2.Body).Decode(&r2); err != nil {
		t.Fatalf("decode second response: %v", err)
	}
	if r2["status"] != "deduplicated" {
		t.Errorf("second push (dup): expected 'deduplicated', got %q", r2["status"])
	}
}

// --- Concurrent requests don't corrupt internal state ---

func TestHandler_ConcurrentRequests_NoRaceCondition(t *testing.T) {
	// Note: run with -race to catch data races.
	cfg := testConfig("")
	q, _ := newTestQueue()

	var notifyCount atomic.Int64
	h := NewHandler(cfg, q, func(string) { notifyCount.Add(1) })

	const goroutines = 20
	var wg sync.WaitGroup

	for i := range goroutines {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Mix different branches: push to main, unknown repo, non-main branch.
			var body string
			var event string
			switch i % 3 {
			case 0:
				body = pushPayload("anatolykoptev/dozor", "refs/heads/main", "sha"+string(rune('a'+i)))
				event = "push"
			case 1:
				body = pushPayload("unknown/repo", "refs/heads/main", "sha")
				event = "push"
			case 2:
				body = "{}"
				event = "ping"
			}
			req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/deploy/github", strings.NewReader(body))
			req.Header.Set("X-GitHub-Event", event)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Errorf("goroutine %d: expected 200, got %d", i, w.Code)
			}
		}(i)
	}
	wg.Wait()
}

// --- Empty body ---

func TestHandler_EmptyBody_PushEvent_Returns400(t *testing.T) {
	t.Parallel()

	cfg := testConfig("")
	q, _ := newTestQueue()
	h := NewHandler(cfg, q, func(string) {})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/deploy/github", strings.NewReader(""))
	req.Header.Set("X-GitHub-Event", "push")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	// Empty body → invalid JSON → 400.
	if w.Code != http.StatusBadRequest {
		t.Errorf("empty body: expected 400, got %d", w.Code)
	}
}

// --- Unknown GitHub event type ---

func TestHandler_UnknownEvent_ReturnsIgnored(t *testing.T) {
	t.Parallel()

	cfg := testConfig("")
	q, _ := newTestQueue()
	h := NewHandler(cfg, q, func(string) {})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/deploy/github", strings.NewReader(`{}`))
	req.Header.Set("X-GitHub-Event", "create")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("unknown event: expected 200, got %d", w.Code)
	}
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["status"] != "ignored" {
		t.Errorf("unknown event: expected 'ignored', got %q (reason: %q)", resp["status"], resp["reason"])
	}
}

// --- Notify callback is not invoked synchronously by handler ---

func TestHandler_NotifyNotCalledSynchronously(t *testing.T) {
	t.Parallel()

	cfg := testConfig("")
	q, _ := newTestQueue()

	notified := make(chan string, 1)
	h := NewHandler(cfg, q, func(msg string) {
		select {
		case notified <- msg:
		default:
		}
	})

	// The handler itself does NOT call notify — that's the queue worker's job.
	// Verify no spurious call happens synchronously during ServeHTTP.
	body := pushPayload("anatolykoptev/dozor", "refs/heads/main", "abc1234567890")
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/deploy/github", strings.NewReader(body))
	req.Header.Set("X-GitHub-Event", "push")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	select {
	case msg := <-notified:
		t.Errorf("handler should not call notify synchronously, got: %s", msg)
	default:
		// Good — no synchronous notification.
	}
}
