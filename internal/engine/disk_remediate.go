package engine

import "context"

// DiskRemediateResult summarizes disk auto-remediation actions.
type DiskRemediateResult struct {
	Targets []CleanupTarget // journal/tmp/caches results
	Docker  string          // empty unless docker prune ran
	Errors  []string        // any per-target errors
}

// AutoRemediateDisk runs disk cleanup based on alert severity using a three-level strategy:
//
//   - AlertWarning (80-84%): journal(7d) + tmp(7d) + caches + apt + sccache + npm_yarn + docker dangling.
//   - AlertWarningHigh (85-94%): all WARNING targets + lang caches (go/npm/uv/pip) + docker builder prune (until=72h).
//   - AlertCritical / AlertError (95%+): all WARNING_HIGH targets + unconditional build-cache prune + age-bounded docker system prune.
//
// Returns nil if cleanup collector unavailable or level is not Warning/WarningHigh/Critical/Error.
func (a *ServerAgent) AutoRemediateDisk(ctx context.Context, level AlertLevel) *DiskRemediateResult {
	if a.cleanup == nil {
		return nil
	}

	switch level {
	case AlertWarning, AlertWarningHigh, AlertCritical, AlertError:
		// valid levels — proceed
	default:
		return nil
	}

	res := &DiskRemediateResult{}

	// Base targets — run at all active levels.
	appendTarget(res, a.cleanup.cleanJournal(ctx, "7d"), "journal")
	appendTarget(res, a.cleanup.cleanTmp(ctx, "7d"), "tmp")
	appendTarget(res, a.cleanup.cleanCaches(ctx), "caches")

	// WARNING+ targets — apt, sccache, npm/yarn caches, docker dangling images.
	appendTarget(res, a.cleanup.cleanAptCache(ctx), "apt")
	appendTarget(res, a.cleanup.cleanSccache(ctx), "sccache")
	appendTarget(res, a.cleanup.cleanNpmYarn(ctx), "npm_yarn")
	appendTarget(res, a.cleanup.cleanDockerDangling(ctx), "docker_dangling")

	// WARNING_HIGH+ targets — language tool caches + builder cache aged >72h.
	if level == AlertWarningHigh || level == AlertCritical || level == AlertError {
		appendTarget(res, a.cleanup.cleanGo(ctx), "go")
		appendTarget(res, a.cleanup.cleanNpm(ctx), "npm")
		appendTarget(res, a.cleanup.cleanUv(ctx), "uv")
		appendTarget(res, a.cleanup.cleanPip(ctx), "pip")
		appendTarget(res, a.cleanup.cleanDockerBuilderAged(ctx, "72h"), "docker_builder_aged")
	}

	// CRITICAL / ERROR — full docker system prune.
	if level == AlertCritical || level == AlertError {
		// Unconditional build-cache prune FIRST — age-filtered prunes miss
		// same-day cache that can fill the docker volume in a single heavy build
		// day (RCA 2026-05-25: sdc 91%, 65GB of <24h cache, filtered prune freed 0).
		appendTarget(res, a.cleanup.cleanDockerBuilderAll(ctx), "docker_builder_all")
		res.Docker = a.PruneDocker(ctx, true, true, false, "24h")
	}

	return res
}

// appendTarget adds a CleanupTarget to the result, propagating any error to res.Errors.
func appendTarget(res *DiskRemediateResult, t CleanupTarget, errPrefix string) {
	res.Targets = append(res.Targets, t)
	if t.Error != "" {
		res.Errors = append(res.Errors, errPrefix+": "+t.Error)
	}
}
