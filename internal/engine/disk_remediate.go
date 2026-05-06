package engine

import "context"

// DiskRemediateResult summarizes disk auto-remediation actions.
type DiskRemediateResult struct {
	Targets []CleanupTarget // journal/tmp/caches results
	Docker  string          // empty unless docker prune ran
	Errors  []string        // any per-target errors
}

// AutoRemediateDisk runs disk cleanup based on alert severity.
//   - AlertWarning: cleanJournal(7d) + cleanTmp(7d) + cleanCaches.
//   - AlertCritical / AlertError: same + PruneDocker(images, buildCache, volumes=false, age=24h).
//
// Returns nil if cleanup collector unavailable or level is not Warning/Critical/Error.
func (a *ServerAgent) AutoRemediateDisk(ctx context.Context, level AlertLevel) *DiskRemediateResult {
	if a.cleanup == nil {
		return nil
	}

	switch level {
	case AlertWarning, AlertCritical, AlertError:
		// valid levels — proceed
	default:
		return nil
	}

	res := &DiskRemediateResult{}

	journal := a.cleanup.cleanJournal(ctx, "7d")
	res.Targets = append(res.Targets, journal)
	if journal.Error != "" {
		res.Errors = append(res.Errors, "journal: "+journal.Error)
	}

	tmp := a.cleanup.cleanTmp(ctx, "7d")
	res.Targets = append(res.Targets, tmp)
	if tmp.Error != "" {
		res.Errors = append(res.Errors, "tmp: "+tmp.Error)
	}

	caches := a.cleanup.cleanCaches(ctx)
	res.Targets = append(res.Targets, caches)
	if caches.Error != "" {
		res.Errors = append(res.Errors, "caches: "+caches.Error)
	}

	if level == AlertCritical || level == AlertError {
		res.Docker = a.PruneDocker(ctx, true, true, false, "24h")
	}

	return res
}
