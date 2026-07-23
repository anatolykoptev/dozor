package deploy

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
)

// maxWebhookBody bounds the request body. Push events with many commits and
// large file lists can grow well beyond the original 64 KB cap, so we allow up
// to 5 MB. GitHub itself caps webhook payloads at ~25 MB.
const maxWebhookBody = 5 * 1024 * 1024 // 5 MiB

// pushEvent is the subset of GitHub's push webhook payload we need.
type pushEvent struct {
	Ref        string `json:"ref"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	Pusher struct {
		Name string `json:"name"`
	} `json:"pusher"`
	HeadCommit struct {
		ID      string `json:"id"`
		Message string `json:"message"`
	} `json:"head_commit"`
	// Commits carry the per-commit changed-file lists used by the path
	// filter. GitHub omits these fields for force pushes / very large pushes,
	// in which case the filter is bypassed (build proceeds as before).
	Commits []struct {
		ID       string   `json:"id"`
		Added    []string `json:"added"`
		Removed  []string `json:"removed"`
		Modified []string `json:"modified"`
	} `json:"commits"`
}

// Handler processes GitHub webhook push events.
type Handler struct {
	config    *Config
	queue     *Queue
	notify    func(string)
	debouncer *Debouncer
	checker   *prLabelChecker
	// shaResolver resolves the currently-deployed short SHA for a repo, given
	// its build/source directory. Used only by the release-event path
	// (webhook_release.go) to compute a real changed-files diff, since a
	// GitHub "release published" payload carries no per-commit file list.
	// Defaults to resolveGitSHA — the same primitive the debounce-persistence
	// layer (debounce_persist.go) uses to detect a stale rebuild. Swappable in
	// tests.
	shaResolver shaResolverFunc
}

// NewHandler creates a GitHub webhook handler. The handler unconditionally
// uses a Debouncer; per-repo dispatch falls back to immediate Submit when the
// configured debounce window is zero.
func NewHandler(config *Config, queue *Queue, notify func(string)) *Handler {
	h := &Handler{
		config:      config,
		queue:       queue,
		notify:      notify,
		checker:     newPRLabelChecker(config.GitHubToken),
		shaResolver: resolveGitSHA,
	}
	if config.GitHubToken == "" {
		slog.Warn("DOZOR_GITHUB_TOKEN not set; PR label check disabled (marker-only mode)")
	}
	h.debouncer = NewDebouncer(nil, h.dispatch)
	// Durable debounce: mirror the pending set to ~/.dozor/deploy-debounce.json
	// so queued builds survive a dozor restart (graceful self-deploy or crash).
	// See debounce_persist.go (VOLATILE-PENDING-STATE fix).
	h.debouncer.WithPersistence(DefaultDebouncePersistPath())
	return h
}

// RecoverPending replays any debounce entries persisted by a previous dozor
// process: re-arming timers for builds still within their window, firing those
// whose window already elapsed during downtime, and skipping any whose commit
// is already the deployed HEAD. Call once at boot, before serving webhooks.
// Tolerant of a missing or corrupt state file (logs + continues).
func (h *Handler) RecoverPending(ctx context.Context) {
	if h.debouncer == nil {
		return
	}
	if err := h.debouncer.Reload(ctx); err != nil {
		slog.Warn("deploy: debounce reload failed", "error", err)
	}
}

// Close releases the handler's debouncer goroutines. Safe to call once.
func (h *Handler) Close() {
	if h.debouncer != nil {
		h.debouncer.Stop(nil)
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
	// Find ALL config entries matching (repo, branch). A monorepo can declare
	// several independent deploy targets for one repo (keyed "owner/repo#suffix");
	// each is dispatched separately and gated by its own BuildPaths filter, so a
	// push that only touches one app's paths deploys only that target. A single-
	// target repo yields one match, identical to the previous single-lookup path.
	// For releases, look up by repo only (no branch concept for tags).
	var matches []*RepoConfig
	if event == "push" {
		// Extract short branch name from "refs/heads/<branch>".
		const headsPrefix = "refs/heads/"
		if !strings.HasPrefix(push.Ref, headsPrefix) {
			// Non-branch ref (e.g. refs/tags/* on a push event) — ignore.
			respondJSON(w, http.StatusOK, map[string]string{
				"status": "ignored",
				"reason": "not a branch push",
			})
			return
		}
		branch := push.Ref[len(headsPrefix):]
		matches = h.config.LookupAll(push.Repository.FullName, branch)
		if len(matches) == 0 {
			// No config entry matches this (repo, branch) pair.
			slog.Debug("deploy/webhook: no config for repo+branch",
				"repo", push.Repository.FullName,
				"branch", branch,
				"pusher", push.Pusher.Name)
			respondJSON(w, http.StatusOK, map[string]string{
				"status": "ignored",
				"reason": "no deploy config for branch " + branch,
			})
			return
		}
		// Filter out deploy_on=release targets: those build ONLY on a
		// GitHub release event, not on every push. Per-match (not
		// all-or-nothing) so a monorepo with mixed targets still builds
		// the push-based ones. If every matched target is release-only,
		// respond ignored — the release event will ship them.
		filtered := matches[:0]
		for _, rc := range matches {
			if rc.DeployOn == "release" {
				slog.Info("deploy/webhook: deploy_on=release, skipping push, waiting for release event",
					"repo", push.Repository.FullName,
					"branch", branch,
				)
				continue
			}
			filtered = append(filtered, rc)
		}
		matches = filtered
		if len(matches) == 0 {
			respondJSON(w, http.StatusOK, map[string]string{
				"status": "ignored",
				"reason": "all matched targets are deploy_on: release",
			})
			return
		}
	} else {
		// release event: route to the target CONFIGURED to deploy on releases
		// (deploy_on: release), not a random first-match. A tag carries no
		// changed files to gate per-target fan-out, so a release builds a
		// single target (fan-out is a push-only concept) — but for a repo with
		// both a release target (prod/main) and a push target (canary/dev,
		// keyed "owner/repo#dev"), an unordered first-match could route the
		// release to the canary, whose source clone lacks the release commit
		// ("git worktree add: invalid reference"). LookupReleaseTarget selects
		// the deploy_on: release entry deterministically, falling back to
		// first-match for single-target repos (unchanged behaviour).
		rc := h.config.LookupReleaseTarget(push.Repository.FullName)
		if rc == nil {
			slog.Info("deploy/webhook: unknown repo",
				"repo", push.Repository.FullName)
			respondJSON(w, http.StatusOK, map[string]string{
				"status": "ignored",
				"reason": "repo not configured",
			})
			return
		}
		// A release payload carries no per-commit changed-files list (unlike
		// push's commits[].added/modified/removed) — resolve a real diff
		// against the last-deployed SHA so skipByPathFilter's BuildPaths/
		// SkipPaths gating applies to releases exactly as it does to pushes,
		// instead of unconditionally building via its "elided diff" fallback.
		attachReleaseDiff(r.Context(), &push, rc, h.shaResolver)
		matches = []*RepoConfig{rc}
	}

	// Single-target repo (the overwhelming common case): preserve the original
	// response contract exactly, including the skip "reason" field that tooling
	// and tests key on. Multi-target monorepos fall through to the aggregating
	// path below.
	if len(matches) == 1 {
		rc := matches[0]
		if h.skipByPathFilter(push, rc) {
			slog.Info("deploy skipped: no build-relevant files changed",
				"repo", push.Repository.FullName,
				"commit", short(push.HeadCommit.ID),
				"build_paths", rc.BuildPaths,
			)
			respondJSON(w, http.StatusOK, map[string]string{
				"status": "skipped",
				"reason": "no_relevant_paths",
				"repo":   push.Repository.FullName,
				"commit": short(push.HeadCommit.ID),
			})
			return
		}
		status := h.dispatchPush(push, rc)
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
		return
	}

	// Multi-target monorepo: dispatch each matching target independently. Each
	// is gated by its own BuildPaths filter; targets keyed distinctly (services
	// / branch) debounce and queue independently (see dispatchPush). Statuses
	// are aggregated in deterministic match order.
	statuses := make([]string, 0, len(matches))
	for _, rc := range matches {
		if h.skipByPathFilter(push, rc) {
			slog.Info("deploy skipped: no build-relevant files changed",
				"repo", push.Repository.FullName,
				"commit", short(push.HeadCommit.ID),
				"build_paths", rc.BuildPaths,
			)
			statuses = append(statuses, "skipped")
			continue
		}

		status := h.dispatchPush(push, rc)
		slog.Info("deploy/webhook: processed",
			"repo", push.Repository.FullName,
			"commit", short(push.HeadCommit.ID),
			"status", status,
		)
		statuses = append(statuses, status)
	}

	respondJSON(w, http.StatusOK, map[string]string{
		"status": strings.Join(statuses, ","),
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
