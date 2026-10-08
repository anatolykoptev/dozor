package deploy

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
)

// pullOutcome classifies a successful deploy-clone pull outcome for the
// legacy dozor_deploy_clone_pull_total counter. Failure outcomes were removed
// in #239 — a failed verification REFUSES the deploy and counts under
// dozor_deploy_clone_refused_total instead.
type pullOutcome string

const (
	pullUpToDate    pullOutcome = "up_to_date"
	pullFastForward pullOutcome = "fast_forward"

	defaultBranch = "main"
)

// gitStatusRunner executes `git status --porcelain` in clonePath.
// Replaceable in tests.
var gitStatusRunner = defaultGitStatusRunner

//nolint:unused // DI default seam — assigned to var gitStatusRunner, swapped in tests
func defaultGitStatusRunner(ctx context.Context, clonePath string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain") //nolint:gosec // trusted config
	cmd.Dir = clonePath
	return cmd.Output()
}

// gitFetchRunner executes `git fetch origin <branch> --no-tags --quiet` in clonePath.
// The runner never takes the fetch lock itself — the caller owns the
// withFetchLock scope so that ONE lock can cover a whole
// fetch → rev-parse → merge sequence (pullDeployClone); standalone callers
// (source_sync, webhook_release) wrap the call in withFetchLock themselves.
// Replaceable in tests.
var gitFetchRunner = defaultGitFetchRunner

//nolint:unused // DI default seam — assigned to var gitFetchRunner, swapped in tests
func defaultGitFetchRunner(ctx context.Context, clonePath, branch string) error {
	cmd := exec.CommandContext(ctx, "git", "fetch", "origin", branch, "--no-tags", "--quiet") //nolint:gosec // trusted config
	cmd.Dir = clonePath
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, tail(string(out), maxOutputLen))
	}
	return nil
}

// gitCurrentBranchRunner returns the clone's current branch (rev-parse --abbrev-ref HEAD).
// Replaceable in tests.
var gitCurrentBranchRunner = defaultGitCurrentBranchRunner

//nolint:unused // DI default seam — assigned to var gitCurrentBranchRunner, swapped in tests
func defaultGitCurrentBranchRunner(ctx context.Context, clonePath string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--abbrev-ref", "HEAD") //nolint:gosec // trusted config
	cmd.Dir = clonePath
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

// gitRevParseRunner executes `git rev-parse FETCH_HEAD` and `git rev-parse HEAD`
// in clonePath to determine whether a pull would advance HEAD.
// Returns (fetchHead, head, error).
// Replaceable in tests.
var gitRevParseRunner = defaultGitRevParseRunner

//nolint:unused // DI default seam — assigned to var gitRevParseRunner, swapped in tests
func defaultGitRevParseRunner(ctx context.Context, clonePath, ref string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", ref) //nolint:gosec // trusted config
	cmd.Dir = clonePath
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("rev-parse %s: %w", ref, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// gitPullFFRunner executes `git pull --ff-only origin <branch>` in the dir
// under withFetchLock — a pull mutates refs and the working tree, so it must
// serialize against concurrent fetches in the same clone. Used by
// source_sync for the SOURCE checkout; the deploy-clone path does NOT use
// this — a pull fetches a second time, which can race origin forward past
// the ref this pull sequence verified. pullDeployClone merges the exact
// fetched SHA instead (gitMergeFFRunner). Replaceable in tests.
var gitPullFFRunner = defaultGitPullFFRunner

//nolint:unused // DI default seam — assigned to var gitPullFFRunner, swapped in tests
func defaultGitPullFFRunner(ctx context.Context, clonePath, branch string) error {
	return withFetchLock(ctx, clonePath, func() error {
		cmd := exec.CommandContext(ctx, "git", "pull", "--ff-only", "origin", branch) //nolint:gosec // trusted config
		cmd.Dir = clonePath
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%w: %s", err, tail(string(out), maxOutputLen))
		}
		return nil
	})
}

// gitMergeFFRunner executes `git merge --ff-only <sha>` in clonePath —
// fast-forwards HEAD to the EXACT commit the fetch in this pull sequence
// resolved, with no second fetch. Never takes the fetch lock: its only
// caller (pullDeployClone) holds ONE lock across fetch → rev-parse → merge.
// Replaceable in tests.
var gitMergeFFRunner = defaultGitMergeFFRunner

//nolint:unused // DI default seam — assigned to var gitMergeFFRunner, swapped in tests
func defaultGitMergeFFRunner(ctx context.Context, clonePath, sha string) error {
	cmd := exec.CommandContext(ctx, "git", "merge", "--ff-only", sha) //nolint:gosec // sha resolved from FETCH_HEAD by the caller
	cmd.Dir = clonePath
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, tail(string(out), maxOutputLen))
	}
	return nil
}

