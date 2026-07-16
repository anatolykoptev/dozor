package deploy

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"time"
)

// Cross-lane box-wide build lock (P2 of the dozor build-orchestration audit).
//
// dozor deploy builds, CI-runner builds, and interactive/agent builds are three
// uncoordinated lanes on the shared 4-core ARM box; when they coincide 3+
// concurrent `cargo build --release` push load average to ~80. dozor already
// serialises its OWN heavy builds via heavySem(1); this lock serialises a
// dozor HEAVY build against the OTHER heavy-build lanes by acquiring the SAME
// box-wide ci-lock the CI runners already use.
//
// The CI runners serialise via /home/krolik/bin/ci-lock-acquire.sh +
// ci-lock-release.sh — a mkdir-slot lock in $XDG_RUNTIME_DIR/ci-slot-N
// (CI_LOCK_SLOTS default 1, CI_LOCK_WAIT_SECS 2700s, stale-steal after
// CI_LOCK_STALE_SECS 3600s). dozor acquires this SAME lock around each HEAVY
// build and releases it after.
//
// Namespace (verified 2026-07-16 on krolik): dozor and EVERY CI runner run as
// the same user (krolik, uid 1002) under user@1002.service, so they share
// $XDG_RUNTIME_DIR=/run/user/1002 and the ci-slot-N dirs collide → the lock
// genuinely serialises cross-lane. No shared-path override is needed on this
// box. The env knobs below let a split-user deployment point both lanes at a
// shared dir (e.g. set XDG_RUNTIME_DIR on the acquire/release env) if it ever
// runs that way.
//
// Contract (correctness is paramount — a lock bug stalls ALL fleet deploys):
//   - Heavy builds ONLY. A non-Heavy build never touches the lock.
//   - Acquire before the build; RELEASE on EVERY exit path via defer (success,
//     build error, panic, early return). A leaked lock blocks all future heavy
//     builds until the 1h stale-steal — the stale-steal is a BACKSTOP, never
//     the primary mechanism.
//   - Deterministic JOB_KEY: GITHUB_REPOSITORY/GITHUB_RUN_ID/GITHUB_JOB are set
//     identically on acquire and release so the scripts compute the SAME key.
//   - Fail-SAFE on acquire timeout: a non-zero acquire (no slot freed within
//     CI_LOCK_WAIT_SECS) logs a warning and PROCEEDS with the build — a stuck
//     deploy is the lesser evil vs a deadlocked pipeline. A release is still
//     attempted (the script no-ops on missing state).

const (
	defaultCILockAcquireScript = "/home/krolik/bin/ci-lock-acquire.sh"
	defaultCILockReleaseScript = "/home/krolik/bin/ci-lock-release.sh"
	defaultCILockWaitSecs      = 2700 // 45m, matches the runner default

	// ciLockJob is the GITHUB_JOB value dozor identifies as — distinguishes a
	// dozor heavy-build slot from a CI runner slot in the owner file.
	ciLockJob = "dozor-deploy"
)

// ciLockAcquireScript / ciLockReleaseScript are the box-wide ci-lock scripts
// already used by the CI runners. Overridable via DOZOR_CI_LOCK_ACQUIRE /
// DOZOR_CI_LOCK_RELEASE for non-krolik hosts and for tests (swapped directly).
var (
	ciLockAcquireScript = defaultCILockAcquireScript
	ciLockReleaseScript = defaultCILockReleaseScript
	ciLockWaitSecs      = defaultCILockWaitSecs
)

func init() {
	if v := os.Getenv("DOZOR_CI_LOCK_ACQUIRE"); v != "" {
		ciLockAcquireScript = v
	}
	if v := os.Getenv("DOZOR_CI_LOCK_RELEASE"); v != "" {
		ciLockReleaseScript = v
	}
	if v := os.Getenv("DOZOR_CI_LOCK_WAIT_SECS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			ciLockWaitSecs = n
		}
	}
}

