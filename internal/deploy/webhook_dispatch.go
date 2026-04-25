package deploy

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
// BuildPaths whitelist. Returns false (i.e. proceed to build) when:
//   - BuildPaths is empty (feature disabled)
//   - the push has no per-commit file list (force push / oversize)
//   - at least one changed file matches the whitelist
//
// On skip, increments dozor_deploy_skipped_total{reason="no_relevant_paths"}.
func (h *Handler) skipByPathFilter(push pushEvent, rc *RepoConfig) bool {
	if len(rc.BuildPaths) == 0 {
		return false
	}
	changed := push.changedFiles()
	if len(changed) == 0 {
		// GitHub elided the diff — be conservative and build.
		return false
	}
	if MatchAny(changed, rc.BuildPaths) {
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
func (h *Handler) dispatchPush(push pushEvent, rc *RepoConfig) string {
	if window := rc.DebounceWindow(); window > 0 && h.debouncer != nil {
		// Key includes repo + sorted services so two services in the same
		// repo debounce independently.
		key := push.Repository.FullName + "#" + serviceKey(rc.Services)
		svcLabel := ""
		if len(rc.Services) > 0 {
			svcLabel = rc.Services[0]
		}
		h.debouncer.Submit(key, PendingEvent{
			Repo:      push.Repository.FullName,
			Service:   svcLabel,
			CommitSHA: push.HeadCommit.ID,
			Config:    *rc,
		}, window)
		return "debounced"
	}

	// Immediate dispatch — original behaviour.
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
