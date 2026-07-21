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
	// from syncError so the error metric stays alert-clean. After the P0
	// classifier this fires ONLY when `rev-list --count origin/<b>..HEAD > 0`
	// (genuine local commits ahead) — never as a catch-all for "ff failed".
	syncDiverged sourceSyncOutcome = "skipped_diverged"
	// syncUntrackedCollision — a clean ancestor (behind>0, ahead==0, tracked==0)
	// whose ff is refused because an UNTRACKED local file occupies a path the
	// incoming commit tracks. Distinct from syncDiverged (no local commits) — this
	// is the auto-fixable class. Fires when quarantine is OFF, or when quarantine
	// ran but the ff still failed.
	syncUntrackedCollision sourceSyncOutcome = "skipped_untracked_collision"
	// syncFFAfterQuarantine — an untracked-collision was auto-resolved: the
	// colliding untracked files were moved to the quarantine dir and the ff-only
	// pull then succeeded. (P1, behind DOZOR_DEPLOY_SOURCE_SYNC_QUARANTINE.)
	syncFFAfterQuarantine sourceSyncOutcome = "ff_after_quarantine"
	// syncQuarantineCapped — more than maxQuarantineColliders untracked colliders;
	// quarantine refused (a bulk-move that large signals something is wrong and
	// needs a human). Left untouched.
	syncQuarantineCapped sourceSyncOutcome = "skipped_quarantine_capped"
	// syncError — a git command failed unexpectedly; checkout left as-is.
	syncError sourceSyncOutcome = "error"
)

// maxQuarantineColliders caps how many untracked colliding files the auto-fix
// (P1) will move in one run. Beyond this, quarantine refuses and emits
// syncQuarantineCapped — a collider count this large means something is wrong
// (a whole tracked subtree shadowed by untracked copies) and wants a human, not
// a bulk mv on the deploy hot path.
const maxQuarantineColliders = 200

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

// gitRevListCountRunner returns the integer output of
// `git rev-list --count <range>` in sourcePath (e.g. "origin/main..HEAD" for the
// ahead-count, "HEAD..origin/main" for the behind-count). Used by the P0
// classifier to distinguish a genuine divergence (commits ahead) from a clean
// ancestor whose ff failed for another reason. Replaceable in tests.
var gitRevListCountRunner = defaultGitRevListCountRunner

//nolint:unused // DI default seam — assigned to var gitRevListCountRunner, swapped in tests
func defaultGitRevListCountRunner(ctx context.Context, sourcePath, revRange string) (int, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-list", "--count", revRange) //nolint:gosec // trusted config
	cmd.Dir = sourcePath
	out, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("rev-list --count %s: %w", revRange, err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, fmt.Errorf("rev-list --count %s: parse %q: %w", revRange, string(out), err)
	}
	return n, nil
}

// gitLsTreeRunner returns the set of file paths that origin/<branch> tracks as
// blobs (`git ls-tree -r --name-only origin/<branch>`), as a path→struct{} set.
// Used by the P1 quarantine to compute the collider intersection: an untracked
// local file collides only if the incoming commit ALSO tracks that exact path.
// Replaceable in tests.
var gitLsTreeRunner = defaultGitLsTreeRunner

//nolint:unused // DI default seam — assigned to var gitLsTreeRunner, swapped in tests
func defaultGitLsTreeRunner(ctx context.Context, sourcePath, branch string) (map[string]struct{}, error) {
	ref := "origin/" + branch
	cmd := exec.CommandContext(ctx, "git", "ls-tree", "-r", "--name-only", ref) //nolint:gosec // trusted config
	cmd.Dir = sourcePath
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ls-tree %s: %w", ref, err)
	}
	set := make(map[string]struct{})
	for _, line := range strings.Split(string(out), "\n") {
		p := strings.TrimSpace(line)
		if p != "" {
			set[p] = struct{}{}
		}
	}
	return set, nil
}

// gitIsTracked reports whether path is tracked in sourcePath's index
// (`git ls-files --error-unmatch <path>` exits 0). The P1 quarantine calls this
// per collider immediately before any mv as defence-in-depth: a tracked file
// must NEVER be moved, even if porcelain classified it untracked a moment ago
// (TOCTOU guard). Replaceable in tests.
var gitIsTracked = defaultGitIsTracked

