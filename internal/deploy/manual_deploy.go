package deploy

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
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
	// AllowStaleConfig, when true, skips deploy-clone verification entirely
	// (server_deploy allow_stale_config — issue #239): no fetch, no pull, no
	// pre-up re-check; the compose files are used as they sit on disk. The
	// deploy proceeds with a WARN + override counter + a
	// "STALE CONFIG OVERRIDE" marker in the reply.
	AllowStaleConfig bool
	// NoBuild honours server_deploy build=false (compose repos only): skip
	// the worktree build entirely and run `up --no-build`, recreating
	// services from the CURRENT image — the compose file still comes from
	// the (verified) deploy clone, so this is the fast path for a
	// config-only change. No receipt SHA is advanced.
	NoBuild bool
}

// ManualDeployResult is returned synchronously from ExecuteManualDeploy.
type ManualDeployResult struct {
	Success  bool
	BuiltSHA string // short SHA of the commit that was actually built; empty on a no-build deploy
	Error    string
	// ComposeSHA is the deploy clone's verified HEAD the `up` ran against
	// (compose@<sha>, #239). Empty when no clone verification applied.
	ComposeSHA string
}

// gitManualFetchRunner wraps the git fetch step for the manual path.
// Seam for unit tests — defaults to the shared runCmd runner.
var gitManualFetchRunner = func(ctx context.Context, sourcePath, branch string) error {
	return withFetchLock(ctx, sourcePath, func() error {
		return runCmd(ctx, sourcePath, "git", "fetch", "origin", branch, "--no-tags", "--quiet")
	})
}

// gitManualCurrentBranchRunner returns the source clone's checked-out branch.
// Seam for unit tests.
var gitManualCurrentBranchRunner = defaultGitCurrentBranchRunner

// gitManualOriginSHARunner resolves the FULL 40-char SHA of origin/<branch> in dir.
// Unlike gitShortSHARunner (which reads HEAD), this reads the remote-tracking
// ref — so the value is truthful even when the on-disk HEAD lags behind the
// most recent fetch. Returns the FULL SHA (not --short) so CommitSHA is
// consistent with the webhook lane (which receives a full SHA from the GitHub
// payload).
// Seam for unit tests.
var gitManualOriginSHARunner = defaultGitManualOriginSHARunner

//nolint:unused // DI default seam — assigned to var gitManualOriginSHARunner, swapped in tests
func defaultGitManualOriginSHARunner(ctx context.Context, dir, branch string) (string, error) {
	ref := "origin/" + branch
	cmd := exec.CommandContext(ctx, "git", "rev-parse", ref) //nolint:gosec // trusted config
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse %s: %w", ref, err)
	}
	return strings.TrimSpace(string(out)), nil
}

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
//	Image caching is explicitly skipped (with a logged reason) because the
//	on-disk working tree may have uncommitted changes — the tree hash of HEAD
//	is not a trustworthy content-address for what the build actually produced.
//	Only valid for KindCompose repos.
//
// ExecuteManualDeploy runs a fully synchronous manual deploy, routing through
// the same kind-aware builders as the webhook path. It is the explicit human
// path (server_deploy MCP tool) and is NEVER gated by deploy_on: manual — the
// gate lives in executeBuild (the automatic webhook/queue path), which this
// function does not call. On a successful deploy of a deploy_on: manual repo,
// the pending-deploy gauge (dozor_pending_deploy) is cleared back to 0 — the
// artifact that was held ready is now deployed (issue #183 half 2).
//
// The caller is expected to run this in a goroutine and write the log to a
// temp file — see StartManualDeploy in internal/engine/deploy.go.
func ExecuteManualDeploy(ctx context.Context, req ManualDeployRequest) ManualDeployResult {
	result := executeManualDeploy(ctx, req)
	if result.Success {
		if req.Config.DeployOn == deployOnManual {
			setPendingDeploy(req.Repo, req.Config.Services, 0)
			slog.Info("deploy/manual: pending-deploy gauge cleared (server_deploy completed)",
				"repo", req.Repo,
				"services", req.Config.Services,
			)
		}
		// A successful manual deploy also clears a refusal-set pending marker
		// on repos of ANY deploy_on — the refused deploy has now shipped.
		clearDeployRefusal(req.Repo, req.Config.Services)
	}
	return result
}

