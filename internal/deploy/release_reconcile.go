package deploy

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
)

// ReconcileMissedReleases re-drives deploys for webhook deliveries that never
// reached dozor. A webhook that lands while dozor is restarting gets a 502 and
// GitHub does NOT auto-retry it — the tag exists, the box never deploys, and
// nothing reports the miss (issue #174: a release stayed undeployed until a
// human noticed).
//
// Called once at startup, AFTER RecoverQueue/RecoverPending — queue dedup by
// (service-key, SHA) makes a re-submit of an already-recovered build a no-op.
//
// Per configured repo:
//   - deploy_on: release / manual → target = the newest semver tag's commit
//     (release-please convention: the tag points at a merge commit on the
//     release branch). For manual repos the existing manual gate still holds
//     the deploy and sets dozor_pending_deploy=1, so a missed manual release
//     surfaces via DozorReleaseWithheld exactly as if the webhook had arrived.
//   - deploy_on unset (push-deployed) → target = the remote tip of the
//     configured branch (a missed push deploys the newest tip — newest-wins,
//     same as the queue's own coalescing).
//
// Detection is remote-read-only: `git ls-remote` against the source clone's
// origin URL — no local fetch, so it works even when the clone is stale.
// Deployed SHA is rev-parse HEAD of the repo's OWN source clone — the clone
// whose HEAD syncSourceCheckout fast-forwards to origin/<branch> at build time,
// so after a release deploy it sits at the tag's merge commit. The deploy clone
// (buildDirForConfig) is deliberately NOT used: on this fleet deploy_clone_path
// points at the compose repo (krolik-server), an unrelated repository whose
// HEAD can never equal an app-repo tag SHA — comparing against it enqueues a
// redundant build for every repo on every dozor restart.
//
// Conservative: an unresolvable remote URL, target ref, or deployed SHA skips
// the repo with a WARN — the reconciler must never deploy on a guess.
func ReconcileMissedReleases(ctx context.Context, cfg *Config, q *Queue) {
	if cfg == nil || q == nil {
		return
	}
	for key, rc := range cfg.Repos {
		repo := stripBranchSuffix(key)
		dir := sourceDirForConfig(rc)
		if dir == "" {
			continue
		}
		target, err := reconcileTarget(ctx, dir, &rc)
		if err != nil {
			slog.Warn("deploy/reconcile: cannot resolve remote target — skipping",
				"repo", repo, "dir", dir, "error", err)
			continue
		}
		if target == "" {
			continue
		}
		deployed := reconcileDeployedSHA(ctx, dir)
		if deployed == "" || deployed == "unknown" {
			// Never deployed → not a "missed release" we can prove; the first
			// real push/release deploys it. Don't guess.
			continue
		}
		if target == deployed {
			continue
		}
		slog.Info("deploy/reconcile: deployed SHA is behind the remote target — enqueueing missed deploy",
			"repo", repo, "services", rc.Services,
			"deployed", short(deployed), "target", short(target),
			"deploy_on", rc.DeployOn)
		q.Submit(BuildRequest{
			Repo:      repo,
			CommitSHA: target,
			// nil → "unknown": skipByPathFilter builds conservatively, which is
			// correct for a recovery deploy — we cannot prove which files changed.
			ChangedPaths: nil,
			Config:       rc,
		})
	}
}

// reconcileTarget resolves what the deployed SHA SHOULD be for this repo:
// latest semver tag commit for release-gated repos, branch tip for
// push-deployed ones. Returns "" when the repo simply has no tags yet.
func reconcileTarget(ctx context.Context, dir string, rc *RepoConfig) (string, error) {
	url, err := gitRemoteURLRunner(ctx, dir)
	if err != nil {
		return "", fmt.Errorf("remote get-url: %w", err)
	}
	if url == "" {
		return "", fmt.Errorf("remote get-url: empty origin URL in %s", dir)
	}
	if rc.DeployOn == deployOnRelease || rc.DeployOn == deployOnManual {
		return latestSemverTag(ctx, url)
	}
	branch := rc.Branch
	if branch == "" {
		branch = "main"
	}
	return lsRemoteSHA(ctx, url, "refs/heads/"+branch)
}