//nolint:unused // DI default seam — assigned to var gitIsTracked, swapped in tests
func defaultGitIsTracked(ctx context.Context, sourcePath, path string) bool {
	cmd := exec.CommandContext(ctx, "git", "ls-files", "--error-unmatch", "--", path) //nolint:gosec // trusted config
	cmd.Dir = sourcePath
	// Exit 0 → tracked; non-zero → untracked (or unknown path). We treat any
	// non-zero as "not provably tracked"; the mv only proceeds on a definite
	// untracked verdict.
	return cmd.Run() == nil
}

// quarantineMover moves srcAbs to dstAbs, creating dstAbs's parent dir. The P1
// auto-fix wires the default os-level implementation; tests swap it to assert
// which files would move without touching the filesystem. Returns an error that
// aborts the whole quarantine (fail-closed: a failed mv leaves the tree as-is).
var quarantineMover = defaultQuarantineMover

//nolint:unused // DI default seam — assigned to var quarantineMover, swapped in tests
func defaultQuarantineMover(srcAbs, dstAbs string) error {
	if err := os.MkdirAll(filepath.Dir(dstAbs), 0o750); err != nil { //nolint:mnd // standard workspace dir mode
		return fmt.Errorf("mkdir quarantine dir: %w", err)
	}
	if err := os.Rename(srcAbs, dstAbs); err != nil {
		return fmt.Errorf("mv %s: %w", srcAbs, err)
	}
	return nil
}

// quarantineRoot is the base dir under ~/tmp where colliding untracked files are
// moved (recoverable, never rm). Under ~/tmp per the scratch-hygiene rule so the
// home-tidy $HOME-root sweep never touches it. Resolved once at init.
var quarantineRoot = defaultQuarantineRoot()

func defaultQuarantineRoot() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "/home/krolik"
	}
	return filepath.Join(home, "tmp", "git-sync-quarantine")
}

// sourceSyncEnabled reads DOZOR_DEPLOY_SOURCE_SYNC once at process init.
// Default OFF (kill-switch is the absence of the flag). Truthy values per
// strconv.ParseBool: "1", "t", "T", "TRUE", "true", "True".
var sourceSyncEnabled = parseBoolEnv("DOZOR_DEPLOY_SOURCE_SYNC")

// quarantineEnabled reads DOZOR_DEPLOY_SOURCE_SYNC_QUARANTINE once at process
// init. Default OFF — the untracked-collision auto-quarantine (P1) is a
// filesystem-mutating step on the deploy hot path, gated separately from the
// classifier (P0, always-on) and from the parent source-sync flag. With it OFF,
// an untracked collision is classified `skipped_untracked_collision` and left
// untouched; with it ON, the colliding untracked files are quarantined and the
// ff retried once.
var quarantineEnabled = parseBoolEnv("DOZOR_DEPLOY_SOURCE_SYNC_QUARANTINE")

