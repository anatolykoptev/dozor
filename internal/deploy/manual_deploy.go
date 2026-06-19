package deploy

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"
)

// ManualDeployRequest describes a manual deploy triggered via server_deploy MCP tool.
// Unlike webhook-driven BuildRequest, the source of truth is the configured branch
// in deploy-repos.yaml — NOT the on-disk HEAD of the source clone.
type ManualDeployRequest struct {
	// Repo is the full GitHub repo name ("owner/name"), matching a key in deploy-repos.yaml.
	// Empty when the projectPath is not configured (ad-hoc fallback).
	Repo string
	// Config is the resolved RepoConfig from deploy-repos.yaml.
	Config RepoConfig
	// FromDisk, when true, skips git worktree pinning and builds from the
	// on-disk source clone as-is. Intended for local debugging only.
	// Always log a WARN when this flag is set so operators can tell it apart.
	FromDisk bool
}

// ManualDeployResult is returned synchronously from ExecuteManualDeploy.
type ManualDeployResult struct {
	Success  bool
	BuiltSHA string // short SHA of the commit that was actually built
	Error    string
}

// gitManualFetchRunner wraps the git fetch step for the manual path.
// Seam for unit tests — defaults to the shared runCmd runner.
var gitManualFetchRunner = func(ctx context.Context, sourcePath, branch string) error {
	return runCmd(ctx, sourcePath, "git", "fetch", "origin", branch, "--no-tags", "--quiet")
}

// gitManualCurrentBranchRunner returns the source clone's checked-out branch.
// Seam for unit tests.
var gitManualCurrentBranchRunner = defaultGitCurrentBranchRunner

// ExecuteManualDeploy runs a fully synchronous manual deploy, routing through
// the same kind-aware builders as the webhook path.
//
// For a configured repo (req.FromDisk==false), the deploy strategy mirrors
// queue_build.go's executeBuild dispatch on resolvedKind():
//
//	KindStatic  — fetch origin/<branch>, run StaticDeployScript via executeStaticBuild.
//	KindBinary  — run executeBinaryBuild (git pull + BuildCmd + systemd restart).
//	KindCompose — fetch origin/<branch>, create a detached worktree at origin/<branch>,
//	              composeBuild (injects OXPULSE_GIT_SHA) → composeUp → cleanup.
//
// For all configured non-from_disk paths:
//  1. Fetch origin/<branch> on SourcePath (never modifies the working tree).
//     Binary kind skips this step — executeBinaryBuild does its own git pull.
//  2. Detect branch drift: if SourcePath HEAD ≠ origin/<branch>, log WARN +
//     bump dozor_manual_deploy_branch_mismatch_total (the build still proceeds
//     from the correct ref — the drift is surfaced, not fatal).
//     Binary kind skips drift detection (no fixed worktree HEAD to compare).
//  3. Compose only: build a detached worktree at origin/<branch> via
//     gitPrepareBranch, which always targets the remote tracking ref.
//  4. Compose only: inject OXPULSE_GIT_SHA via composeBuild (same as webhook path).
//  5. Compose only: clean up the worktree.
//
// For req.FromDisk==true (debug opt-out):
//
//	Skips steps 1-3 and passes an empty worktreePath to composeBuild,
//	which falls back to the on-disk tree. Log a WARN so it is visible.
//	Only valid for KindCompose repos.
//
// The caller is expected to run this in a goroutine and write the log to a
// temp file — see StartManualDeploy in internal/engine/deploy.go.
func ExecuteManualDeploy(ctx context.Context, req ManualDeployRequest) ManualDeployResult {
	branch := req.Config.Branch
	if branch == "" {
		branch = defaultBranch
	}
	sourcePath := req.Config.SourcePath

	result := ManualDeployResult{}

	if req.FromDisk {
		slog.Warn("deploy/manual: from_disk=true — building on-disk source tree (debug mode, not SHA-pinned)",
			"repo", req.Repo,
			"source_path", sourcePath,
		)
		ManualDeployTotal.WithLabelValues(req.Repo, "from_disk", "started").Inc()
		buildReq := BuildRequest{
			Repo:      req.Repo,
			CommitSHA: "", // resolveGitSHA reads HEAD of sourcePath at build time
			Config:    req.Config,
		}
		if errMsg := composeBuild(ctx, buildReq, ""); errMsg != "" {
			ManualDeployTotal.WithLabelValues(req.Repo, "from_disk", "failure").Inc()
			result.Error = errMsg
			return result
		}
		if errMsg := composeUp(ctx, buildReq); errMsg != "" {
			ManualDeployTotal.WithLabelValues(req.Repo, "from_disk", "failure").Inc()
			result.Error = errMsg
			return result
		}
		result.BuiltSHA = resolveGitSHA(ctx, sourcePath)
		ManualDeployTotal.WithLabelValues(req.Repo, "from_disk", "success").Inc()
		result.Success = true
		return result
	}

	// --- Kind-aware dispatch (mirrors queue_build.go executeBuild) ---

	switch req.Config.resolvedKind() {
	case KindStatic:
		return executeManualStaticDeploy(ctx, req, branch, sourcePath)
	case KindBinary:
		return executeManualBinaryDeploy(ctx, req)
	}

	// KindCompose: SHA-pinned from origin/<branch>.
	return executeManualComposeDeploy(ctx, req, branch, sourcePath)
}