// gitMergeBaseRunner executes `git merge-base --is-ancestor <ancestor>
// <descendant>` in dir: nil iff ancestor is an ancestor of descendant
// (inclusive — equal commits count). The pre-up clone check uses it to tell
// a legitimate forward fast-forward (origin advanced between build and up)
// from a non-descendant move. Replaceable in tests.
var gitMergeBaseRunner = defaultGitMergeBaseRunner

//nolint:unused // DI default seam — assigned to var gitMergeBaseRunner, swapped in tests
func defaultGitMergeBaseRunner(ctx context.Context, dir, ancestor, descendant string) error {
	cmd := exec.CommandContext(ctx, "git", "merge-base", "--is-ancestor", ancestor, descendant) //nolint:gosec // internal refs
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, tail(string(out), maxOutputLen))
	}
	return nil
}

// gitShortSHARunner executes `git rev-parse --short HEAD` in dir.
// Replaceable in tests.
var gitShortSHARunner = defaultGitShortSHARunner

//nolint:unused // DI default seam — assigned to var gitShortSHARunner, swapped in tests
func defaultGitShortSHARunner(ctx context.Context, dir string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--short", "HEAD") //nolint:gosec // trusted config
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse --short HEAD: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// classifyPorcelain splits `git status --porcelain` output into tracked
// modifications vs untracked files. Only tracked changes block the pull —
// untracked files (agent-written plans/reports in the deploy clone) were
// causing ~18 false dirty-skip WARNs/day while the clone silently went stale.
func classifyPorcelain(out string) (tracked, untracked int) {
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if strings.HasPrefix(line, "??") {
			untracked++
		} else {
			tracked++
		}
	}
	return tracked, untracked
}

// trackedFiles extracts the modified paths from `git status --porcelain`
// output for the dirty-clone refusal message (porcelain v1: two status
// columns + space, then path; "R  old -> new" keeps both names). Untracked
// "??" entries are excluded — they never block.
func trackedFiles(porcelain string) []string {
	var files []string
	for _, line := range strings.Split(porcelain, "\n") {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "??") {
			continue
		}
		path := line
		if len(line) > 3 { //nolint:mnd // porcelain v1 path offset
			path = strings.TrimSpace(line[3:])
		}
		files = append(files, path)
	}
	return files
}

// cloneVerify is the build-time deploy-clone verification record: the state
// the clone was proven to be in when the build ran. The pre-up re-check
// (verifyDeployCloneForUp) compares the clone's CURRENT state against this
// record — fetch is never repeated at up time, so fetchedSHA is the
// origin/<branch> ref captured at build-time fetch.
type cloneVerify struct {
	path       string // DeployClonePath
	branch     string // resolved deploy_clone_branch
	fetchedSHA string // FETCH_HEAD after the build-time fetch (== origin/<branch> then)
	headSHA    string // HEAD after the build-time pull/ff
}

// refusalPending remembers which repos' dozor_pending_deploy=1 came from a
// clone REFUSAL rather than the deploy_on:manual gate, so a later successful
// deploy clears the series even for non-manual repos (the manual-gate clear
// path only fires for manual repos). Guarded by pendingDeployMu; keyed by
// stripBranchSuffix(repo) like deployedSHAs.
var refusalPending = map[string]bool{}

// markDeployRefused sets dozor_pending_deploy=1 for the refused repo —
// refused means "nothing deployed; a deploy is owed" — and remembers the
// marker so the next successful deploy clears the series regardless of
// deploy_on.
func markDeployRefused(repo string, services []string) {
	setPendingDeploy(repo, services, 1)
	pendingDeployMu.Lock()
	refusalPending[stripBranchSuffix(repo)] = true
	pendingDeployMu.Unlock()
}

// clearDeployRefusal resets a refusal-set pending gauge after a successful
// deploy. No-op for repos with no refusal marker so non-manual repos don't
// accumulate dozor_pending_deploy=0 series they never needed.
func clearDeployRefusal(repo string, services []string) {
	key := stripBranchSuffix(repo)
	pendingDeployMu.Lock()
	marked := refusalPending[key]
	if marked {
		delete(refusalPending, key)
	}
	pendingDeployMu.Unlock()
	if marked {
		setPendingDeploy(repo, services, 0)
	}
}