// parseBoolEnv reads name once and returns its strconv.ParseBool value, defaulting
// to false on empty/invalid (the kill-switch is the absence of the flag).
func parseBoolEnv(name string) bool {
	v := os.Getenv(name)
	if v == "" {
		return false
	}
	enabled, err := strconv.ParseBool(v)
	if err != nil {
		//nolint:gosec // G706: value is from trusted deploy env config (operator-set), logged for diagnostics
		slog.Warn("env flag invalid, treating as off", "flag", name, "value", v)
		return false
	}
	return enabled
}

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
//   - on target branch, ff pull         → syncFFUpdated / syncUpToDate, or on
//     ff-refusal the P0 classifier (classifyFFFailure):
//     ahead>0  → syncDiverged
//     ahead==0 → untracked-collision → syncUntrackedCollision, or with the
//     quarantine flag ON: syncFFAfterQuarantine / syncQuarantineCapped
//   - on a feature branch, ref-ff       → syncFFUpdated / syncUpToDate / syncCheckedOutElsewhere / syncDiverged
//     (a bare ref-ff cannot collide with an untracked worktree file → diverged is
//     exact: the local ref is non-ff to origin)
//
// Every outcome is observable: the caller bumps DeploySourceSyncTotal; every
// error path also logs a WARN. Best-effort — it is the caller's contract that
// this runs in a detached, timeout-bounded goroutine off the deploy hot path.
func syncSourceCheckout(ctx context.Context, repo, sourcePath, deployClonePath, branch string) sourceSyncOutcome {
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

	// Use the per-target configured branch (req.Config.Branch) when present;
	// fall back to the main/master guess ONLY when the caller passed none,
	// preserving backward-compat for repos without a `branch:` config field.
	if branch == "" {
		branch = detectDefaultBranch(ctx, sourcePath)
	}
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
		// ff refused. Do NOT assume "diverged" — classify why. The tracked-dirty
		// case is already excluded upstream (syncSourceCheckout short-circuits on
		// tracked>0 BEFORE we get here), so the only two classes left are:
		//   ahead>0  → genuine divergence (local commits unpushed) → syncDiverged
		//   ahead==0 → clean ancestor whose ff was blocked by an untracked file
		//              occupying an incoming tracked path → untracked-collision
		//              (auto-fixable via quarantine when enabled).
		return classifyFFFailure(ctx, repo, sourcePath, branch, err)
	}
	newHead, _ := gitRevParseRunner(ctx, sourcePath, "HEAD")
	slog.Info("deploy: source sync — fast-forwarded",
		"repo", repo, "source", sourcePath, "branch", branch, "from", short(head), "to", short(newHead))
	return syncFFUpdated
}

// classifySyncState is the pure, side-effect-free taxonomy shared by the dozor
// Go path and the ~/bin/git-sync-main-master.sh hourly timer. It maps a git
// state to the result label, with the SAME decision order both impls must obey:
//
//  1. tracked>0                        → skipped_dirty   (real uncommitted work)
//  2. else ahead>0                     → skipped_diverged (local commits unpushed)
//  3. else behind==0                   → up_to_date
//  4. else (clean ancestor, behind>0):
//     - no untracked collision         → ff_updated      (the ff will succeed)
//     - untracked collision, quarantine off → skipped_untracked_collision
//     - untracked collision, quarantine on  → ff_after_quarantine
//
// The ordering of (1) before (2) is the go-job fix: tracked-dirty must be classed
// `skipped_dirty`, never `skipped_diverged`. This function is the contract the
// golden fixture (testdata/sync-classification.golden.tsv) pins for BOTH impls;
// the production path's interleaved git calls must produce the same labels. The
// golden test lives in source_sync_classify_test.go (Go) and
// ~/bin/git-sync-classify_test.sh (shell), both reading the one TSV.
func classifySyncState(tracked, ahead, behind int, untrackedCollision, quarantineEnabled bool) sourceSyncOutcome {
	switch {
	case tracked > 0:
		return syncDirtySkipped
	case ahead > 0:
		return syncDiverged
	case behind == 0:
		return syncUpToDate
	case !untrackedCollision:
		return syncFFUpdated
	case quarantineEnabled:
		return syncFFAfterQuarantine
	default:
		return syncUntrackedCollision
	}
}

// classifyFFFailure decides why an ff-only advance was refused on a checkout
// whose tracked tree is clean (the tracked-dirty case is excluded upstream).
// Decision order is load-bearing:
//  1. ahead = rev-list --count origin/<b>..HEAD. If ahead>0 → genuine divergence
//     (local commits unpushed) → syncDiverged. NEVER auto-touch.
//  2. else (ahead==0, a clean ancestor whose ff was blocked by an untracked file
//     that shadows an incoming tracked path) → the auto-fixable untracked-
//     collision class. If quarantine is enabled, move the colliders aside and
//     retry the ff once; otherwise classify syncUntrackedCollision and leave it.
//
// This is the fix for the go-job mislabel: the old code returned syncDiverged
// for ANY ff failure without ever computing ahead — so a clean ancestor with an
// untracked collider was reported as "diverged".
func classifyFFFailure(ctx context.Context, repo, sourcePath, branch string, ffErr error) sourceSyncOutcome {
	ahead, err := gitRevListCountRunner(ctx, sourcePath, "origin/"+branch+"..HEAD")
	if err != nil {
		// Cannot compute ahead/behind → cannot classify safely. Treat as error
		// (a real git failure), not a benign skip, so the error metric surfaces it.
		slog.Warn("deploy: source sync — ff refused and ahead-count failed; cannot classify",
			"repo", repo, "source", sourcePath, "branch", branch, "ff_error", ffErr, "error", err)
		return syncError
	}
	if ahead > 0 {
		// Genuine divergence: the operator has local commits not on origin. ff
		// refuses; leave it. Distinct from a real git failure so error stays clean.
		slog.Warn("deploy: source sync — ff-only refused: local commits ahead of origin (diverged); left untouched",
			"repo", repo, "source", sourcePath, "branch", branch, "ahead", ahead, "ff_error", ffErr)
		return syncDiverged
	}
	// ahead==0: a clean ancestor whose ff was blocked — the untracked-collision
	// class. Auto-resolve it when quarantine is enabled.
	return resolveUntrackedCollision(ctx, repo, sourcePath, branch, ffErr)
}