// executeManualStaticDeploy handles KindStatic manual deploys:
//  1. Fetch origin/<branch> so SourcePath is fresh.
//  2. Drift guard (informational, build always uses origin/<branch> via the script's env).
//  3. Run StaticDeployScript with DEPLOY_REPO_PATH=SourcePath and DEPLOY_SHA=<sha at origin/branch>.
func executeManualStaticDeploy(ctx context.Context, req ManualDeployRequest, branch, sourcePath string) ManualDeployResult {
	result := ManualDeployResult{}

	// Step 1: fetch so origin/<branch> is fresh.
	if err := gitManualFetchRunner(ctx, sourcePath, branch); err != nil {
		slog.Error("deploy/manual: git fetch failed",
			"repo", req.Repo,
			"source_path", sourcePath,
			"branch", branch,
			"error", err,
		)
		ManualDeployTotal.WithLabelValues(req.Repo, "sha_pinned", "failure").Inc()
		result.Error = fmt.Sprintf("git fetch origin %s: %v", branch, err)
		return result
	}

	// Step 2: drift guard.
	cloneBranch, err := gitManualCurrentBranchRunner(ctx, sourcePath)
	if err != nil {
		slog.Debug("deploy/manual: cannot read source clone branch (drift guard skipped)",
			"repo", req.Repo, "error", err)
	} else if cloneBranch != "" && cloneBranch != "HEAD" && cloneBranch != branch {
		slog.Warn("deploy/manual: source clone branch drift detected; build will use origin/<configured> regardless",
			"repo", req.Repo,
			"source_path", sourcePath,
			"configured_branch", branch,
			"actual_branch", cloneBranch,
		)
		ManualDeployBranchMismatchTotal.WithLabelValues(req.Repo, branch, cloneBranch).Inc()
	}

	// Step 3: resolve the SHA at origin/<branch> for DEPLOY_SHA.
	// We read it from the fetch-updated remote-tracking ref rather than
	// the on-disk HEAD, so the script always sees the pinned ref.
	sha := resolveGitSHA(ctx, sourcePath)
	result.BuiltSHA = sha

	slog.Info("deploy/manual: running static deploy script",
		"repo", req.Repo,
		"branch", branch,
		"sha", sha,
		"script", req.Config.StaticDeployScript,
	)

	buildReq := BuildRequest{
		Repo:      req.Repo,
		CommitSHA: sha,
		Config:    req.Config,
	}
	br := executeStaticBuild(ctx, buildReq)
	if !br.Success {
		ManualDeployTotal.WithLabelValues(req.Repo, "sha_pinned", "failure").Inc()
		result.Error = br.Error
		return result
	}

	ManualDeployTotal.WithLabelValues(req.Repo, "sha_pinned", "success").Inc()
	result.Success = true
	return result
}

// executeManualBinaryDeploy handles KindBinary manual deploys.
// executeBinaryBuild does its own git pull + build + systemd restart — no
// separate fetch or worktree step is needed here.
func executeManualBinaryDeploy(ctx context.Context, req ManualDeployRequest) ManualDeployResult {
	result := ManualDeployResult{}

	slog.Info("deploy/manual: running binary deploy",
		"repo", req.Repo,
		"source_path", req.Config.SourcePath,
		"build_cmd", req.Config.BuildCmd,
	)

	buildReq := BuildRequest{
		Repo:   req.Repo,
		Config: req.Config,
	}
	br := executeBinaryBuild(ctx, buildReq)
	if !br.Success {
		ManualDeployTotal.WithLabelValues(req.Repo, "sha_pinned", "failure").Inc()
		result.Error = br.Error
		return result
	}

	// Resolve SHA post-pull so BuiltSHA reflects what was actually built.
	result.BuiltSHA = resolveGitSHA(ctx, req.Config.SourcePath)
	ManualDeployTotal.WithLabelValues(req.Repo, "sha_pinned", "success").Inc()
	result.Success = true
	return result
}