func executeManualDeploy(ctx context.Context, req ManualDeployRequest) ManualDeployResult {
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
		// from_disk builds the on-disk working tree, which may have uncommitted
		// changes. The tree hash of HEAD would NOT reliably be the build's
		// content-address, so publishing under a tree-hash tag would violate
		// the invariant: an image is only published under a tree-hash tag if
		// it is the image THAT build produced. Log the skip explicitly so it
		// is never mistaken for a cache miss.
		if req.Config.ImageCache.Registry != "" {
			slog.Info("deploy/manual: image cache skipped — from_disk builds the on-disk working tree which may have uncommitted changes; tree hash is not a trustworthy content-address for the built image",
				"repo", req.Repo)
		}
		ManualDeployTotal.WithLabelValues(req.Repo, "from_disk", "started").Inc()
		buildReq := BuildRequest{
			Repo:      req.Repo,
			CommitSHA: resolveGitFullSHA(ctx, sourcePath), // full SHA so DEPLOY_SHA and ${SHA} are valid
			Config:    req.Config,
			FromDisk:  true,
		}
		if req.Config.Heavy {
			waitForLoadBelowThreshold(ctx)
			release := acquireCrossLaneLock(ctx, req.Repo, buildReq.CommitSHA)
			defer release()
		}
		// from_disk builds must never push to the image cache (no worktree,
		// no treeHash) — the push stays gated on builtNow for clarity.
		if errMsg, _, _ := composeBuild(ctx, buildReq, "", ""); errMsg != "" {
			ManualDeployTotal.WithLabelValues(req.Repo, "from_disk", "failure").Inc()
			result.Error = errMsg
			return result
		}
		// from_disk verifies nothing — the clone check is skipped by design,
		// so no verification record exists to thread to composeUp.
		if errMsg, composeSHA := composeUp(ctx, buildReq, nil); errMsg != "" {
			ManualDeployTotal.WithLabelValues(req.Repo, "from_disk", "failure").Inc()
			result.Error = errMsg
			return result
		} else {
			result.ComposeSHA = composeSHA
		}
		result.BuiltSHA = resolveGitSHA(ctx, sourcePath)
		ManualDeployTotal.WithLabelValues(req.Repo, "from_disk", "success").Inc()
		result.Success = true
		return result
	}

	// --- Kind-aware dispatch (mirrors queue_build.go executeBuild) ---

	if req.NoBuild {
		if req.Config.resolvedKind() != KindCompose {
			result.Error = "build=false is only supported for compose repos — binary/static deploys always build"
			return result
		}
		return executeManualNoBuildDeploy(ctx, req)
	}

	switch req.Config.resolvedKind() {
	case KindStatic:
		return executeManualStaticDeploy(ctx, req, branch, sourcePath)
	case KindBinary:
		return executeManualBinaryDeploy(ctx, req)
	}

	// KindCompose: SHA-pinned from origin/<branch>.
	return executeManualComposeDeploy(ctx, req, branch, sourcePath)
}

// executeManualNoBuildDeploy handles server_deploy build=false on a compose
// repo: skip the source fetch, worktree, and `compose build` entirely, and
// recreate services from the CURRENT image via `up --no-build`. The compose
// file still comes from the deploy clone, so the clone verification runs
// exactly as for a built deploy (unless allow_stale_config opts out). No
// deployed-SHA receipt is advanced — nothing was built.
func executeManualNoBuildDeploy(ctx context.Context, req ManualDeployRequest) ManualDeployResult {
	result := ManualDeployResult{}
	ManualDeployTotal.WithLabelValues(req.Repo, "no_build", "started").Inc()

	// Verify the deploy clone: the compose file `up` reads comes from it.
	// pullDeployClone does the fetch+verify and returns the baseline record;
	// composeUp re-verifies the clone again right before running (#239).
	var cv *cloneVerify
	if req.Config.DeployClonePath != "" {
		if req.AllowStaleConfig {
			slog.Warn("deploy: STALE CONFIG OVERRIDE — deploy-clone verification skipped by allow_stale_config",
				"repo", req.Repo, "clone", req.Config.DeployClonePath)
			DeployCloneRefusedTotal.WithLabelValues(req.Repo, "override").Inc()
		} else {
			var refuseMsg string
			cv, refuseMsg = pullDeployClone(ctx, BuildRequest{Repo: req.Repo, Config: req.Config})
			if refuseMsg != "" {
				ManualDeployTotal.WithLabelValues(req.Repo, "no_build", "failure").Inc()
				result.Error = refuseMsg
				return result
			}
		}
	}

	buildReq := BuildRequest{
		Repo:             req.Repo,
		Config:           req.Config,
		AllowStaleConfig: req.AllowStaleConfig,
		NoBuild:          true,
	}
	errMsg, composeSHA := composeUp(ctx, buildReq, cv)
	if errMsg != "" {
		ManualDeployTotal.WithLabelValues(req.Repo, "no_build", "failure").Inc()
		result.Error = errMsg
		return result
	}
	result.ComposeSHA = composeSHA
	ManualDeployTotal.WithLabelValues(req.Repo, "no_build", "success").Inc()
	result.Success = true
	return result
}

