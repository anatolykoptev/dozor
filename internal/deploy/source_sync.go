package deploy

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// sourceSyncOutcome classifies the result of a best-effort source-checkout sync.
type sourceSyncOutcome string

const (
	// syncUpToDate — nothing to do (HEAD already at origin, or guard hit).
	syncUpToDate sourceSyncOutcome = "up_to_date"
	// syncFFUpdated — the local branch ref was fast-forwarded to origin.
	syncFFUpdated sourceSyncOutcome = "ff_updated"
	// syncDirtySkipped — tracked working-tree edits present; left untouched.
	syncDirtySkipped sourceSyncOutcome = "skipped_dirty"
	// syncLockedSkipped — .git/index.lock present (a build/agent/timer holds it).
	syncLockedSkipped sourceSyncOutcome = "skipped_locked"
	// syncDisabled — DOZOR_DEPLOY_SOURCE_SYNC not set truthy (the default).
	syncDisabled sourceSyncOutcome = "skipped_disabled"
	// syncCheckedOutElsewhere — the target branch is checked out in another
	// worktree, so git refuses the ref update — benign, the operator's worktree
	// is intentionally not advanced.
	syncCheckedOutElsewhere sourceSyncOutcome = "checked_out_elsewhere"
	// syncDiverged — local default branch has commits ahead of / diverged from
	// origin; ff refuses. Benign (operator has unpushed work) — kept distinct
	// from syncError so the error metric stays alert-clean.
	syncDiverged sourceSyncOutcome = "skipped_diverged"
	// syncError — a git command failed unexpectedly; checkout left as-is.
	syncError sourceSyncOutcome = "error"
)

// sourceSyncTimeout bounds a single source-sync attempt. The caller launches
// the sync in a detached goroutine with this timeout so a hung `git fetch`
// (network) can never block, delay, or fail the deploy.
const sourceSyncTimeout = 60 * time.Second

// gitRefFFRunner fast-forwards the local <branch> ref to origin/<branch>
// WITHOUT touching the working tree, via the network-free self-fetch
// `git fetch . origin/<branch>:<branch>`. Used when sourcePath is checked out
// on a different (feature) branch. git itself refuses a non-fast-forward AND
// refuses a ref checked out in another worktree — both are benign skips, not
// data-loss. Replaceable in tests.
var gitRefFFRunner = defaultGitRefFFRunner

//nolint:unused // DI default seam — assigned to var gitRefFFRunner, swapped in tests
func defaultGitRefFFRunner(ctx context.Context, sourcePath, branch string) (string, error) {
	refspec := fmt.Sprintf("origin/%s:%s", branch, branch)
	cmd := exec.CommandContext(ctx, "git", "fetch", ".", refspec) //nolint:gosec // trusted config
	cmd.Dir = sourcePath
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%w: %s", err, truncate(string(out), maxOutputLen))
	}
	return string(out), nil
}

// gitIndexLockPresent reports whether sourcePath has a .git/index.lock — i.e.
// another git process (a concurrent deploy, the hourly timer, or an operator)
// holds the index. Replaceable in tests.
var gitIndexLockPresent = defaultGitIndexLockPresent

//nolint:unused // DI default seam — assigned to var gitIndexLockPresent, swapped in tests
func defaultGitIndexLockPresent(sourcePath string) bool {
	_, err := os.Stat(filepath.Join(sourcePath, ".git", "index.lock"))
	return err == nil
}

// sourceSyncEnabled reads DOZOR_DEPLOY_SOURCE_SYNC once at process init.
// Default OFF (kill-switch is the absence of the flag). Truthy values per
// strconv.ParseBool: "1", "t", "T", "TRUE", "true", "True".
var sourceSyncEnabled = func() bool {
	v := os.Getenv("DOZOR_DEPLOY_SOURCE_SYNC")
	if v == "" {
		return false
	}
	enabled, err := strconv.ParseBool(v)
	if err != nil {
		slog.Warn("DOZOR_DEPLOY_SOURCE_SYNC invalid, treating as off", "value", v)
		return false
	}
	return enabled
}()

