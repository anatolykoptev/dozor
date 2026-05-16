package deploy

import (
	"container/list"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"sync"
	"time"
)

// noDeployMarkerRE matches [no-deploy], [no-auto-deploy], and common
// case/separator variants anywhere in a commit message.
// Brackets are required — bare "no-deploy" does not match (avoids false
// positives in investigation notes like "checking no-deploy regression").
// noDeployMarkerRE matches bracket-delimited no-deploy markers, case-insensitive,
// anywhere in a commit message. Brackets are required to avoid false positives
// (bare "nodeploy" in prose does not match).
//
// Recognised forms (separators dash/underscore are optional between segments):
//   - [no-deploy]  [no_deploy]  [nodeploy]
//   - [no-auto-deploy]  [no_auto_deploy]  [no-autodeploy]  [NoAutoDeploy]
var noDeployMarkerRE = regexp.MustCompile(`(?i)\[no[-_]?(auto[-_]?)?deploy\]`)

const (
	// prLabelCacheSize is the maximum number of SHA → skip decisions held in
	// the LRU cache. 256 covers many hours of webhook bursts without meaningful
	// memory cost (~few KB).
	prLabelCacheSize = 256

	// prLabelCacheTTL is how long a cached decision is considered fresh.
	// 10 minutes is enough to deduplicate debounce-coalesced bursts while
	// not leaking stale "skip" decisions after a label is removed.
	prLabelCacheTTL = 10 * time.Minute

	// prLabelAPITimeout caps each GitHub API call. Fail-open on timeout.
	prLabelAPITimeout = 2 * time.Second

	// noAutoDeployLabel is the GitHub PR label that suppresses auto-deploy.
	noAutoDeployLabel = "no-auto-deploy"
)

// prLabelChecker is a thread-safe cache + HTTP client that answers whether a
// given push commit should be skipped due to a no-auto-deploy PR label or
// commit message marker.
//
// Decisions are cached by commit SHA with a 10-minute TTL using an LRU
// strategy, so debounce-coalesced bursts never fan out duplicate API calls.
type prLabelChecker struct {
	token   string
	apiBase string // overridable for tests; empty = "https://api.github.com"
	client  *http.Client

	mu    sync.Mutex
	cache map[string]*list.Element // sha → list element containing *cacheEntry
	lru   *list.List
}

type cacheEntry struct {
	sha    string
	skip   bool
	expiry time.Time
}

// newPRLabelChecker returns a checker configured with the given GitHub token.
// Pass empty string to operate in marker-only mode (API calls are skipped).
func newPRLabelChecker(token string) *prLabelChecker {
	return &prLabelChecker{
		token:  token,
		client: &http.Client{Timeout: prLabelAPITimeout},
		cache:  make(map[string]*list.Element),
		lru:    list.New(),
	}
}

// ShouldSkip returns true if the deploy should be skipped.
//
// Decision order:
//  1. Commit message contains a [no-deploy] / [no-auto-deploy] marker
//     (synchronous, zero network cost).
//  2. GitHub API: any PR associated with this SHA has the "no-auto-deploy"
//     label (single HTTP call, LRU-cached, 2 s timeout).
//
// Fail-OPEN: any error (network, timeout, non-200) returns false so that GH
// outages or misconfigured tokens never block deploys.
func (c *prLabelChecker) ShouldSkip(ctx context.Context, repo, sha, commitMsg string) bool {
	// Fast path: marker in commit message requires no network.
	if noDeployMarkerRE.MatchString(commitMsg) {
		slog.Info("deploy skip: commit message marker", "repo", repo, "sha", sha[:min(7, len(sha))])
		return true
	}

	// Without a token we cannot call the API; marker-only mode.
	if c.token == "" {
		return false
	}

	// Cache lookup (positive and negative results cached equally).
	if skip, ok := c.lookupCache(sha); ok {
		return skip
	}

	// API call — fail-open on any error.
	skip := c.checkPRLabelsAPI(ctx, repo, sha)
	c.storeCache(sha, skip)
	return skip
}

// lookupCache returns the cached decision and whether a valid (non-expired)
// entry exists.
func (c *prLabelChecker) lookupCache(sha string) (skip bool, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, exists := c.cache[sha]
	if !exists {
		return false, false
	}
	entry := el.Value.(*cacheEntry)
	if time.Now().After(entry.expiry) {
		c.lru.Remove(el)
		delete(c.cache, sha)
		return false, false
	}
	c.lru.MoveToFront(el)
	return entry.skip, true
}

// storeCache records a skip decision keyed by SHA and evicts LRU entries if
// the cache exceeds prLabelCacheSize.
func (c *prLabelChecker) storeCache(sha string, skip bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := &cacheEntry{
		sha:    sha,
		skip:   skip,
		expiry: time.Now().Add(prLabelCacheTTL),
	}
	el := c.lru.PushFront(entry)
	c.cache[sha] = el

	// Evict oldest entries when over capacity.
	for c.lru.Len() > prLabelCacheSize {
		oldest := c.lru.Back()
		if oldest == nil {
			break
		}
		old := oldest.Value.(*cacheEntry)
		delete(c.cache, old.sha)
		c.lru.Remove(oldest)
	}
}

// checkPRLabelsAPI calls the GitHub REST API to list PRs associated with the
// given commit SHA and returns true if any has the "no-auto-deploy" label.
// Returns false on any error (fail-open).
func (c *prLabelChecker) checkPRLabelsAPI(ctx context.Context, repo, sha string) bool {
	base := c.apiBase
	if base == "" {
		base = "https://api.github.com"
	}
	url := fmt.Sprintf("%s/repos/%s/commits/%s/pulls", base, repo, sha)

	reqCtx, cancel := context.WithTimeout(ctx, prLabelAPITimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		slog.Warn("prLabelChecker: request build failed", "repo", repo, "sha", sha, "err", err)
		return false
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.client.Do(req)
	if err != nil {
		slog.Warn("prLabelChecker: api call failed", "repo", repo, "sha", sha, "err", err)
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		slog.Warn("prLabelChecker: api non-200", "repo", repo, "sha", sha, "status", resp.StatusCode)
		return false
	}

	var prs []struct {
		Number int `json:"number"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&prs); err != nil {
		slog.Warn("prLabelChecker: decode failed", "repo", repo, "sha", sha, "err", err)
		return false
	}

	for _, pr := range prs {
		for _, lbl := range pr.Labels {
			if lbl.Name == noAutoDeployLabel {
				slog.Info("prLabelChecker: skip via PR label",
					"repo", repo,
					"sha", sha[:min(7, len(sha))],
					"pr", pr.Number,
				)
				return true
			}
		}
	}
	return false
}

