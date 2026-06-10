package deploy

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
)

// pullOutcome classifies the result of a deploy-clone pull attempt.
type pullOutcome string

const (
	pullUpToDate     pullOutcome = "up_to_date"
	pullFastForward  pullOutcome = "fast_forward"
	pullDirtySkipped pullOutcome = "dirty_skipped"
	pullDiverged     pullOutcome = "diverged_skipped"
	pullError        pullOutcome = "error"

	defaultBranch = "main"
)

// gitStatusRunner executes `git status --porcelain` in clonePath.
// Replaceable in tests.
var gitStatusRunner = defaultGitStatusRunner

func defaultGitStatusRunner(ctx context.Context, clonePath string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain") //nolint:gosec // trusted config
	cmd.Dir = clonePath
	return cmd.Output()
}

// gitFetchRunner executes `git fetch origin <branch> --no-tags --quiet` in clonePath.
// Replaceable in tests.
var gitFetchRunner = defaultGitFetchRunner

func defaultGitFetchRunner(ctx context.Context, clonePath, branch string) error {
	cmd := exec.CommandContext(ctx, "git", "fetch", "origin", branch, "--no-tags", "--quiet") //nolint:gosec // trusted config
	cmd.Dir = clonePath
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, truncate(string(out), maxOutputLen))
	}
	return nil
}

// gitCurrentBranchRunner returns the clone's current branch (rev-parse --abbrev-ref HEAD).
// Replaceable in tests.
var gitCurrentBranchRunner = defaultGitCurrentBranchRunner

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

func defaultGitRevParseRunner(ctx context.Context, clonePath, ref string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", ref) //nolint:gosec // trusted config
	cmd.Dir = clonePath
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("rev-parse %s: %w", ref, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// gitPullFFRunner executes `git pull --ff-only origin <branch>` in clonePath.
// Replaceable in tests.
var gitPullFFRunner = defaultGitPullFFRunner

func defaultGitPullFFRunner(ctx context.Context, clonePath, branch string) error {
	cmd := exec.CommandContext(ctx, "git", "pull", "--ff-only", "origin", branch) //nolint:gosec // trusted config
	cmd.Dir = clonePath
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, truncate(string(out), maxOutputLen))
	}
	return nil
}

// gitShortSHARunner executes `git rev-parse --short HEAD` in dir.
// Replaceable in tests.
var gitShortSHARunner = defaultGitShortSHARunner

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

// pullDeployClone auto-pulls the deploy clone at clonePath to origin/<branch>
// before a compose build. It is a best-effort operation: failures are logged
// and counted but never abort the build.
//
// Decision table:
//
//	dirty working tree → pullDirtySkipped  (WARN log, build proceeds)
//	fetch fails        → pullError         (WARN log, build proceeds)
//	FETCH_HEAD == HEAD → pullUpToDate      (INFO log)
//	ff-only pull ok    → pullFastForward   (INFO log)
//	ff-only pull fails → pullDiverged      (WARN log, build proceeds)
func pullDeployClone(ctx context.Context, repo, clonePath, branch string) pullOutcome {
	if clonePath == "" {
		return pullUpToDate // no-op, nothing to do
	}
	if branch == "" {
		branch = defaultBranch
	}
	// The deploy clone tracks ITS OWN checked-out branch (e.g. krolik-server's
	// main), which can differ from the triggering repo's branch (e.g. an
	// oxpulse-chat `dev` deploy). Fetching the triggering repo's branch in a
	// different clone fails with "couldn't find remote ref". Prefer the clone's
	// actual branch; keep `branch` as the fallback for detached HEAD / errors.
	if cur, err := gitCurrentBranchRunner(ctx, clonePath); err == nil && cur != "" && cur != "HEAD" {
		branch = cur
	}

	// 1. Check for local modifications — never overwrite operator edits.
	statusOut, err := gitStatusRunner(ctx, clonePath)
	if err != nil {
		slog.Warn("deploy: clone pull — git status failed",
			"repo", repo, "clone", clonePath, "error", err)
		DeployClonePullTotal.WithLabelValues(repo, string(pullError)).Inc()
		return pullError
	}
	tracked, untracked := classifyPorcelain(string(statusOut))
	if tracked > 0 {
		slog.Warn("deploy: clone pull skipped — working tree is dirty; compose config may be stale",
			"repo", repo,
			"clone", clonePath,
			"dirty_tracked", tracked,
			"untracked", untracked,
			"status", truncate(string(statusOut), maxOutputLen),
		)
		DeployClonePullTotal.WithLabelValues(repo, string(pullDirtySkipped)).Inc()
		return pullDirtySkipped
	}
	if untracked > 0 {
		// Untracked files (agent-written plans/reports) cannot be overwritten
		// by a ff-only pull of tracked content — proceed. A rare name
		// collision with an incoming tracked file fails the pull below and
		// is logged as pullDiverged; the build still proceeds.
		slog.Debug("deploy: clone pull proceeding — untracked files present",
			"repo", repo, "untracked", untracked)
	}

	// 2. Fetch latest refs.
	if err := gitFetchRunner(ctx, clonePath, branch); err != nil {
		slog.Warn("deploy: clone pull — git fetch failed; proceeding with current state",
			"repo", repo, "clone", clonePath, "branch", branch, "error", err)
		DeployClonePullTotal.WithLabelValues(repo, string(pullError)).Inc()
		return pullError
	}

	// 3. Compare FETCH_HEAD with HEAD to detect no-op.
	fetchHead, err := gitRevParseRunner(ctx, clonePath, "FETCH_HEAD")
	if err != nil {
		slog.Warn("deploy: clone pull — cannot resolve FETCH_HEAD; proceeding",
			"repo", repo, "clone", clonePath, "error", err)
		DeployClonePullTotal.WithLabelValues(repo, string(pullError)).Inc()
		return pullError
	}
	head, err := gitRevParseRunner(ctx, clonePath, "HEAD")
	if err != nil {
		slog.Warn("deploy: clone pull — cannot resolve HEAD; proceeding",
			"repo", repo, "clone", clonePath, "error", err)
		DeployClonePullTotal.WithLabelValues(repo, string(pullError)).Inc()
		return pullError
	}
	if fetchHead == head {
		slog.Info("deploy: clone pull — already up to date",
			"repo", repo, "clone", clonePath, "sha", short(head))
		DeployClonePullTotal.WithLabelValues(repo, string(pullUpToDate)).Inc()
		return pullUpToDate
	}

	// 4. Fast-forward pull.
	if err := gitPullFFRunner(ctx, clonePath, branch); err != nil {
		slog.Warn("deploy: clone pull — ff-only failed (diverged?); proceeding with current state",
			"repo", repo, "clone", clonePath, "branch", branch, "error", err)
		DeployClonePullTotal.WithLabelValues(repo, string(pullDiverged)).Inc()
		return pullDiverged
	}

	newHead, _ := gitRevParseRunner(ctx, clonePath, "HEAD")
	slog.Info("deploy: clone pull — fast-forwarded",
		"repo", repo, "clone", clonePath, "from", short(head), "to", short(newHead))
	DeployClonePullTotal.WithLabelValues(repo, string(pullFastForward)).Inc()
	return pullFastForward
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
