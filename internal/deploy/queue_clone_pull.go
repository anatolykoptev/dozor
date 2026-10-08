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
// Replaceable in tests.
var gitFetchRunner = defaultGitFetchRunner

//nolint:unused // DI default seam — assigned to var gitFetchRunner, swapped in tests
func defaultGitFetchRunner(ctx context.Context, clonePath, branch string) error {
	return withFetchLock(ctx, clonePath, func() error {
		cmd := exec.CommandContext(ctx, "git", "fetch", "origin", branch, "--no-tags", "--quiet") //nolint:gosec // trusted config
		cmd.Dir = clonePath
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%w: %s", err, tail(string(out), maxOutputLen))
		}
		return nil
	})
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

// gitPullFFRunner executes `git pull --ff-only origin <branch>` in clonePath
// under the same withFetchLock as the fetch — a pull mutates refs and the
// working tree, so it must serialize against concurrent fetches in the same
// clone. Replaceable in tests.
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
// Order matters: the fetch runs FIRST (under withFetchLock, even on a dirty
// tree) so the dirty check compares against fresh refs and the diverged
// refusal can name real SHAs. The ff pull runs under the same lock.
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
//	tracked modifications          → dirty      (message names the files)
//	HEAD != fetched ref post-pull  → diverged   (message names both SHAs)
func pullDeployClone(ctx context.Context, req BuildRequest) (*cloneVerify, string) {
	clonePath := req.Config.DeployClonePath
	if clonePath == "" {
		return nil, "" // no deploy clone configured — nothing to verify
	}
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

	// 4. Compare the fetched ref with HEAD; fast-forward when behind (under
	// the fetch lock). The post-pull HEAD == FETCH_HEAD comparison is the
	// verdict — it catches a failed ff, diverged history AND local commits
	// the remote lacks.
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
		if perr := gitPullFFRunner(ctx, clonePath, branch); perr != nil {
			slog.Warn("deploy: clone pull — ff-only pull failed (diverged history or local commits)",
				"repo", repo, "clone", clonePath, "branch", branch, "error", perr)
		}
		if head, err = gitRevParseRunner(ctx, clonePath, "HEAD"); err != nil {
			return nil, refuseDeployClone(repo, "fetch_error", services,
				fmt.Sprintf("deploy clone %s: cannot resolve HEAD after pull: %v", clonePath, err))
		}
		if head != remote {
			slog.Warn("deploy: clone pull refused — HEAD is not at origin/<branch>",
				"repo", repo, "clone", clonePath, "branch", branch,
				"head", short(head), "origin", short(remote))
			return nil, refuseDeployClone(repo, "diverged", services,
				fmt.Sprintf("deploy clone %s diverged: HEAD=%s but origin/%s=%s — reconcile the clone (commit or revert local commits), then re-deploy",
					clonePath, short(head), branch, short(remote)))
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
// time; the ref fetched at build time (cv.fetchedSHA) is the baseline:
//
//	tracked modifications          → dirty      (message names the files)
//	detached HEAD                  → detached
//	HEAD != build-time HEAD        → moved      (message names both SHAs)
//	git ops fail                   → fetch_error
//
// A verified clone satisfies HEAD == cv.headSHA == cv.fetchedSHA ==
// origin/<deploy_clone_branch>@build-time — the compose files the up reads
// are exactly the ones the build verified. A nil cv (no clone configured,
// from_disk, or allow_stale_config) skips the check entirely.
//
// A non-empty return is a deploy-failing refusal message; side effects are
// the refusal counter + pending-deploy marker, same as pullDeployClone.
func verifyDeployCloneForUp(ctx context.Context, repo string, services []string, cv *cloneVerify) string {
	if cv == nil {
		return ""
	}
	statusOut, err := gitStatusRunner(ctx, cv.path)
	if err != nil {
		return refuseDeployClone(repo, "fetch_error", services,
			fmt.Sprintf("deploy clone %s: git status failed before up: %v", cv.path, err))
	}
	if tracked, _ := classifyPorcelain(string(statusOut)); tracked > 0 {
		files := trackedFiles(string(statusOut))
		slog.Warn("deploy: clone up-check refused — working tree is dirty",
			"repo", repo, "clone", cv.path, "files", files)
		return refuseDeployClone(repo, "dirty", services,
			fmt.Sprintf("deploy clone %s has uncommitted changes (%s) — commit or revert the deploy-clone changes, then re-deploy",
				cv.path, strings.Join(files, ", ")))
	}
	cur, err := gitCurrentBranchRunner(ctx, cv.path)
	if err != nil {
		return refuseDeployClone(repo, "fetch_error", services,
			fmt.Sprintf("deploy clone %s: cannot determine checked-out branch before up: %v", cv.path, err))
	}
	if cur == "" || cur == "HEAD" {
		slog.Warn("deploy: clone up-check refused — detached HEAD",
			"repo", repo, "clone", cv.path)
		return refuseDeployClone(repo, "detached", services,
			fmt.Sprintf("deploy clone %s is on a detached HEAD — checkout %s in the deploy clone, then re-deploy", cv.path, cv.branch))
	}
	head, err := gitRevParseRunner(ctx, cv.path, "HEAD")
	if err != nil {
		return refuseDeployClone(repo, "fetch_error", services,
			fmt.Sprintf("deploy clone %s: cannot resolve HEAD before up: %v", cv.path, err))
	}
	if head != cv.headSHA {
		slog.Warn("deploy: clone up-check refused — HEAD moved between build and up",
			"repo", repo, "clone", cv.path,
			"at_build", short(cv.headSHA), "now", short(head))
		return refuseDeployClone(repo, "moved", services,
			fmt.Sprintf("deploy clone %s: HEAD moved from %s to %s between build and up — deploy-clone-sync or a local change advanced the clone; re-deploy so the build and up verify the same origin/%s",
				cv.path, short(cv.headSHA), short(head), cv.branch))
	}
	return ""
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
