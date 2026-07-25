package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

// Drift check outcome constants (label values for dozor_config_drift).
const (
	outcomeOK               = "ok"
	outcomeDrift            = "drift"
	outcomeNoToken          = "no_token"
	outcomeAPIError         = "api_error"
	outcomeNoWebhook        = "no_webhook"
	outcomeDisabled         = "disabled"
	outcomeMirrorUnreadable = "mirror_unreadable"
)

// Drift check type constants (label values for dozor_config_drift).
const (
	checkConfigMirror  = "config_mirror"
	checkWebhookEvents = "webhook_events"
)

// driftAPITimeout caps each GitHub API call during the webhook-events drift
// check. The check observes only and must never block a deploy, so a 10 s
// ceiling is generous while bounding tail latency on a flaky network.
const driftAPITimeout = 10 * time.Second

// driftCheckInterval is how often the periodic drift check runs. 10 minutes
// is frequent enough to surface a drift the same morning an operator
// introduces it (a webhook subscription change or a config edit), but gentle
// on the GitHub API rate limit: list-webhooks is 1 call per repo, so ~15 repos
// = 90 calls/hr, well within the 5000/hr authenticated budget. Drift is a
// slow-moving human error, not a per-event concern.
const driftCheckInterval = 10 * time.Minute

// dozorWebhookPathFragment identifies a GitHub webhook as dozor's: dozor
// registers POST /deploy/github (cmd/dozor/gateway.go), so a hook whose
// config.url contains this fragment is the one whose event subscription we
// must verify.
const dozorWebhookPathFragment = "/deploy/github"

// GitHub webhook event names that deploy_on maps to.
const (
	eventPush    = "push"
	eventRelease = "release"
)

// DriftChecker detects two classes of silent config drift:
//
//  1. Config-mirror drift: the live config file (~/.dozor/deploy-repos.yaml)
//     diverges from its version-controlled mirror (DOZOR_CONFIG_GIT_MIRROR).
//     Two copies are kept in sync by hand; nothing detects when they diverge.
//
//  2. Webhook-event drift: a repo's deploy_on setting requires a GitHub webhook
//     event that the repo's dozor webhook is not subscribed to. go-hully had
//     deploy_on: release but its webhook was subscribed to [push] only —
//     release events never arrived and the repo never deployed, with no error
//     anywhere, for months.
//
// The checker OBSERVES only: it logs and sets a Prometheus gauge. It never
// blocks or fails a deploy, never auto-mutates webhooks, and never auto-copies
// the config file.
type DriftChecker struct {
	cfg        *Config
	livePath   string
	mirrorPath string // empty = config-mirror check disabled
	token      string
	apiBase    string // overridable for tests; empty = "https://api.github.com"
	client     *http.Client
}

// NewDriftChecker builds a checker from the given config and live config path.
// The git-mirror path is read from DOZOR_CONFIG_GIT_MIRROR (empty = check
// disabled). The GitHub token is read from cfg.GitHubToken.
func NewDriftChecker(cfg *Config, livePath string) *DriftChecker {
	return &DriftChecker{
		cfg:        cfg,
		livePath:   livePath,
		mirrorPath: os.Getenv("DOZOR_CONFIG_GIT_MIRROR"),
		token:      cfg.GitHubToken,
		client:     &http.Client{Timeout: driftAPITimeout},
	}
}

// Run executes both drift checks, logs findings, and updates the
// dozor_config_drift gauge. Safe to call periodically. Never panics: a
// deferred recover ensures a bug in the check cannot crash the gateway.
func (d *DriftChecker) Run(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("drift check panicked", slog.Any("panic", r))
		}
	}()
	d.setGauge("", checkConfigMirror, d.checkConfigMirror())
	d.checkWebhookEvents(ctx)
}

// --- drift #1: config mirror ---

// checkConfigMirror compares the live config file against the git-mirror copy
// (DOZOR_CONFIG_GIT_MIRROR). Absent/unreadable mirror is NOT drift — the check
// is skipped with a distinct outcome. Only a readable mirror that differs from
// the live copy is drift. Returns the outcome string (see outcome constants).
func (d *DriftChecker) checkConfigMirror() string {
	if d.mirrorPath == "" {
		return outcomeDisabled
	}

	mirrorData, err := os.ReadFile(d.mirrorPath)
	if err != nil {
		// Absent/unreadable mirror must NOT be reported as drift.
		slog.Warn("drift: config mirror unreadable — check skipped (NOT drift)",
			"mirror_path", d.mirrorPath, "error", err)
		return outcomeMirrorUnreadable
	}

	liveData, err := os.ReadFile(d.livePath)
	if err != nil {
		// The live file was readable at startup (LoadConfig succeeded); if it
		// vanished mid-run this is an operational error, not drift.
		slog.Warn("drift: live config unreadable — check skipped (NOT drift)",
			"live_path", d.livePath, "error", err)
		return outcomeMirrorUnreadable
	}

	if bytes.Equal(liveData, mirrorData) {
		return outcomeOK
	}

	slog.Error("drift: live config differs from git mirror — a reviewed config change may be sitting in git doing nothing (or the live copy was hand-edited and never committed)",
		"live_path", d.livePath,
		"mirror_path", d.mirrorPath,
		"live_bytes", len(liveData),
		"mirror_bytes", len(mirrorData),
	)
	return outcomeDrift
}