// refuseDeployClone is the single refusal side-effect: bump the reason
// counter, mark the repo pending-deploy so the refusal is visible on the
// dashboard, and return the refusal message for the caller's error path.
func refuseDeployClone(repo, reason string, services []string, msg string) string {
	DeployCloneRefusedTotal.WithLabelValues(repo, reason).Inc()
	markDeployRefused(repo, services)
	return msg
}

// pullDeployClone verifies and refreshes the deploy clone before a compose
// build, FAIL-CLOSED (issue #239): the build must never again read compose
// files from a stale, dirty, or diverged tree — a skipped pull once
// recreated ox-browser from a stale compose and served 401s for ~2 minutes.
//
// Order matters: the fetch runs FIRST (even on a dirty tree) so the dirty
// check compares against fresh refs and the diverged refusal can name real
// SHAs. ONE withFetchLock covers fetch → rev-parse → merge: the old
// `git pull --ff-only origin <branch>` re-fetched inside a second lock
// scope, so origin could advance between the two fetches and the deploy
// would be refused as "diverged" on stale advice. The clone fast-forwards
// with `git merge --ff-only <exact FETCH_HEAD sha>` instead.
//
// The compared branch is the deploy clone's OWN configured branch
// (deploy_clone_branch, default main) — never the service repo's Branch:
// the clone is a different repository.
//
// On success it returns the verification record the caller threads to the
// pre-up re-check (verifyDeployCloneForUp); a non-empty errMsg is a
// deploy-failing refusal.
//
// Refusal table (each bumps dozor_deploy_clone_refused_total{repo,reason}):
//
//	fetch fails / clone unreadable → fetch_error
//	detached HEAD                  → detached
//	wrong branch checked out       → wrong_branch
//	tracked modifications          → dirty      (message names the files)
//	HEAD != fetched ref post-merge → diverged   (message names both SHAs)
func pullDeployClone(ctx context.Context, req BuildRequest) (*cloneVerify, string) {
	clonePath := req.Config.DeployClonePath
	if clonePath == "" {
		return nil, "" // no deploy clone configured — nothing to verify
	}
	var cv *cloneVerify
	var refuseMsg string
	if err := withFetchLock(ctx, clonePath, func() error {
		cv, refuseMsg = pullDeployCloneLocked(ctx, req, clonePath)
		return nil
	}); err != nil {
		slog.Warn("deploy: clone pull refused — fetch lock acquisition failed",
			"repo", req.Repo, "clone", clonePath, "error", err)
		return nil, refuseDeployClone(req.Repo, "fetch_error", req.Config.Services,
			fmt.Sprintf("deploy clone %s: fetch lock: %v", clonePath, err))
	}
	return cv, refuseMsg
}

