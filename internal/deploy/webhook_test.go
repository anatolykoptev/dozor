package deploy

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func computeSignature(payload, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestVerifySignature(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"test": true}`)
	secret := "test-secret"

	validSig := computeSignature(string(payload), secret)

	tests := []struct {
		name      string
		payload   []byte
		signature string
		secret    string
		want      bool
	}{
		{
			name:      "valid signature",
			payload:   payload,
			signature: validSig,
			secret:    secret,
			want:      true,
		},
		{
			name:      "wrong secret",
			payload:   payload,
			signature: computeSignature(string(payload), "wrong-secret"),
			secret:    secret,
			want:      false,
		},
		{
			name:      "empty signature",
			payload:   payload,
			signature: "",
			secret:    secret,
			want:      false,
		},
		{
			name:      "wrong prefix",
			payload:   payload,
			signature: "sha1=abcdef",
			secret:    secret,
			want:      false,
		},
		{
			name:      "malformed hex",
			payload:   payload,
			signature: "sha256=not-valid-hex!@#",
			secret:    secret,
			want:      false,
		},
		{
			name:      "prefix only",
			payload:   payload,
			signature: "sha256=",
			secret:    secret,
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := verifySignature(tt.payload, tt.signature, tt.secret)
			if got != tt.want {
				t.Errorf("verifySignature() = %v, want %v", got, tt.want)
			}
		})
	}
}

// testQueue is a minimal Queue that tracks Submit calls without a worker.
type testQueue struct{}

func newTestQueue() (*Queue, *testQueue) {
	tq := &testQueue{}
	q := &Queue{
		ch:       make(chan BuildRequest, queueSize),
		queued:   make(map[string]bool),
		building: make(map[string]bool),
	}
	return q, tq
}

func testConfig(secret string) *Config {
	return &Config{
		Secret: secret,
		Repos: map[string]RepoConfig{
			"anatolykoptev/dozor": {
				ComposePath: "/home/krolik/deploy/krolik-server",
				Services:    []string{"dozor"},
				SourcePath:  "/home/krolik/src/dozor",
			},
		},
	}
}

func pushPayload(repo, ref, sha string) string {
	p := pushEvent{}
	p.Ref = ref
	p.Repository.FullName = repo
	p.HeadCommit.ID = sha
	p.HeadCommit.Message = "test commit"
	data, _ := json.Marshal(p)
	return string(data)
}

func TestHandler_Push(t *testing.T) {
	t.Parallel()

	secret := "webhook-secret"
	cfg := testConfig(secret)
	q, _ := newTestQueue()
	h := NewHandler(cfg, q, func(string) {})

	body := pushPayload("anatolykoptev/dozor", "refs/heads/main", "abc1234567890")
	sig := computeSignature(body, secret)

	req := httptest.NewRequest(http.MethodPost, "/deploy/github", strings.NewReader(body))
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-Hub-Signature-256", sig)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["status"] != "queued" {
		t.Errorf("status = %q, want %q", resp["status"], "queued")
	}
	if resp["repo"] != "anatolykoptev/dozor" {
		t.Errorf("repo = %q, want %q", resp["repo"], "anatolykoptev/dozor")
	}
	if resp["commit"] != "abc1234" {
		t.Errorf("commit = %q, want %q", resp["commit"], "abc1234")
	}
}

func TestHandler_NonMainBranch(t *testing.T) {
	t.Parallel()

	cfg := testConfig("")
	q, _ := newTestQueue()
	h := NewHandler(cfg, q, func(string) {})

	body := pushPayload("anatolykoptev/dozor", "refs/heads/feature-x", "abc1234567890")

	req := httptest.NewRequest(http.MethodPost, "/deploy/github", strings.NewReader(body))
	req.Header.Set("X-GitHub-Event", "push")

	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["status"] != "ignored" {
		t.Errorf("status = %q, want %q", resp["status"], "ignored")
	}
	if resp["reason"] != "not main branch" {
		t.Errorf("reason = %q, want %q", resp["reason"], "not main branch")
	}
}

func TestHandler_Ping(t *testing.T) {
	t.Parallel()

	cfg := testConfig("")
	q, _ := newTestQueue()
	h := NewHandler(cfg, q, func(string) {})

	req := httptest.NewRequest(http.MethodPost, "/deploy/github", strings.NewReader("{}"))
	req.Header.Set("X-GitHub-Event", "ping")

	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["status"] != "pong" {
		t.Errorf("status = %q, want %q", resp["status"], "pong")
	}
}

func TestHandler_UnknownRepo(t *testing.T) {
	t.Parallel()

	cfg := testConfig("")
	q, _ := newTestQueue()
	h := NewHandler(cfg, q, func(string) {})

	body := pushPayload("unknown/repo", "refs/heads/main", "abc1234567890")

	req := httptest.NewRequest(http.MethodPost, "/deploy/github", strings.NewReader(body))
	req.Header.Set("X-GitHub-Event", "push")

	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["status"] != "ignored" {
		t.Errorf("status = %q, want %q", resp["status"], "ignored")
	}
	if resp["reason"] != "repo not configured" {
		t.Errorf("reason = %q, want %q", resp["reason"], "repo not configured")
	}
}

func TestHandler_InvalidSignature(t *testing.T) {
	t.Parallel()

	cfg := testConfig("correct-secret")
	q, _ := newTestQueue()
	h := NewHandler(cfg, q, func(string) {})

	body := pushPayload("anatolykoptev/dozor", "refs/heads/main", "abc1234567890")
	wrongSig := computeSignature(body, "wrong-secret")

	req := httptest.NewRequest(http.MethodPost, "/deploy/github", strings.NewReader(body))
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-Hub-Signature-256", wrongSig)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestHandler_InvalidJSON(t *testing.T) {
	t.Parallel()

	cfg := testConfig("")
	q, _ := newTestQueue()
	h := NewHandler(cfg, q, func(string) {})

	req := httptest.NewRequest(http.MethodPost, "/deploy/github",
		strings.NewReader("not json{{{"))
	req.Header.Set("X-GitHub-Event", "push")

	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestHandler_NonPushEvent(t *testing.T) {
	t.Parallel()

	cfg := testConfig("")
	q, _ := newTestQueue()
	h := NewHandler(cfg, q, func(string) {})

	req := httptest.NewRequest(http.MethodPost, "/deploy/github",
		strings.NewReader(`{"action":"opened"}`))
	req.Header.Set("X-GitHub-Event", "issues")

	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["status"] != "ignored" {
		t.Errorf("status = %q, want %q", resp["status"], "ignored")
	}
	if resp["reason"] != "not a push or release event" {
		t.Errorf("reason = %q, want %q", resp["reason"], "not a push event")
	}
}
