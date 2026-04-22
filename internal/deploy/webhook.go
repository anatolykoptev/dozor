package deploy

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"regexp"
)

const maxWebhookBody = 64 * 1024 // 64KB

// pushEvent is the subset of GitHub's push webhook payload we need.
type pushEvent struct {
	Ref        string `json:"ref"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	HeadCommit struct {
		ID      string `json:"id"`
		Message string `json:"message"`
	} `json:"head_commit"`
}

// Handler processes GitHub webhook push events.
type Handler struct {
	config *Config
	queue  *Queue
	notify func(string)
}

// NewHandler creates a GitHub webhook handler.
func NewHandler(config *Config, queue *Queue, notify func(string)) *Handler {
	return &Handler{
		config: config,
		queue:  queue,
		notify: notify,
	}
}

// ServeHTTP handles POST /deploy/github.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBody))
	if err != nil {
		http.Error(w, "read body failed", http.StatusBadRequest)
		return
	}

	// Verify HMAC signature.
	if h.config.Secret != "" {
		// Try both X-Hub-Signature-256 and X-Hub-Signature headers
		sig := r.Header.Get("X-Hub-Signature-256")
		if sig == "" {
			sig = r.Header.Get("X-Hub-Signature")
		}
		if sig == "" {
			slog.Warn("deploy/webhook: missing signature, rejecting",
				"remote", r.RemoteAddr)
			http.Error(w, "missing signature", http.StatusUnauthorized)
			return
		}
		if !verifySignature(body, sig, h.config.Secret) {
			slog.Warn("deploy/webhook: invalid signature, rejecting",
				"remote", r.RemoteAddr,
				"signature_header", r.Header.Get("X-Hub-Signature-256"),
				"legacy_header", r.Header.Get("X-Hub-Signature"))
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}
	}

	// Handle ping event.
	event := r.Header.Get("X-GitHub-Event")
	if event == "ping" {
		respondJSON(w, http.StatusOK, map[string]string{
			"status": "pong",
		})
		return
	}

	var push pushEvent

	// Process push and release events.
	switch event {
	case "release":
		var release struct {
			Action  string `json:"action"`
			Release struct {
				TagName         string `json:"tag_name"`
				TargetCommitish string `json:"target_commitish"`
			} `json:"release"`
			Repository struct {
				FullName string `json:"full_name"`
			} `json:"repository"`
		}
		if err := json.Unmarshal(body, &release); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}

		// Only deploy published releases with semantic version tags
		if release.Action != "published" {
			respondJSON(w, http.StatusOK, map[string]string{
				"status": "ignored",
				"reason": "not a published release",
			})
			return
		}

		// Check for semantic version pattern (v1.0.0, v2.1.3, etc.)
		if !matchesSemVer(release.Release.TagName) {
			respondJSON(w, http.StatusOK, map[string]string{
				"status": "ignored",
				"reason": "tag does not match semantic version pattern",
			})
			return
		}

		// Use release tag as "commit" for deploy tracking
		push = pushEvent{
			Ref: "refs/tags/" + release.Release.TagName,
			Repository: struct {
				FullName string `json:"full_name"`
			}{FullName: release.Repository.FullName},
			HeadCommit: struct {
				ID      string `json:"id"`
				Message string `json:"message"`
			}{
				ID:      release.Release.TargetCommitish,
				Message: "Release " + release.Release.TagName,
			},
		}
	case "push":
		if err := json.Unmarshal(body, &push); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
	default:
		respondJSON(w, http.StatusOK, map[string]string{
			"status": "ignored",
			"reason": "not a push or release event",
		})
		return
	}
	// For push events, only main branch. For releases, we already validated.
	if event == "push" && push.Ref != "refs/heads/main" {
		respondJSON(w, http.StatusOK, map[string]string{
			"status": "ignored",
			"reason": "not main branch",
		})
		return
	}

	// Lookup repo config.
	rc := h.config.Lookup(push.Repository.FullName)
	if rc == nil {
		slog.Info("deploy/webhook: unknown repo",
			"repo", push.Repository.FullName)
		respondJSON(w, http.StatusOK, map[string]string{
			"status": "ignored",
			"reason": "repo not configured",
		})
		return
	}

	// Submit to build queue (async).
	submitted := h.queue.Submit(BuildRequest{
		Repo:      push.Repository.FullName,
		CommitSHA: push.HeadCommit.ID,
		Config:    *rc,
	})

	status := "queued"
	if !submitted {
		status = "deduplicated"
	}

	slog.Info("deploy/webhook: processed",
		"repo", push.Repository.FullName,
		"commit", short(push.HeadCommit.ID),
		"status", status,
	)

	respondJSON(w, http.StatusOK, map[string]string{
		"status": status,
		"repo":   push.Repository.FullName,
		"commit": short(push.HeadCommit.ID),
	})
}

// verifySignature checks the HMAC-SHA256 signature from GitHub.
func verifySignature(payload []byte, signature, secret string) bool {
	if signature == "" {
		return false
	}

	const prefix = "sha256="
	if len(signature) <= len(prefix) {
		return false
	}
	sigHex := signature[len(prefix):]

	sigBytes, err := hex.DecodeString(sigHex)
	if err != nil {
		return false
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	expected := mac.Sum(nil)

	return hmac.Equal(sigBytes, expected)
}

// matchesSemVer checks if a tag matches semantic version pattern (v1.0.0, v2.1.3, etc.)
func matchesSemVer(tag string) bool {
	// Pattern: v1.0.0, v1.2.3, v10.20.30, etc.
	matched, _ := regexp.MatchString(`^v\d+\.\d+\.\d+$`, tag)
	return matched
}

func respondJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data) //nolint:errcheck
}