// resolveUntrackedCollision handles a clean ancestor (ahead==0, tracked==0)
// whose ff-only pull was blocked. With the quarantine flag OFF (default) it
// classifies the outcome syncUntrackedCollision and leaves the tree untouched.
// With the flag ON (P1) it moves the colliding untracked files to the quarantine
// dir and retries the ff once:
//   - retry succeeds → syncFFAfterQuarantine
//   - retry still fails → syncUntrackedCollision (do NOT loop)
//   - >maxQuarantineColliders → syncQuarantineCapped (escalate, never bulk-move)
//
// SAFETY: only files confirmed untracked (`git ls-files --error-unmatch` says
// "not tracked") are ever moved; a single tracked collider aborts the whole
// quarantine. mv (not rm) keeps every moved file recoverable.
func resolveUntrackedCollision(ctx context.Context, repo, sourcePath, branch string, ffErr error) sourceSyncOutcome {
	if !quarantineEnabled {
		slog.Warn("deploy: source sync — ff blocked by untracked collision on a clean ancestor; "+
			"quarantine disabled, left untouched (set DOZOR_DEPLOY_SOURCE_SYNC_QUARANTINE to auto-resolve)",
			"repo", repo, "source", sourcePath, "branch", branch, "ff_error", ffErr)
		return syncUntrackedCollision
	}

	moved, capped, err := quarantineColliders(ctx, repo, sourcePath, branch)
	if err != nil {
		slog.Warn("deploy: source sync — quarantine aborted; checkout left untouched",
			"repo", repo, "source", sourcePath, "branch", branch, "error", err)
		// Abort (e.g. a collider turned out tracked, or a mv failed) → leave the
		// tree as it was. Classify as the collision class, not error: the tree is
		// intact, no data lost, and a human-resolvable collision remains.
		return syncUntrackedCollision
	}
	if capped {
		// Too many colliders to safely bulk-move — escalate, don't touch the tree.
		return syncQuarantineCapped
	}
	if moved == 0 {
		// No untracked file actually intersected the incoming tree, yet ff failed:
		// the blocker is something else (a worktree lock, a race). Not our class —
		// leave it; surface as the collision class so it stays visible without
		// inflating the error metric.
		slog.Warn("deploy: source sync — ff blocked but no untracked collider found; left untouched",
			"repo", repo, "source", sourcePath, "branch", branch, "ff_error", ffErr)
		return syncUntrackedCollision
	}

	// Retry the ff-only pull exactly ONCE after clearing the colliders.
	if retryErr := gitPullFFRunner(ctx, sourcePath, branch); retryErr != nil {
		slog.Warn("deploy: source sync — ff still refused after quarantine; left untouched (no retry loop)",
			"repo", repo, "source", sourcePath, "branch", branch,
			"quarantined_files", moved, "error", retryErr)
		return syncUntrackedCollision
	}
	newHead, _ := gitRevParseRunner(ctx, sourcePath, "HEAD")
	slog.Info("deploy: source sync — fast-forwarded after quarantining untracked colliders",
		"repo", repo, "source", sourcePath, "branch", branch,
		"quarantined_files", moved, "to", short(newHead))
	return syncFFAfterQuarantine
}