// pullDeployCloneLocked is the verification pipeline, run inside
// pullDeployClone's single withFetchLock — gitFetchRunner, gitRevParseRunner
// and gitMergeFFRunner must NOT lock internally so the one lock spans all
// three.
func pullDeployCloneLocked(ctx context.Context, req BuildRequest, clonePath string) (*cloneVerify, string) {
	repo := req.Repo
	services := req.Config.Services
	branch := req.Config.deployCloneBranch()

	// 1. Fetch FIRST — before the dirty check — so a dirty clone still gets
	// fresh refs. #239: the old dirty-check-first order skipped the fetch and
	// the deploy silently used stale compose files.
	if err := gitFetchRunner(ctx, clonePath, branch); err != nil {
		slog.Warn("deploy: clone pull refused — git fetch failed",
			"repo", repo, "clone", clonePath, "branch", branch, "error", err)
		return nil, refuseDeployClone(repo, "fetch_error", services,
			fmt.Sprintf("deploy clone %s: git fetch origin/%s failed: %v", clonePath, branch, err))
	}

	// 2. A detached clone has no branch head to meaningfully compare —
	// refuse rather than guess.
	cur, err := gitCurrentBranchRunner(ctx, clonePath)
	if err != nil {
		slog.Warn("deploy: clone pull refused — cannot determine checked-out branch",
			"repo", repo, "clone", clonePath, "error", err)
		return nil, refuseDeployClone(repo, "fetch_error", services,
			fmt.Sprintf("deploy clone %s: cannot determine checked-out branch: %v", clonePath, err))
	}
	if cur == "" || cur == "HEAD" {
		slog.Warn("deploy: clone pull refused — detached HEAD",
			"repo", repo, "clone", clonePath, "branch", branch)
		return nil, refuseDeployClone(repo, "detached", services,
			fmt.Sprintf("deploy clone %s is on a detached HEAD — checkout %s in the deploy clone, then re-deploy",
				clonePath, branch))
	}
	// The clone must sit on its configured branch: on another branch, HEAD
	// belongs to a different line and the fetch/merge comparison against
	// origin/<branch> is meaningless — a checkout switch must be fixed by
	// the operator, never silently served.
	if cur != branch {
		slog.Warn("deploy: clone pull refused — wrong branch checked out",
			"repo", repo, "clone", clonePath, "configured", branch, "actual", cur)
		return nil, refuseDeployClone(repo, "wrong_branch", services,
			fmt.Sprintf("deploy clone %s is on branch %q, not %q — checkout %s in the deploy clone, then re-deploy",
				clonePath, cur, branch, branch))
	}

	// 3. Refuse on ANY tracked modification. Untracked files (agent-written
	// plans/reports) never block — a ff of tracked content cannot overwrite
	// them.
	statusOut, err := gitStatusRunner(ctx, clonePath)
	if err != nil {
		slog.Warn("deploy: clone pull refused — git status failed; clone unverifiable",
			"repo", repo, "clone", clonePath, "error", err)
		return nil, refuseDeployClone(repo, "fetch_error", services,
			fmt.Sprintf("deploy clone %s: git status failed: %v", clonePath, err))
	}
	if tracked, _ := classifyPorcelain(string(statusOut)); tracked > 0 {
		files := trackedFiles(string(statusOut))
		slog.Warn("deploy: clone pull refused — working tree is dirty",
			"repo", repo, "clone", clonePath, "branch", branch, "files", files)
		return nil, refuseDeployClone(repo, "dirty", services,
			fmt.Sprintf("deploy clone %s has uncommitted changes (%s) — commit or revert the deploy-clone changes, then re-deploy",
				clonePath, strings.Join(files, ", ")))
	}

	// 4. Compare the fetched ref with HEAD; fast-forward to the EXACT
	// fetched SHA when behind (`git merge --ff-only`, still under the fetch
	// lock — no second fetch). The post-merge HEAD == FETCH_HEAD comparison
	// is the verdict — it catches a failed ff, diverged history AND local
	// commits the remote lacks.
	remote, err := gitRevParseRunner(ctx, clonePath, "FETCH_HEAD")
	if err != nil {
		slog.Warn("deploy: clone pull refused — cannot resolve FETCH_HEAD",
			"repo", repo, "clone", clonePath, "error", err)
		return nil, refuseDeployClone(repo, "fetch_error", services,
			fmt.Sprintf("deploy clone %s: cannot resolve FETCH_HEAD: %v", clonePath, err))
	}
	head, err := gitRevParseRunner(ctx, clonePath, "HEAD")
	if err != nil {
		slog.Warn("deploy: clone pull refused — cannot resolve HEAD",
			"repo", repo, "clone", clonePath, "error", err)
		return nil, refuseDeployClone(repo, "fetch_error", services,
			fmt.Sprintf("deploy clone %s: cannot resolve HEAD: %v", clonePath, err))
	}

	outcome := pullUpToDate
	if head != remote {
		oldHead := head
		var merr error
		if merr = gitMergeFFRunner(ctx, clonePath, remote); merr != nil {
			slog.Warn("deploy: clone pull — ff-only merge of fetched sha failed (diverged history, local commits, or untracked-file collision)",
				"repo", repo, "clone", clonePath, "branch", branch, "sha", short(remote), "error", merr)
		}
		if head, err = gitRevParseRunner(ctx, clonePath, "HEAD"); err != nil {
			return nil, refuseDeployClone(repo, "fetch_error", services,
				fmt.Sprintf("deploy clone %s: cannot resolve HEAD after merge: %v", clonePath, err))
		}
		if head != remote {
			slog.Warn("deploy: clone pull refused — HEAD is not at the fetched origin/<branch> sha",
				"repo", repo, "clone", clonePath, "branch", branch,
				"head", short(head), "fetched", short(remote))
			return nil, refuseDeployClone(repo, "diverged", services,
				fmt.Sprintf("deploy clone %s diverged: HEAD=%s but fetched origin/%s=%s (merge --ff-only: %v) — reconcile the clone (commit or revert local commits), then re-deploy",
					clonePath, short(head), branch, short(remote), merr))
		}
		slog.Info("deploy: clone pull — fast-forwarded",
			"repo", repo, "clone", clonePath, "from", short(oldHead), "to", short(head))
		outcome = pullFastForward
	} else {
		slog.Info("deploy: clone pull — already up to date",
			"repo", repo, "clone", clonePath, "sha", short(head))
	}
	DeployClonePullTotal.WithLabelValues(repo, string(outcome)).Inc()
	return &cloneVerify{path: clonePath, branch: branch, fetchedSHA: remote, headSHA: head}, ""
}

