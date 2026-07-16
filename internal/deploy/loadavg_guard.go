package deploy

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Absolute-load backpressure guard for HEAVY dozor builds (P3 of the dozor
// build-orchestration audit).
//
// The P2 cross-lane ci-lock (crosslane_lock.go) serialises a dozor HEAVY build
// against the CI-runner lane, but does NOT catch load from UNCOORDINATED lanes
// (interactive/agent `cargo`/`go` builds that never take the ci-lock). Observed:
// load average hit ~82 from concurrent uncoordinated builds on the shared 4-core
// ARM box.
//
// This guard adds an absolute-load backpressure check: before a HEAVY build
// proceeds (and BEFORE it acquires the cross-lane lock — holding the box-wide
// lock while idle-waiting on load would needlessly stall the CI lane), it waits
// for the box 1-minute load average to drop below a threshold. If the load is
// still above threshold after a cap, it PROCEEDS anyway — a stuck deploy is the
// lesser evil vs a deadlocked pipeline (same fail-safe philosophy as the P2
// lock timeout).
//
// Contract (correctness is paramount — this is on the deploy critical path):
//   - Heavy builds ONLY. A non-heavy build never touches the guard.
//   - Runs BEFORE acquireCrossLaneLock in processBuild (do NOT hold the
//     box-wide lock while idle-waiting on load).
//   - Fail-OPEN on read error: if /proc/loadavg can't be read (non-Linux,
//     missing /proc), PROCEED immediately + log. A guard that blocks builds
//     because it can't read load is worse than no guard.
//   - Respect ctx cancellation while waiting (return/proceed if ctx is done).
//
// Structure mirrors crosslane_lock.go: injectable func vars, fail-safe,
// metric on every exit path, env-overridable thresholds.

const (
	defaultLoadGuardWaitSecs = 600 // 10m — same order as the P2 lock timeout
)

// loadGuardPollInterval is the interval between loadavg polls while waiting.
// A package var so tests can override it to something tiny — tests MUST NOT
// actually sleep for real seconds.
var loadGuardPollInterval = 15 * time.Second

// loadGuardWaitSecs is the fail-safe cap (seconds): if the load is still above
// threshold after this many seconds, PROCEED anyway. Overridable via
// DOZOR_LOAD_WAIT_SECS.
var loadGuardWaitSecs = defaultLoadGuardWaitSecs

// loadGuardMaxLoad is the 1-minute loadavg threshold above which a heavy build
// waits. Default = 2 * runtime.NumCPU() (= 8.0 on the 4-core box). Overridable
// via DOZOR_MAX_LOADAVG.
var loadGuardMaxLoad = float64(2 * runtime.NumCPU())

func init() {
	if v := os.Getenv("DOZOR_MAX_LOADAVG"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			loadGuardMaxLoad = f
		}
	}
	if v := os.Getenv("DOZOR_LOAD_WAIT_SECS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			loadGuardWaitSecs = n
		}
	}
}

// loadavgReader reads the 1-minute load average. Seam for tests; the default
// reads /proc/loadavg. Mirrors how crosslane_lock.go makes ciLockAcquirer /
// ciLockReleaser swappable.
var loadavgReader = readProcLoadavg

// readProcLoadavg reads /proc/loadavg and returns the 1-minute load average
// (first field). Returns an error on non-Linux hosts or missing /proc — the
// caller MUST fail-open on error (proceed immediately).
func readProcLoadavg() (float64, error) {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, err
	}
	return parseLoadavgLine(data)
}

// parseLoadavgLine parses the first field (1-min load average) from a
// /proc/loadavg content line. Format: "0.52 0.43 0.35 3/123 45678\n".
func parseLoadavgLine(data []byte) (float64, error) {
	fields := strings.Fields(string(data))
	if len(fields) < 1 {
		return 0, fmt.Errorf("malformed /proc/loadavg: %q", string(data))
	}
	load, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, fmt.Errorf("malformed /proc/loadavg first field %q: %w", fields[0], err)
	}
	return load, nil
}

// waitForLoadBelowThreshold blocks until the 1-minute load average drops below
// loadGuardMaxLoad, then returns. If the load is still above threshold after
// loadGuardWaitSecs, it PROCEEDS anyway (fail-safe). If the loadavg can't be
// read, it PROCEEDS immediately (fail-open). Respects ctx cancellation.
//
// Returns the outcome label and increments LoadDeferredTotal on every exit
// path so a dashboard/alert can observe how often the guard defers a build.
// Outcome values:
//   - "proceeded_immediately"  — load already below threshold, no wait.
//   - "proceeded_after_wait"   — waited, load dropped below threshold, proceeded.
//   - "proceeded_timeout"      — waited to the cap (or ctx cancelled), still high, proceeded.
//   - "proceeded_read_error"   — couldn't read loadavg, proceeded (fail-open).
func waitForLoadBelowThreshold(ctx context.Context) string {
	load, err := loadavgReader()
	if err != nil {
		slog.Warn("deploy: loadavg read failed, proceeding (fail-open)",
			"error", err, "threshold", loadGuardMaxLoad)
		LoadDeferredTotal.WithLabelValues("proceeded_read_error").Inc()
		return "proceeded_read_error"
	}
	if load < loadGuardMaxLoad {
		LoadDeferredTotal.WithLabelValues("proceeded_immediately").Inc()
		return "proceeded_immediately"
	}

	// Load is high — wait for it to drop, with a fail-safe cap.
	slog.Info("deploy: loadavg above threshold, waiting for heavy build",
		"load", load, "threshold", loadGuardMaxLoad,
		"poll_interval", loadGuardPollInterval, "cap_secs", loadGuardWaitSecs)

	deadline := time.Now().Add(time.Duration(loadGuardWaitSecs) * time.Second)
	// If the cap is already 0 (or negative), proceed immediately (timeout).
	if !time.Now().Before(deadline) {
		slog.Warn("deploy: loadavg still above threshold after cap, proceeding anyway",
			"load", load, "threshold", loadGuardMaxLoad, "cap_secs", loadGuardWaitSecs)
		LoadDeferredTotal.WithLabelValues("proceeded_timeout").Inc()
		return "proceeded_timeout"
	}

	ticker := time.NewTicker(loadGuardPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Warn("deploy: loadavg wait cancelled by ctx, proceeding",
				"load", load, "threshold", loadGuardMaxLoad)
			LoadDeferredTotal.WithLabelValues("proceeded_timeout").Inc()
			return "proceeded_timeout"
		case <-ticker.C:
			load, err := loadavgReader()
			if err != nil {
				slog.Warn("deploy: loadavg read failed during wait, proceeding (fail-open)",
					"error", err, "threshold", loadGuardMaxLoad)
				LoadDeferredTotal.WithLabelValues("proceeded_read_error").Inc()
				return "proceeded_read_error"
			}
			if load < loadGuardMaxLoad {
				slog.Info("deploy: loadavg dropped below threshold, proceeding",
					"load", load, "threshold", loadGuardMaxLoad)
				LoadDeferredTotal.WithLabelValues("proceeded_after_wait").Inc()
				return "proceeded_after_wait"
			}
			if !time.Now().Before(deadline) {
				slog.Warn("deploy: loadavg still above threshold after cap, proceeding anyway",
					"load", load, "threshold", loadGuardMaxLoad, "cap_secs", loadGuardWaitSecs)
				LoadDeferredTotal.WithLabelValues("proceeded_timeout").Inc()
				return "proceeded_timeout"
			}
		}
	}
}
