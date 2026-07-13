package engine

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
)

// DiskRemediateResult summarizes disk auto-remediation actions.
type DiskRemediateResult struct {
	Targets []CleanupTarget // journal/tmp/caches results
	Docker  string          // empty unless docker prune ran
	Errors  []string        // any per-target errors
}

// cleanupAgeDaysDefault is the fallback age (in days) for age-bounded cleanup
// ops (journal vacuum, tmp/cargo file pruning) when DOZOR_CLEANUP_AGE_DAYS is
// unset or invalid.
const cleanupAgeDaysDefault = 4

// cleanupAgeDays reads DOZOR_CLEANUP_AGE_DAYS (a plain integer day count),
// falling back to cleanupAgeDaysDefault when the env var is unset, not a
// valid integer, or <= 0. Logs a WARN on an invalid (set but bad) value —
// mirrors DOZOR_REMEDIATE_COOLDOWN's read-and-validate style in
// cmd/dozor/gateway_remediate_cooldown.go.
func cleanupAgeDays() int {
	raw := os.Getenv("DOZOR_CLEANUP_AGE_DAYS")
	if raw == "" {
		return cleanupAgeDaysDefault
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		slog.Warn("invalid DOZOR_CLEANUP_AGE_DAYS, falling back to default", //nolint:gosec // raw is an operator-supplied env var, not user input
			"value", raw, "default", cleanupAgeDaysDefault)
		return cleanupAgeDaysDefault
	}
	return n
}

// AutoRemediateDisk runs disk cleanup based on alert severity using a three-level strategy:
//
//   - AlertWarning (80-84%): journal(age) + tmp(age) + caches + apt + npm_yarn + docker dangling.
//   - AlertWarningHigh (85-94%): all WARNING targets + lang caches (go/npm/uv/pip) +
//     cargo target-dir prune (age) + docker builder prune (until=72h).
//   - AlertCritical / AlertError (95%+): all WARNING_HIGH targets + sccache +
//     unconditional build-cache prune + age-bounded docker system prune.
//
// age is the configured DOZOR_CLEANUP_AGE_DAYS threshold (default 4 days), shared by
// journal/tmp/cargo pruning.
//
// sccache moved to the CRITICAL-only tier (was WARNING): sccache-shared sits at its
// configured SCCACHE_CACHE_SIZE cap by design (LRU-managed), so nuking it at WARNING
// would routinely wipe the fleet's build-cache accelerator for a non-emergency level.
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
	minAge := fmt.Sprintf("%dd", cleanupAgeDays())

	// Base targets — run at all active levels.
	appendTarget(res, a.cleanup.cleanJournal(ctx, minAge), "journal")
	appendTarget(res, a.cleanup.cleanTmp(ctx, minAge), "tmp")
	appendTarget(res, a.cleanup.cleanCaches(ctx), "caches")

	// WARNING+ targets — apt, npm/yarn caches, docker dangling images.
	appendTarget(res, a.cleanup.cleanAptCache(ctx), "apt")
	appendTarget(res, a.cleanup.cleanNpmYarn(ctx), "npm_yarn")
	appendTarget(res, a.cleanup.cleanDockerDangling(ctx), "docker_dangling")

	// WARNING_HIGH+ targets — language tool caches + cargo target-dir prune + builder cache aged >72h.
	if level == AlertWarningHigh || level == AlertCritical || level == AlertError {
		appendTarget(res, a.cleanup.cleanGo(ctx), "go")
		appendTarget(res, a.cleanup.cleanNpm(ctx), "npm")
		appendTarget(res, a.cleanup.cleanUv(ctx), "uv")
		appendTarget(res, a.cleanup.cleanPip(ctx), "pip")
		appendTarget(res, a.cleanup.cleanCargo(ctx, minAge), "cargo")
		appendTarget(res, a.cleanup.cleanDockerBuilderAged(ctx, "72h"), "docker_builder_aged")
	}

	// CRITICAL / ERROR — sccache + full docker system prune.
	if level == AlertCritical || level == AlertError {
		// sccache is CRITICAL-only — see the doc comment above and
		// cleanSccache in cleanup_langs.go: it is LRU-managed at a fixed cap
		// by design, so it legitimately sits "full" under normal load; only
		// nuke it once we're already in an emergency.
		appendTarget(res, a.cleanup.cleanSccache(ctx), "sccache")
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