// ciLockAcquirer runs the acquire script with the given env. Returns true when
// a slot was acquired (script exit 0). A non-zero exit means the wait elapsed
// without a free slot (the script's only exit-1 path is the timeout branch).
// Seam for tests; the default shells out via exec.CommandContext.
var ciLockAcquirer = defaultCILockAcquirer

func defaultCILockAcquirer(ctx context.Context, env []string) (bool, error) {
	cmd := exec.CommandContext(ctx, ciLockAcquireScript) //nolint:gosec // trusted local script path
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		slog.Debug("deploy: cross-lane lock acquire script returned non-zero",
			"script", ciLockAcquireScript, "output", string(out), "error", err)
		return false, err
	}
	return true, nil
}

// ciLockReleaser runs the release script with the given env. The script always
// exits 0 (a release failure must not redden a finished job; stale-steal is the
// backstop), so errors here are logged but never propagated. Seam for tests.
var ciLockReleaser = defaultCILockReleaser

func defaultCILockReleaser(env []string) {
	cmd := exec.Command(ciLockReleaseScript) //nolint:gosec // trusted local script path
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		slog.Warn("deploy: cross-lane lock release failed (stale-steal is the backstop)",
			"script", ciLockReleaseScript, "output", string(out), "error", err)
	}
}

// ciLockEnv builds the deterministic env so acquire and release compute the
// SAME JOB_KEY inside the scripts. The scripts derive JOB_KEY from
// GITHUB_REPOSITORY/GITHUB_RUN_ID/GITHUB_JOB (falling back to $$); setting all
// three to a stable per-build key makes the release match the acquire exactly.
// repo namespacing via GITHUB_REPOSITORY=dozor/<repo> keeps dozor slots
// distinct from CI runner slots for the same SHA.
func ciLockEnv(repo, buildID string) []string {
	// Guard against a JOB_KEY divergence: the scripts derive RUN_ID via
	// ${GITHUB_RUN_ID:-$$}, and bash `:-` fires on EMPTY too — so an empty
	// buildID would make the acquire and release (separate procs) fall to
	// DIFFERENT $$ PIDs, the release key mismatch, and the acquired slot leak
	// until the 1h stale-steal. A fixed placeholder keeps RUN_ID non-empty and
	// stable across the acquire/release pair (reachable via head_commit:null).
	runID := buildID
	if runID == "" {
		runID = "nosha"
	}
	return []string{
		"GITHUB_REPOSITORY=dozor/" + repo,
		"GITHUB_RUN_ID=" + runID,
		"GITHUB_JOB=" + ciLockJob,
		"CI_LOCK_WAIT_SECS=" + strconv.Itoa(ciLockWaitSecs),
	}
}

// acquireCrossLaneLock tries to take the box-wide ci-lock for a HEAVY build.
// It ALWAYS returns a non-nil releaser the caller MUST defer on every exit
// path. Behaviour:
//   - acquired (script exit 0): releaser releases the slot on call.
//   - timed out / failed (non-zero): logs a warning and PROCEEDS unlocked
//     (fail-safe); the releaser still attempts a release, which the script
//     turns into a no-op when no state file exists for the key.
//
// The buildID must be stable across the acquire/release pair (it is captured in
// the env closure); the caller generates it once per build.
func acquireCrossLaneLock(ctx context.Context, repo, buildID string) func() {
	env := ciLockEnv(repo, buildID)
	acquired, err := ciLockAcquirer(ctx, env)
	if !acquired {
		wait := time.Duration(ciLockWaitSecs) * time.Second
		slog.Warn("cross-lane lock acquire timed out, proceeding unlocked",
			"repo", repo, "build_id", buildID, "wait", wait, "error", err)
		CrossLaneLockTotal.WithLabelValues("timeout_proceeded").Inc()
		// Fail-safe: still attempt release (no-op if acquire wrote no state).
		return func() { ciLockReleaser(env) }
	}
	slog.Info("deploy: cross-lane lock acquired for heavy build",
		"repo", repo, "build_id", buildID)
	CrossLaneLockTotal.WithLabelValues("acquired").Inc()
	return func() { ciLockReleaser(env) }
}