// executeManualComposeDeploy handles KindCompose manual deploys:
//  1. Fetch origin/<branch> on SourcePath.
//  2. Detect branch drift (informational).
//  3. Create a detached worktree at origin/<branch> via gitPrepareBranch,
//     which always targets the remote tracking ref exactly.
//  4. Build via composeBuild (injects OXPULSE_GIT_SHA from the worktree HEAD).
//  5. Bring containers up via composeUp.
//  6. Defer worktree cleanup.
func executeManualComposeDeploy(ctx context.Context, req ManualDeployRequest, branch, sourcePath string) ManualDeployResult {
	result := ManualDeployResult{}

	// Step 1: fetch so origin/<branch> is fresh.
	if err := gitManualFetchRunner(ctx, sourcePath, branch); err != nil {
		slog.Error("deploy/manual: git fetch failed",
			"repo", req.Repo,
			"source_path", sourcePath,
			"branch", branch,
			"error", err,
		)
		ManualDeployTotal.WithLabelValues(req.Repo, "sha_pinned", "failure").Inc()
		result.Error = fmt.Sprintf("git fetch origin %s: %v", branch, err)
		return result
	}

	// Step 2: drift guard — source clone's checked-out branch vs configured branch.
	cloneBranch, err := gitManualCurrentBranchRunner(ctx, sourcePath)
	if err != nil {
		slog.Debug("deploy/manual: cannot read source clone branch (drift guard skipped)",
			"repo", req.Repo, "error", err)
	} else if cloneBranch != "" && cloneBranch != "HEAD" && cloneBranch != branch {
		slog.Warn("deploy/manual: source clone branch drift detected; build will use origin/<configured> regardless",
			"repo", req.Repo,
			"source_path", sourcePath,
			"configured_branch", branch,
			"actual_branch", cloneBranch,
		)
		ManualDeployBranchMismatchTotal.WithLabelValues(req.Repo, branch, cloneBranch).Inc()
	}

	// Step 3: create a detached worktree at origin/<branch>.
	// gitPrepareBranch always builds "origin/<branch>" as the target ref,
	// so the manual path is pinned to exactly what origin holds — never to
	// whatever the local clone happens to have checked out.
	worktreePath, cleanup, errMsg := gitPrepareBranch(ctx, sourcePath, branch)
	if errMsg != "" {
		ManualDeployTotal.WithLabelValues(req.Repo, "sha_pinned", "failure").Inc()
		result.Error = errMsg
		return result
	}
	defer cleanup()

	// Step 4: build via the same composeBuild path (injects OXPULSE_GIT_SHA).
	buildReq := BuildRequest{
		Repo:      req.Repo,
		CommitSHA: resolveGitSHA(ctx, worktreePath), // short SHA at worktree HEAD
		Config:    req.Config,
	}
	result.BuiltSHA = buildReq.CommitSHA

	slog.Info("deploy/manual: building sha-pinned worktree",
		"repo", req.Repo,
		"branch", branch,
		"worktree", worktreePath,
		"sha", result.BuiltSHA,
	)

	if errMsg := composeBuild(ctx, buildReq, worktreePath); errMsg != "" {
		ManualDeployTotal.WithLabelValues(req.Repo, "sha_pinned", "failure").Inc()
		result.Error = errMsg
		return result
	}

	// Step 5: bring containers up.
	if errMsg := composeUp(ctx, buildReq); errMsg != "" {
		ManualDeployTotal.WithLabelValues(req.Repo, "sha_pinned", "failure").Inc()
		result.Error = errMsg
		return result
	}

	ManualDeployTotal.WithLabelValues(req.Repo, "sha_pinned", "success").Inc()
	result.Success = true
	return result
}

// gitPrepareBranch creates a detached worktree at origin/<branch> in the
// source clone. Unlike gitPrepare (which resolves a SHA), this always targets
// the remote tracking ref — ensuring the manual path builds exactly what
// origin holds regardless of the local clone's checkout state.
func gitPrepareBranch(ctx context.Context, sourcePath, branch string) (worktreePath string, cleanup func(), errMsg string) {
	noop := func() {}
	if sourcePath == "" {
		return "", noop, "source_path is empty — cannot create worktree"
	}

	target := "origin/" + branch
	wtPath := fmt.Sprintf("/tmp/deploy-manual-%s-%d", branch, time.Now().UnixMilli())

	if err := runCmd(ctx, sourcePath, "git", "worktree", "add", "--detach", wtPath, target); err != nil {
		return "", noop, fmt.Sprintf("git worktree add (manual, origin/%s): %v", branch, err)
	}

	cleanupFn := func() {
		if err := runCmd(context.Background(), sourcePath, "git", "worktree", "remove", "--force", wtPath); err != nil {
			slog.Warn("deploy/manual: worktree cleanup failed, removing manually",
				"path", wtPath, "error", err)
			os.RemoveAll(wtPath)
		}
	}

	slog.Info("deploy/manual: worktree created", "path", wtPath, "target", target)
	return wtPath, cleanupFn, ""
}
