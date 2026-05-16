package deploy

import (
	"context"
	"log/slog"
)

// This file isolates the post-validation routing logic of the GitHub webhook
// handler — path filtering, debounce dispatch, and queue submission. ServeHTTP
// (in webhook.go) handles HMAC + event classification only.

// changedFiles returns the union of added/removed/modified files across every
// commit in the push. Returns nil if GitHub did not include any per-commit
// diffs (force push or oversized push).
func (p pushEvent) changedFiles() []string {
	if len(p.Commits) == 0 {
		return nil
	}
	seen := make(map[string]struct{})
	for _, c := range p.Commits {
		for _, f := range c.Added {
			seen[f] = struct{}{}
		}
		for _, f := range c.Removed {
			seen[f] = struct{}{}
		}
		for _, f := range c.Modified {
			seen[f] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for f := range seen {
		out = append(out, f)
	}
	return out
}

// skipByPathFilter reports whether this push should be skipped due to the
// BuildPaths whitelist or SkipPaths deny-list. Returns false (i.e. proceed
// to build) when:
//   - BuildPaths is empty (feature disabled — SkipPaths also ignored)
//   - the push has no per-commit file list (force push / oversize)
//   - at least one non-skip changed file matches the whitelist
//
// Filter order (when BuildPaths non-empty):
//  1. Subtract changed files that match SkipPaths — these are treated as
//     "not deploy-worthy" regardless of whether they also match BuildPaths
//     (skip-list wins on overlap). Operator intent: "even if Cargo.toml
//     matches build_paths, ignore changes under target/**".
//  2. If nothing remains → skip{reason="only_skip_paths"}.
//  3. Else if remaining files don't match BuildPaths → skip{reason="no_relevant_paths"}.
//
// On skip, increments dozor_deploy_skipped_total{reason=<above>}.
func (h *Handler) skipByPathFilter(push pushEvent, rc *RepoConfig) bool {
	if len(rc.BuildPaths) == 0 {
		return false
	}
	changed := push.changedFiles()
	if len(changed) == 0 {
		// GitHub elided the diff — be conservative and build.
		return false
	}

	relevant := changed
	if len(rc.SkipPaths) > 0 {
		relevant = make([]string, 0, len(changed))
		for _, f := range changed {
			if !MatchPath(f, rc.SkipPaths) {
				relevant = append(relevant, f)
			}
		}
		if len(relevant) == 0 {
			for _, svc := range rc.Services {
				SkippedTotal.WithLabelValues(push.Repository.FullName, svc, "only_skip_paths").Inc()
			}
			return true
		}
	}

	if MatchAny(relevant, rc.BuildPaths) {
		return false
	}
	for _, svc := range rc.Services {
		SkippedTotal.WithLabelValues(push.Repository.FullName, svc, "no_relevant_paths").Inc()
	}
	return true
}

// dispatchPush hands the (filtered) push event off to either the debouncer or
// directly to the build queue, returning the status string included in the
// HTTP response ("queued", "deduplicated", or "debounced").
//
// Skip-debounce optimisation: when a build is already active or pending for
// this service group, bypass the debounce window entirely and submit directly
// to the queue. The queue's newest-wins dedup collapses it correctly, and
// skipping debounce eliminates 30–60 s of unnecessary latency.
func (h *Handler) dispatchPush(push pushEvent, rc *RepoConfig) string {
	// no-auto-deploy check: skip deploy when commit message marker is present
	// or when any associated PR has the "no-auto-deploy" label.
	// Fail-open: errors in the label API call return false (deploy proceeds).
	if h.checker != nil && !rc.IgnoreNoAutoDeployLabel {
		if h.checker.ShouldSkip(
			context.Background(),
			push.Repository.FullName,
			push.HeadCommit.ID,
			push.HeadCommit.Message,
		) {
			for _, svc := range rc.Services {
				SkippedTotal.WithLabelValues(push.Repository.FullName, svc, "no_auto_deploy").Inc()
			}
			slog.Info("deploy skipped: no-auto-deploy",
				"repo", push.Repository.FullName,
				"commit", short(push.HeadCommit.ID),
			)
			return "skipped_no_auto_deploy"
		}
	}

	if window := rc.DebounceWindow(); window > 0 && h.debouncer != nil {
		// Use the queue's key (serviceKey only, no repo prefix) to match what
		// the queue tracks internally in busySHA/pending.
		queueKey := serviceKey(rc.Services)
		if h.queue.IsActiveOrPending(queueKey) {
			// Build already active/pending: debounce window adds pure latency here.
			// Go straight to queue; newest-wins dedup handles duplicate SHAs.
			slog.Info("deploy debounced: skipping debounce (build active/pending)",
				"repo", push.Repository.FullName,
				"services", rc.Services,
				"commit", short(push.HeadCommit.ID),
			)
		} else {
			// Key includes repo + sorted services so two services in the same
			// repo debounce independently.
			debounceKey := push.Repository.FullName + "#" + queueKey
			svcLabel := ""
			if len(rc.Services) > 0 {
				svcLabel = rc.Services[0]
			}
			h.debouncer.Submit(debounceKey, PendingEvent{
				Repo:      push.Repository.FullName,
				Service:   svcLabel,
				CommitSHA: push.HeadCommit.ID,
				Config:    *rc,
			}, window)
			return "debounced"
		}
	}

	// Immediate dispatch — original behaviour (or skip-debounce fast path).
	for _, svc := range rc.Services {
		FiredTotal.WithLabelValues(push.Repository.FullName, svc).Inc()
	}
	if !h.queue.Submit(BuildRequest{
		Repo:      push.Repository.FullName,
		CommitSHA: push.HeadCommit.ID,
		Config:    *rc,
	}) {
		return "deduplicated"
	}
	return "queued"
}

// dispatch is the debouncer callback — pushes the coalesced event into the
// build queue using HEAD at fire time.
func (h *Handler) dispatch(ev PendingEvent) {
	for _, svc := range ev.Config.Services {
		FiredTotal.WithLabelValues(ev.Repo, svc).Inc()
	}
	h.queue.Submit(BuildRequest{
		Repo:      ev.Repo,
		CommitSHA: ev.CommitSHA,
		Config:    ev.Config,
	})
}