// --- drift #2: webhook events vs deploy_on ---

// checkWebhookEvents verifies, for each configured repo, that the repo's
// dozor-pointing GitHub webhook is subscribed to the events its deploy_on
// requires. Each failure mode is a DISTINCT outcome, never silently folded
// into "ok".
func (d *DriftChecker) checkWebhookEvents(ctx context.Context) {
	if d.token == "" {
		// No token: cannot call the API. This is distinct from "ok" — surface
		// it once (not per-repo) and mark every repo with the no_token outcome
		// so a dashboard sees the check is blind, not green.
		slog.Warn("drift: webhook-event check skipped — DOZOR_GITHUB_TOKEN not set (cannot read webhook subscriptions)")
		for _, repoKey := range d.sortedRepoKeys() {
			d.setGauge(repoKey, checkWebhookEvents, outcomeNoToken)
		}
		return
	}

	requiredByRepo := d.requiredEventsByRepo()
	for _, repoKey := range d.sortedRepoKeys() {
		required := requiredByRepo[repoKey]
		outcome := d.checkOneRepoWebhook(ctx, repoKey, required)
		d.setGauge(repoKey, checkWebhookEvents, outcome.outcome)
		switch outcome.outcome {
		case outcomeDrift:
			slog.Error("drift: webhook events do not cover deploy_on — repo will not deploy on the missing event(s)",
				"repo", repoKey,
				"required_events", sortedEvents(required),
				"subscribed_events", outcome.subscribed,
				"missing_events", outcome.missing,
			)
		case outcomeNoWebhook:
			slog.Error("drift: repo has no dozor webhook — no GitHub webhook points at /deploy/github, repo will never deploy",
				"repo", repoKey,
			)
		}
	}
}

// webhookCheckResult holds the outcome of checking one repo's webhook events.
type webhookCheckResult struct {
	outcome    string
	subscribed []string // events the dozor webhook(s) are subscribed to
	missing    []string // required events not covered by the subscription
}