// verifyDeployCloneForUp re-checks the deploy clone RIGHT BEFORE every
// `docker compose up` — the build-time verification can be up to 45 minutes
// old, and deploy-clone-sync (or a human) can dirty/move the clone in
// between. It performs LOCAL-ONLY checks — the fetch is never repeated at up
// time; the baseline is the clone's local refs/remotes/origin/<branch> ref
// as the last fetch left it, plus the build-time cv.headSHA.
//
// A clone is ACCEPTED only when ALL of these hold:
//
//	clean working tree (no tracked modifications)
//	attached HEAD on the configured deploy_clone_branch
//	HEAD unmoved since the build (== cv.headSHA) — the compose is exactly
//	  what the build verified, so the local origin/<branch> ref is not
//	  consulted at all (#248)
//	or, when HEAD DID move: HEAD == refs/remotes/origin/<clone branch>
//	  (the local ref — no fetch) AND cv.headSHA is an ancestor of HEAD
//	  (git merge-base --is-ancestor)
//
// The unmoved-HEAD shortcut exists because the remote-tracking ref is not
// part of the verified state: deploy-clone-sync fetches outside the
// deploy's fetch lock and can advance refs/remotes/origin/<branch> without
// fast-forwarding HEAD (its merge can no-op on an untracked-file
// collision, or the fetch/merge window) — the compose files are still
// exactly the verified tree, so refusing as "moved" was wrong.
//
// The ancestor clause is what makes legitimate forward fast-forwards pass:
// a second queue worker, a manual server_deploy, or deploy-clone-sync can
// advance the clone to a NEWER origin/<branch> between build and up — the
// up then deploys that newer (still origin-verified) tree and reports the
// CURRENT sha. A HEAD that merely MOVED is still refused:
//
//	tracked modifications                   → dirty      (message names the files)
//	detached HEAD                           → detached
//	on a different branch                   → wrong_branch
//	moved HEAD != local origin/<branch>     → moved      (local commit, checkout, reset)
//	moved HEAD not a descendant of build    → moved      (rewritten/rebased origin)
//	git ops fail                            → fetch_error
//
// A nil cv (no clone configured, from_disk, or allow_stale_config) skips the
// check entirely and returns ("", "").
//
// Returns (refuseMsg, upSHA): a non-empty refuseMsg is a deploy-failing
// refusal (side effects: refusal counter + pending-deploy marker); upSHA is
// the clone's CURRENT verified HEAD for the compose@<sha> receipt — the sha
// the up actually runs against, which differs from cv.headSHA after a
// forward fast-forward.
func verifyDeployCloneForUp(ctx context.Context, repo string, services []string, cv *cloneVerify) (refuseMsg, upSHA string) {
	if cv == nil {
		return "", ""
	}
	statusOut, err := gitStatusRunner(ctx, cv.path)
	if err != nil {
		return refuseDeployClone(repo, "fetch_error", services,
			fmt.Sprintf("deploy clone %s: git status failed before up: %v", cv.path, err)), ""
	}
	if tracked, _ := classifyPorcelain(string(statusOut)); tracked > 0 {
		files := trackedFiles(string(statusOut))
		slog.Warn("deploy: clone up-check refused — working tree is dirty",
			"repo", repo, "clone", cv.path, "files", files)
		return refuseDeployClone(repo, "dirty", services,
			fmt.Sprintf("deploy clone %s has uncommitted changes (%s) — commit or revert the deploy-clone changes, then re-deploy",
				cv.path, strings.Join(files, ", "))), ""
	}
	cur, err := gitCurrentBranchRunner(ctx, cv.path)
	if err != nil {
		return refuseDeployClone(repo, "fetch_error", services,
			fmt.Sprintf("deploy clone %s: cannot determine checked-out branch before up: %v", cv.path, err)), ""
	}
	if cur == "" || cur == "HEAD" {
		slog.Warn("deploy: clone up-check refused — detached HEAD",
			"repo", repo, "clone", cv.path)
		return refuseDeployClone(repo, "detached", services,
			fmt.Sprintf("deploy clone %s is on a detached HEAD — checkout %s in the deploy clone, then re-deploy", cv.path, cv.branch)), ""
	}
	if cur != cv.branch {
		slog.Warn("deploy: clone up-check refused — wrong branch checked out",
			"repo", repo, "clone", cv.path, "configured", cv.branch, "actual", cur)
		return refuseDeployClone(repo, "wrong_branch", services,
			fmt.Sprintf("deploy clone %s is on branch %q, not %q — checkout %s in the deploy clone, then re-deploy",
				cv.path, cur, cv.branch, cv.branch)), ""
	}
	head, err := gitRevParseRunner(ctx, cv.path, "HEAD")
	if err != nil {
		return refuseDeployClone(repo, "fetch_error", services,
			fmt.Sprintf("deploy clone %s: cannot resolve HEAD before up: %v", cv.path, err)), ""
	}
	// A HEAD that has not moved since the build is exactly the tree the
	// build verified — accept it without consulting the local
	// origin/<branch> ref. That ref is not part of the verified state:
	// deploy-clone-sync fetches outside the deploy's fetch lock and can
	// advance refs/remotes/origin/<branch> without fast-forwarding HEAD
	// (its merge no-ops on an untracked-file collision, or in the
	// fetch/merge window) — #248 refused that verified tree as "moved".
	if head == cv.headSHA {
		return "", head
	}
	// HEAD moved: it must now sit exactly at the clone's local
	// origin/<branch> ref — the state the last fetch (build-time pull,
	// deploy-clone-sync, or another worker's pull) left verified at the
	// remote tip. A HEAD anywhere else is a local move: a local commit, a
	// checkout of another ref, or a reset — the compose files would not be
	// origin-verified.
	originRef := "refs/remotes/origin/" + cv.branch
	remote, err := gitRevParseRunner(ctx, cv.path, originRef)
	if err != nil {
		return refuseDeployClone(repo, "fetch_error", services,
			fmt.Sprintf("deploy clone %s: cannot resolve %s before up: %v", cv.path, originRef, err)), ""
	}
	if head != remote {
		slog.Warn("deploy: clone up-check refused — HEAD is not at the local origin/<branch> ref",
			"repo", repo, "clone", cv.path, "branch", cv.branch,
			"at_build", short(cv.headSHA), "now", short(head), "origin_ref", short(remote))
		return refuseDeployClone(repo, "moved", services,
			fmt.Sprintf("deploy clone %s: HEAD %s is not at the local origin/%s ref %s (verified at build: %s) — a local commit, checkout, or reset moved the clone; reconcile it back to origin/%s, then re-deploy",
				cv.path, short(head), cv.branch, short(remote), short(cv.headSHA), cv.branch)), ""
	}
	// The clone sits at a NEWER origin/<branch> than the build verified.
	// Accept only a forward fast-forward — the build-time commit must be
	// an ancestor of the current HEAD. A rewritten/rebased origin leaves
	// a non-descendant HEAD: refuse, the compose files no longer derive
	// from the verified line.
	if err := gitMergeBaseRunner(ctx, cv.path, cv.headSHA, head); err != nil {
		slog.Warn("deploy: clone up-check refused — HEAD moved to a non-descendant of the build-verified commit",
			"repo", repo, "clone", cv.path, "branch", cv.branch,
			"at_build", short(cv.headSHA), "now", short(head), "error", err)
		return refuseDeployClone(repo, "moved", services,
			fmt.Sprintf("deploy clone %s: HEAD moved from %s to %s between build and up and is not a descendant of the build-verified commit (%v) — origin/%s was likely rewritten; reconcile the clone, then re-deploy",
				cv.path, short(cv.headSHA), short(head), err, cv.branch)), ""
	}
	slog.Info("deploy: clone up-check — clone fast-forwarded to a newer origin/<branch> between build and up; accepting",
		"repo", repo, "clone", cv.path, "branch", cv.branch,
		"at_build", short(cv.headSHA), "now", short(head))
	return "", head
}

// resolveGitSHA returns the short SHA of HEAD in dir.
// Falls back to "unknown" with a WARN log on any error.
func resolveGitSHA(ctx context.Context, dir string) string {
	if dir == "" {
		return "unknown"
	}
	sha, err := gitShortSHARunner(ctx, dir)
	if err != nil {
		slog.Warn("deploy: cannot resolve git SHA for build-arg", "dir", dir, "error", err)
		return "unknown"
	}
	return sha
}