// syncSourceCheckout best-effort fast-forwards sourcePath's default branch
// (main/master) to origin, on every deploy, so each repo's `~/src/X` source
// checkout — what go-code indexes and the operator develops on — stays current
// instead of waiting for the hourly safety-net timer.
//
// It implements the SAME safety contract as pullDeployClone and the
// ~/bin/git-sync-main-master.sh timer:
//
//   - --ff-only ALWAYS; never merge/rebase/force; never switch the checked-out branch.
//   - flag off (default)                → syncDisabled    (no git calls)
//   - sourcePath == deployClonePath     → syncUpToDate     (already pulled by pullDeployClone; no double-pull)
//   - .git/index.lock present           → syncLockedSkipped
//   - tracked working-tree edits        → syncDirtySkipped (untracked scratch does NOT block)
//   - one real `git fetch origin <branch>` up front (mirrors the timer), THEN:
//   - on target branch, ff pull         → syncFFUpdated / syncUpToDate / syncDiverged
//   - on a feature branch, ref-ff       → syncFFUpdated / syncUpToDate / syncCheckedOutElsewhere / syncDiverged
//
// Every outcome is observable: the caller bumps DeploySourceSyncTotal; every
// error path also logs a WARN. Best-effort — it is the caller's contract that
// this runs in a detached, timeout-bounded goroutine off the deploy hot path.
func syncSourceCheckout(ctx context.Context, repo, sourcePath, deployClonePath string) sourceSyncOutcome {
	if !sourceSyncEnabled {
		return syncDisabled
	}
	if sourcePath == "" {
		return syncUpToDate // no source checkout to advance
	}
	// Guard: when the source IS the deploy clone (krolik-server), pullDeployClone
	// already ff-pulled it pre-build — skip to avoid a redundant double-pull.
	if sourcePath == deployClonePath {
		return syncUpToDate
	}

	// A concurrent git process (deploy worktree-add, the timer, an operator)
	// holds the index — back off rather than fight the lock.
	if gitIndexLockPresent(sourcePath) {
		slog.Debug("deploy: source sync skipped — .git/index.lock present",
			"repo", repo, "source", sourcePath)
		return syncLockedSkipped
	}

	// Never overwrite operator edits: tracked changes block, untracked scratch
	// (agent-written plans/reports) does not — mirrors classifyPorcelain's
	// contract in pullDeployClone.
	statusOut, err := gitStatusRunner(ctx, sourcePath)
	if err != nil {
		slog.Warn("deploy: source sync — git status failed",
			"repo", repo, "source", sourcePath, "error", err)
		return syncError
	}
	tracked, untracked := classifyPorcelain(string(statusOut))
	if tracked > 0 {
		slog.Warn("deploy: source sync skipped — working tree is dirty; source checkout may be stale",
			"repo", repo,
			"source", sourcePath,
			"dirty_tracked", tracked,
			"untracked", untracked,
			"status", truncate(string(statusOut), maxOutputLen),
		)
		return syncDirtySkipped
	}

	branch := detectDefaultBranch(ctx, sourcePath)
	cur, err := gitCurrentBranchRunner(ctx, sourcePath)
	if err != nil {
		slog.Warn("deploy: source sync — cannot resolve current branch",
			"repo", repo, "source", sourcePath, "error", err)
		return syncError
	}

	// ONE real network fetch up front (mirrors git-sync-main-master.sh:63, which
	// does a single `git fetch --prune origin` BEFORE the per-branch on/off
	// logic). Both branches below then operate on a FRESH origin/<branch>
	// remote-tracking ref. This is the load-bearing freshness step — without it
	// the off-branch self-fetch (`git fetch . origin/<b>:<b>`) would advance the
	// local ref only to a stale local origin/<branch>.
	if err := gitFetchRunner(ctx, sourcePath, branch); err != nil {
		slog.Warn("deploy: source sync — git fetch failed; leaving checkout as-is",
			"repo", repo, "source", sourcePath, "branch", branch, "current", cur, "error", err)
		return syncError
	}

	// Case A: the checkout IS on the target branch → ff-only pull.
	if cur == branch {
		return syncOnBranch(ctx, repo, sourcePath, branch)
	}
	// Case B: the checkout is on a feature branch / detached HEAD → advance the
	// freshly-fetched local <branch> ref WITHOUT touching the worktree.
	return syncOffBranch(ctx, repo, sourcePath, branch, cur)
}