// executeManualStaticDeploy handles KindStatic manual deploys:
//  1. Fetch origin/<branch> so SourcePath is fresh.
//  2. Drift guard (informational, build always uses origin/<branch> via the script's env).
//  3. Run StaticDeployScript with DEPLOY_REPO_PATH=SourcePath and DEPLOY_SHA=<full sha at origin/<branch>>.
//     SHA is resolved via gitManualOriginSHARunner (git rev-parse origin/<branch>),
//     not from HEAD, so the value is truthful even when the on-disk clone lags.
//     The SHA is the FULL 40-char commit SHA, matching the webhook lane's
//     CommitSHA (from the GitHub payload) — see artifactTagSHA for why this
//     invariant is enforced.
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
	// We read it from the remote-tracking ref (origin/<branch>), not from
	// the on-disk HEAD — the preceding fetch only updates the remote-tracking
	// ref without moving HEAD, so reading HEAD here would return a stale SHA.
	var sha string
	if s, err := gitManualOriginSHARunner(ctx, sourcePath, branch); err != nil {
		slog.Warn("deploy/manual: cannot resolve origin SHA; falling back to HEAD",
			"repo", req.Repo, "branch", branch, "error", err)
		sha = resolveGitFullSHA(ctx, sourcePath)
	} else {
		sha = s
	}
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
//
// Note: the trigger label is "binary_pull" (not "sha_pinned") because
// executeBinaryBuild runs `git pull --ff-only` with no remote/branch args,
// relying on the clone's upstream config rather than pinning to origin/<branch>.
// A true origin/<branch> pin for the binary path is tracked as a follow-up
// (docs/roadmap.md — "binary kind: origin/<branch> pin").
func executeManualBinaryDeploy(ctx context.Context, req ManualDeployRequest) ManualDeployResult {
	result := ManualDeployResult{}
	branch := req.Config.Branch
	if branch == "" {
		branch = defaultBranch
	}
	sourcePath := req.Config.SourcePath

	// Drift guard — mirror what static/compose paths already do. executeBinaryBuild
	// builds whatever the on-disk upstream resolves to; if the clone is on the
	// wrong branch, the operator should know. Build is not blocked (informational).
	cloneBranch, err := gitManualCurrentBranchRunner(ctx, sourcePath)
	if err != nil {
		slog.Debug("deploy/manual: cannot read source clone branch (drift guard skipped)",
			"repo", req.Repo, "error", err)
	} else if cloneBranch != "" && cloneBranch != "HEAD" && cloneBranch != branch {
		slog.Warn("deploy/manual: binary clone branch drift detected; build uses clone upstream",
			"repo", req.Repo,
			"source_path", sourcePath,
			"configured_branch", branch,
			"actual_branch", cloneBranch,
		)
		ManualDeployBranchMismatchTotal.WithLabelValues(req.Repo, branch, cloneBranch).Inc()
	}

	slog.Info("deploy/manual: running binary deploy",
		"repo", req.Repo,
		"source_path", sourcePath,
		"build_cmd", req.Config.BuildCmd,
	)

	buildReq := BuildRequest{
		Repo:   req.Repo,
		Config: req.Config,
	}
	br := executeBinaryBuild(ctx, buildReq)
	if !br.Success {
		ManualDeployTotal.WithLabelValues(req.Repo, "binary_pull", "failure").Inc()
		result.Error = br.Error
		return result
	}

	// Resolve SHA post-pull so BuiltSHA reflects what was actually built.
	result.BuiltSHA = resolveGitSHA(ctx, sourcePath)
	ManualDeployTotal.WithLabelValues(req.Repo, "binary_pull", "success").Inc()
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
	// whatever the local clone happens to have checked out. It also resolves
	// the tree hash (HEAD^{tree} of the worktree) for image-cache tagging —
	// the same content-address the webhook path's gitPrepare uses.
	worktreePath, treeHash, cleanup, errMsg := gitPrepareBranch(ctx, sourcePath, branch)
	if errMsg != "" {
		ManualDeployTotal.WithLabelValues(req.Repo, "sha_pinned", "failure").Inc()
		result.Error = errMsg
		return result
	}
	defer cleanup()

	// Step 4: build via the same composeBuild path (injects OXPULSE_GIT_SHA).
	// treeHash enables the image-cache pull-before-build path (same as the
	// webhook path's executeBuild → composeBuild).
	buildReq := BuildRequest{
		Repo:             req.Repo,
		CommitSHA:        resolveGitFullSHA(ctx, worktreePath), // FULL 40-char SHA — matches webhook lane's CommitSHA
		Config:           req.Config,
		AllowStaleConfig: req.AllowStaleConfig,
	}
	result.BuiltSHA = buildReq.CommitSHA

	slog.Info("deploy/manual: building sha-pinned worktree",
		"repo", req.Repo,
		"branch", branch,
		"worktree", worktreePath,
		"sha", result.BuiltSHA,
		"tree_hash", treeHash,
	)

	// Same P3+P2 guards as the webhook lane's processBuild: a manual heavy
	// build must serialise against CI and auto-deploys too — running it
	// unlocked is the 3-concurrent-cargo-build scenario the lock exists to
	// prevent (issue #130). Fail-safe semantics identical to processBuild:
	// the load gate proceeds after its cap, the lock proceeds on acquire
	// timeout — a manual deploy must never deadlock on the guard.
	if req.Config.Heavy {
		waitForLoadBelowThreshold(ctx)
		release := acquireCrossLaneLock(ctx, req.Repo, result.BuiltSHA)
		defer release()
	}

	errMsg, builtNow, cv := composeBuild(ctx, buildReq, worktreePath, treeHash)
	if errMsg != "" {
		ManualDeployTotal.WithLabelValues(req.Repo, "sha_pinned", "failure").Inc()
		result.Error = errMsg
		return result
	}

	// Image-cache push-after-build: tag and push the freshly-built image to
	// the registry under the tree-hash tag. Best-effort — push failure NEVER
	// fails the deploy (the image is already built and will be brought up),
	// but it MUST emit an ERROR-level log so a silently-failing push is
	// observable. Mirrors the webhook path's executeBuild push step.
	// Gated on builtNow: a cache pull-hit is not re-pushed (issue #168).
	if treeHash != "" && builtNow {
		pushCachedImages(ctx, buildReq, treeHash)
	}

	// Step 5: bring containers up — the deploy clone is re-verified right
	// before the up against the build-time baseline cv (#239).
	upErr, composeSHA := composeUp(ctx, buildReq, cv)
	if upErr != "" {
		ManualDeployTotal.WithLabelValues(req.Repo, "sha_pinned", "failure").Inc()
		result.Error = upErr
		return result
	}
	result.ComposeSHA = composeSHA

	ManualDeployTotal.WithLabelValues(req.Repo, "sha_pinned", "success").Inc()
	result.Success = true
	return result
}