// quarantineColliders moves every UNTRACKED working-tree file that occupies a
// path the incoming origin/<branch> tree also tracks (the collider set) into the
// per-run quarantine dir, then reports how many it moved.
//
// Detection is a PRE-CHECK (not stderr-parse): intersect the untracked porcelain
// entries with `git ls-tree -r origin/<branch>` so we get the exact, locale- and
// git-version-independent collider list. Each candidate is re-checked with
// `git ls-files --error-unmatch` immediately before its mv — if ANY collider is
// actually tracked, the whole quarantine aborts (returns an error) and zero
// files move (fail-closed; never lose tracked work).
//
// Returns (moved, capped, err):
//   - capped=true when len(colliders) > maxQuarantineColliders (nothing moved).
//   - err != nil aborts with the tree left as-is (a tracked collider, a status
//     re-read failure, or an mv failure).
func quarantineColliders(ctx context.Context, repo, sourcePath, branch string) (moved int, capped bool, err error) {
	// Re-read porcelain at mv-time (not the caller's earlier snapshot) so the
	// untracked set is as fresh as possible before we touch the filesystem.
	statusOut, err := gitStatusRunner(ctx, sourcePath)
	if err != nil {
		return 0, false, fmt.Errorf("re-read status: %w", err)
	}
	untracked := untrackedPaths(string(statusOut))
	if len(untracked) == 0 {
		return 0, false, nil
	}

	incoming, err := gitLsTreeRunner(ctx, sourcePath, branch)
	if err != nil {
		return 0, false, fmt.Errorf("ls-tree origin/%s: %w", branch, err)
	}

	// Collider set: untracked ∩ incoming-tracked paths.
	colliders := make([]string, 0, len(untracked))
	for _, p := range untracked {
		if _, ok := incoming[p]; ok {
			colliders = append(colliders, p)
		}
	}
	if len(colliders) == 0 {
		return 0, false, nil
	}
	if len(colliders) > maxQuarantineColliders {
		slog.Warn("deploy: source sync — untracked-collider count over cap; refusing bulk quarantine, escalating",
			"repo", repo, "source", sourcePath, "branch", branch,
			"colliders", len(colliders), "cap", maxQuarantineColliders)
		return 0, true, nil
	}

	// Per-run timestamped dir so the timer and a deploy never clobber each other.
	runDir := filepath.Join(quarantineRoot, repoSlug(repo), time.Now().UTC().Format("20060102T150405Z"))

	for _, rel := range colliders {
		// Defence-in-depth: a tracked file must NEVER move. If the pre-check and
		// this guard ever disagree (a TOCTOU race), abort the whole quarantine —
		// nothing already moved is lost (it's in the quarantine dir), and the
		// remaining tree is left intact.
		if gitIsTracked(ctx, sourcePath, rel) {
			return moved, false, fmt.Errorf("collider %q is tracked — aborting quarantine (refuse to move tracked work)", rel)
		}
		srcAbs := filepath.Join(sourcePath, filepath.FromSlash(rel))
		dstAbs := filepath.Join(runDir, filepath.FromSlash(rel))
		if mvErr := quarantineMover(srcAbs, dstAbs); mvErr != nil {
			return moved, false, fmt.Errorf("quarantine %q: %w", rel, mvErr)
		}
		moved++
		slog.Info("deploy: source sync — quarantined untracked collider",
			"repo", repo, "path", rel, "to", dstAbs)
	}
	return moved, false, nil
}

// untrackedPaths extracts the relative paths of untracked entries ("?? <path>")
// from `git status --porcelain` output. Mirrors classifyPorcelain's untracked
// rule but returns the paths (classifyPorcelain only counts).
func untrackedPaths(out string) []string {
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "?? ") {
			continue
		}
		p := strings.TrimSpace(strings.TrimPrefix(line, "??"))
		// `--porcelain` (v1) quotes paths with special chars: "a\tb". Unquote so
		// the path matches ls-tree output and the on-disk file.
		if unq, qErr := strconv.Unquote(p); qErr == nil {
			p = unq
		}
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths
}

// repoSlug turns "owner/name" into a single filesystem-safe segment "owner-name"
// for the quarantine dir, so a repo with a slash never escapes quarantineRoot.
func repoSlug(repo string) string {
	if repo == "" {
		return "unknown"
	}
	return strings.ReplaceAll(repo, "/", "-")
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