// checkOneRepoWebhook queries the GitHub API for the repo's webhooks, finds
// the dozor-pointing one(s), and compares their event subscription against the
// required set.
func (d *DriftChecker) checkOneRepoWebhook(ctx context.Context, repoKey string, required []string) webhookCheckResult {
	hooks, ok := d.listRepoHooks(ctx, repoKey)
	if !ok {
		return webhookCheckResult{outcome: outcomeAPIError}
	}

	subscribed := dozorWebhookEvents(hooks)
	if len(subscribed) == 0 {
		return webhookCheckResult{outcome: outcomeNoWebhook}
	}

	subSet := make(map[string]struct{}, len(subscribed))
	for _, e := range subscribed {
		subSet[e] = struct{}{}
	}
	var missing []string
	for _, need := range required {
		if _, covered := subSet[need]; !covered {
			missing = append(missing, need)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return webhookCheckResult{
			outcome:    outcomeDrift,
			subscribed: sortedEvents(subscribed),
			missing:    missing,
		}
	}
	return webhookCheckResult{
		outcome:    outcomeOK,
		subscribed: sortedEvents(subscribed),
	}
}

// githubHook is the subset of GitHub's webhook listing response we need.
type githubHook struct {
	Events []string `json:"events"`
	Config struct {
		URL string `json:"url"`
	} `json:"config"`
}

// listRepoHooks calls GET /repos/{owner}/{repo}/hooks and returns the list.
// Returns (nil, false) on any API error (network, non-200, decode) — the
// caller surfaces this as the distinct "api_error" outcome.
func (d *DriftChecker) listRepoHooks(ctx context.Context, repoKey string) ([]githubHook, bool) {
	base := d.apiBase
	if base == "" {
		base = "https://api.github.com"
	}
	url := fmt.Sprintf("%s/repos/%s/hooks", base, repoKey)

	reqCtx, cancel := context.WithTimeout(ctx, driftAPITimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		slog.Warn("drift: webhook list request build failed", "repo", repoKey, "err", err)
		return nil, false
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Authorization", "Bearer "+d.token)

	resp, err := d.client.Do(req)
	if err != nil {
		slog.Warn("drift: webhook list API call failed", "repo", repoKey, "err", err)
		return nil, false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		slog.Warn("drift: webhook list API non-200", "repo", repoKey, "status", resp.StatusCode)
		return nil, false
	}

	var hooks []githubHook
	if err := json.NewDecoder(resp.Body).Decode(&hooks); err != nil {
		slog.Warn("drift: webhook list decode failed", "repo", repoKey, "err", err)
		return nil, false
	}
	return hooks, true
}

// dozorWebhookEvents returns the union of events from all hooks whose config
// URL points at dozor's /deploy/github endpoint. If a repo has multiple
// dozor-pointing hooks (e.g. an old + a new URL), the union is conservative —
// as long as ANY of them carries the required event, the repo is covered.
func dozorWebhookEvents(hooks []githubHook) []string {
	seen := make(map[string]struct{})
	for _, h := range hooks {
		if !strings.Contains(h.Config.URL, dozorWebhookPathFragment) {
			continue
		}
		for _, e := range h.Events {
			seen[e] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for e := range seen {
		out = append(out, e)
	}
	return out
}

// requiredEventsByRepo aggregates, per repoKey (owner/repo with #suffix
// stripped), the set of GitHub webhook events that deploy_on requires across
// ALL entries for that repo. A monorepo may have one entry with deploy_on=""
// (needs push) and another with deploy_on="release" (needs release); the
// webhook must cover both.
func (d *DriftChecker) requiredEventsByRepo() map[string][]string {
	result := make(map[string][]string, len(d.cfg.Repos))
	for key, rc := range d.cfg.Repos {
		repoKey := stripBranchSuffix(key)
		var need string
		switch rc.DeployOn {
		case eventRelease:
			need = eventRelease
		default: // "" or any other value (validated at load to be "" or "release")
			need = eventPush
		}
		result[repoKey] = appendUnique(result[repoKey], need)
	}
	return result
}

// sortedRepoKeys returns the deduplicated, sorted repoKeys (owner/repo with
// #suffix stripped) from the config. Deterministic order for stable logging.
func (d *DriftChecker) sortedRepoKeys() []string {
	seen := make(map[string]struct{})
	for key := range d.cfg.Repos {
		seen[stripBranchSuffix(key)] = struct{}{}
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// stripBranchSuffix removes a trailing "#<suffix>" from a config map key,
// returning the bare owner/repo identifier.
func stripBranchSuffix(key string) string {
	if idx := strings.LastIndex(key, "#"); idx >= 0 {
		return key[:idx]
	}
	return key
}

// appendUnique appends s to slice only if not already present.
func appendUnique(slice []string, s string) []string {
	for _, v := range slice {
		if v == s {
			return slice
		}
	}
	return append(slice, s)
}

// sortedEvents returns a sorted copy of the given event slice (may be nil).
func sortedEvents(events []string) []string {
	if len(events) == 0 {
		return nil
	}
	cp := append([]string(nil), events...)
	sort.Strings(cp)
	return cp
}

// setGauge sets the dozor_config_drift state-enum gauge: the current outcome
// to 1 and all other known outcomes for that check to 0. This avoids stale 1s
// when the outcome transitions (e.g. drift → ok).
func (d *DriftChecker) setGauge(repo, check, outcome string) {
	var allOutcomes []string
	switch check {
	case checkConfigMirror:
		allOutcomes = []string{outcomeDisabled, outcomeMirrorUnreadable, outcomeOK, outcomeDrift}
	case checkWebhookEvents:
		allOutcomes = []string{outcomeOK, outcomeDrift, outcomeNoToken, outcomeAPIError, outcomeNoWebhook}
	}
	for _, o := range allOutcomes {
		var v float64
		if o == outcome {
			v = 1
		}
		ConfigDrift.WithLabelValues(repo, check, o).Set(v)
	}
}

// StartDriftCheckLoop runs the drift check immediately (startup) and then
// every driftCheckInterval until ctx is cancelled. Blocks in its own goroutine
// — call with `go StartDriftCheckLoop(...)`. Never blocks or fails a deploy.
func StartDriftCheckLoop(ctx context.Context, d *DriftChecker) {
	d.Run(ctx)
	ticker := time.NewTicker(driftCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.Run(ctx)
		}
	}
}