// ComposeDeployDescription summarises for the server_deploy response what a
// compose-kind manual deploy actually pins and verifies (issue #239): the
// source worktree is pinned to origin/<branch>, and — when a deploy clone is
// configured — the clone must be clean at origin/<deploy_clone_branch> or the
// deploy is refused. The SHAs are the currently-known remote-tracking values
// (the deploy re-fetches before building). When from_disk is set, or no
// deploy clone is configured, or the operator passed allow_stale_config,
// nothing about the compose files is verified — the message says so
// explicitly instead of repeating the old false "SHA-pinned" claim.
func ComposeDeployDescription(ctx context.Context, req ManualDeployRequest) string {
	rc := req.Config
	if req.FromDisk {
		return "compose build of the on-disk working tree (from_disk) — nothing verified (from_disk)"
	}
	branch := rc.Branch
	if branch == "" {
		branch = defaultBranch
	}
	srcSHA := "unknown"
	if rc.SourcePath != "" {
		if s, err := gitManualOriginSHARunner(ctx, rc.SourcePath, branch); err == nil {
			srcSHA = ShortSHA(s)
		}
	}
	srcDesc := fmt.Sprintf("source pinned at origin/%s@%s", branch, srcSHA)
	if req.NoBuild {
		srcDesc = "no build (build=false) — up --no-build recreates services from the CURRENT image; no source pinned"
	}
	cloneDesc := "no deploy_clone_path configured — compose files NOT verified"
	if rc.DeployClonePath != "" {
		if req.AllowStaleConfig {
			cloneDesc = "STALE CONFIG OVERRIDE — deploy-clone verification skipped (allow_stale_config); compose used as-is on disk"
		} else {
			cloneBranch := rc.deployCloneBranch()
			cloneSHA := "unknown"
			if s, err := gitManualOriginSHARunner(ctx, rc.DeployClonePath, cloneBranch); err == nil {
				cloneSHA = ShortSHA(s)
			}
			cloneDesc = fmt.Sprintf("compose verified at origin/%s@%s (deploy refused if dirty, detached, or moved)",
				cloneBranch, cloneSHA)
		}
	}
	return "compose: " + srcDesc + "; " + cloneDesc
}