// latestSemverTag returns the commit SHA of the newest vX.Y.Z tag on the
// remote, preferring the peeled ^{} line (annotated tags point at tag objects;
// the commit is what a build needs). "" when the remote has no semver tags.
func latestSemverTag(ctx context.Context, url string) (string, error) {
	out, err := gitLsRemoteRunner(ctx, url, "refs/tags/v*.*.*", "--tags", "--sort=-v:refname")
	if err != nil {
		return "", err
	}
	// Sorted newest-first by version. Annotated tags emit TWO lines — the
	// tag object (refs/tags/vX.Y.Z) then the peeled commit
	// (refs/tags/vX.Y.Z^{}); the object line sorts first, so a naive
	// first-match returns the tag object SHA — which is not a commit, never
	// equals a deployed HEAD, and would poison CommitSHA/DEPLOY_SHA with a
	// non-commit. Remember the first unpeeled semver tag and keep scanning
	// for ITS peeled line; a second tag's line before the peel means the tag
	// was lightweight (object line == commit).
	var candTag, candSHA string
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		sha, ref := fields[0], fields[1]
		name := strings.TrimPrefix(ref, "refs/tags/")
		if peeled := strings.HasSuffix(name, "^{}"); peeled {
			if strings.TrimSuffix(name, "^{}") == candTag {
				return sha, nil // annotated tag → the peeled COMMIT
			}
			continue
		}
		if !matchesSemVer(name) {
			continue
		}
		if candTag != "" {
			return candSHA, nil // previous tag had no peel → lightweight
		}
		candTag, candSHA = name, sha
	}
	return candSHA, nil
}

// lsRemoteSHA resolves a single ref to a commit SHA via git ls-remote.
func lsRemoteSHA(ctx context.Context, url, ref string) (string, error) {
	out, err := gitLsRemoteRunner(ctx, url, ref)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == ref {
			return fields[0], nil
		}
	}
	return "", nil
}

// ── seams (replaced in tests) ──────────────────────────────────────────────

// gitRemoteURLRunner resolves a clone's origin URL. Replaceable in tests.
var gitRemoteURLRunner = defaultGitRemoteURLRunner

//nolint:unused // DI default seam — assigned to var gitRemoteURLRunner, swapped in tests
func defaultGitRemoteURLRunner(ctx context.Context, dir string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "remote", "get-url", "origin") //nolint:gosec // trusted config dir
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git remote get-url origin: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// gitLsRemoteRunner runs `git ls-remote [flags] <url> [pattern]` — git's
// usage is `ls-remote [<options>] <repository> [<refs>...]`, so url sits
// between flags and patterns. Returns raw stdout. Replaceable in tests.
var gitLsRemoteRunner = defaultGitLsRemoteRunner

//nolint:unused // DI default seam — assigned to var gitLsRemoteRunner, swapped in tests
func defaultGitLsRemoteRunner(ctx context.Context, url, pattern string, flags ...string) (string, error) {
	argv := append([]string{"ls-remote"}, flags...)
	argv = append(argv, url)
	if pattern != "" {
		argv = append(argv, pattern)
	}
	cmd := exec.CommandContext(ctx, "git", argv...) //nolint:gosec // url comes from the clone's own config
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git ls-remote %s: %w", url, err)
	}
	return string(out), nil
}

// reconcileDeployedSHA resolves the deployed commit — the FULL 40-char SHA
// (ls-remote emits full SHAs; a short SHA can never equal them, making every
// restart look like drift). Bound to resolveGitFullSHA, not resolveGitSHA.
// Seam var so tests can stub the resolver.
var reconcileDeployedSHA = resolveGitFullSHA