// syncOnBranch handles the case where sourcePath has the target branch checked
// out. The caller has already fetched origin/<branch>; this no-ops if HEAD is
// already current, else ff-only pulls.
func syncOnBranch(ctx context.Context, repo, sourcePath, branch string) sourceSyncOutcome {
	fetchHead, err := gitRevParseRunner(ctx, sourcePath, "FETCH_HEAD")
	if err != nil {
		slog.Warn("deploy: source sync — cannot resolve FETCH_HEAD",
			"repo", repo, "source", sourcePath, "error", err)
		return syncError
	}
	head, err := gitRevParseRunner(ctx, sourcePath, "HEAD")
	if err != nil {
		slog.Warn("deploy: source sync — cannot resolve HEAD",
			"repo", repo, "source", sourcePath, "error", err)
		return syncError
	}
	if fetchHead == head {
		slog.Debug("deploy: source sync — already up to date",
			"repo", repo, "source", sourcePath, "sha", short(head))
		return syncUpToDate
	}
	if err := gitPullFFRunner(ctx, sourcePath, branch); err != nil {
		// Local commits ahead on the default branch — ff refuses. Benign; the
		// operator has unpushed work, leave it untouched. Distinct from a real
		// git failure so the error metric stays alert-clean.
		slog.Warn("deploy: source sync — ff-only pull refused (local commits ahead / diverged); left untouched",
			"repo", repo, "source", sourcePath, "branch", branch, "error", err)
		return syncDiverged
	}
	newHead, _ := gitRevParseRunner(ctx, sourcePath, "HEAD")
	slog.Info("deploy: source sync — fast-forwarded",
		"repo", repo, "source", sourcePath, "branch", branch, "from", short(head), "to", short(newHead))
	return syncFFUpdated
}

// syncOffBranch handles the case where sourcePath is on a feature branch or a
// detached HEAD: advance the local <branch> ref to the just-fetched
// origin/<branch> via the network-free self-fetch, without switching or
// touching the working tree.
func syncOffBranch(ctx context.Context, repo, sourcePath, branch, cur string) sourceSyncOutcome {
	before, err := gitRevParseRunner(ctx, sourcePath, branch)
	if err != nil {
		// The local <branch> ref does not exist yet (fresh checkout where only
		// origin/<branch> exists). Treat "" as the pre-state; the self-fetch
		// below creates it. Not an error.
		before = ""
	}
	out, ffErr := gitRefFFRunner(ctx, sourcePath, branch)
	if ffErr != nil {
		// git refuses to update a ref that is checked out in another worktree —
		// this is the intended outcome when the operator has <branch> open in a
		// separate worktree; it is benign, not a failure.
		if strings.Contains(out, "checked out at") || strings.Contains(out, "is already checked out") {
			slog.Debug("deploy: source sync — target branch checked out in another worktree; ref left as-is",
				"repo", repo, "source", sourcePath, "branch", branch, "current", cur)
			return syncCheckedOutElsewhere
		}
		// A non-fast-forward (local <branch> diverged from origin) is a benign
		// skip — git refuses it, we never force. Distinct from a real failure.
		slog.Warn("deploy: source sync — ref ff-update refused (local ref diverged from origin); left as-is",
			"repo", repo, "source", sourcePath, "branch", branch, "current", cur, "error", ffErr)
		return syncDiverged
	}
	after, err := gitRevParseRunner(ctx, sourcePath, branch)
	if err != nil {
		slog.Warn("deploy: source sync — cannot resolve branch ref after update",
			"repo", repo, "source", sourcePath, "branch", branch, "error", err)
		return syncError
	}
	if before == after {
		slog.Debug("deploy: source sync — branch ref already up to date",
			"repo", repo, "source", sourcePath, "branch", branch, "current", cur)
		return syncUpToDate
	}
	slog.Info("deploy: source sync — branch ref fast-forwarded (worktree untouched)",
		"repo", repo, "source", sourcePath, "branch", branch, "current", cur,
		"from", short(before), "to", short(after))
	return syncFFUpdated
}