// gitPrepareBranch creates a detached worktree at origin/<branch> in the
// source clone. Unlike gitPrepare (which resolves a SHA), this always targets
// the remote tracking ref — ensuring the manual path builds exactly what
// origin holds regardless of the local clone's checkout state.
//
// Returns the worktree path, the git tree hash of the worktree HEAD (used for
// image-cache tagging — same content-address as the webhook path's gitPrepare),
// a cleanup function, and an error message (empty on success). When the tree
// hash cannot be resolved (rare git error), it is returned empty and image
// caching is silently disabled for this deploy (the build proceeds, push/pull
// are skipped) — mirroring gitPrepare's behaviour exactly.
func gitPrepareBranch(ctx context.Context, sourcePath, branch string) (worktreePath, treeHash string, cleanup func(), errMsg string) {
	noop := func() {}
	if sourcePath == "" {
		return "", "", noop, "source_path is empty — cannot create worktree"
	}

	target := "origin/" + branch
	wtPath := fmt.Sprintf("/tmp/deploy-manual-%s-%d", branch, time.Now().UnixMilli())

	if err := runCmd(ctx, sourcePath, "git", "worktree", "add", "--detach", wtPath, target); err != nil {
		return "", "", noop, fmt.Sprintf("git worktree add (manual, origin/%s): %v", branch, err)
	}

	// Resolve the tree hash of the worktree HEAD for image-cache tagging.
	// The worktree is detached at origin/<branch>, so HEAD^{tree} is the tree
	// hash — the same content-address the webhook path uses. On failure,
	// return empty — image caching is silently disabled for this deploy.
	resolvedTreeHash, treeErr := gitTreeHashRunner(ctx, wtPath)
	if treeErr != nil {
		slog.Warn("deploy/manual: cannot resolve tree hash for image cache; feature disabled for this deploy",
			"path", wtPath, "target", target, "error", treeErr)
		resolvedTreeHash = ""
	}

	cleanupFn := func() {
		if err := runCmd(context.Background(), sourcePath, "git", "worktree", "remove", "--force", wtPath); err != nil {
			slog.Warn("deploy/manual: worktree cleanup failed, removing manually",
				"path", wtPath, "error", err)
			os.RemoveAll(wtPath)
		}
	}

	slog.Info("deploy/manual: worktree created", "path", wtPath, "target", target, "tree_hash", resolvedTreeHash)
	return wtPath, resolvedTreeHash, cleanupFn, ""
}
