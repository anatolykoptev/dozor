package deploy

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
)

// gitDiffNameOnlyRunner executes `git diff --name-only <from> <to>` in dir,
// returning the changed file paths. Replaceable in tests.
var gitDiffNameOnlyRunner = defaultGitDiffNameOnlyRunner

//nolint:unused // DI default seam — assigned to var gitDiffNameOnlyRunner, swapped in tests
func defaultGitDiffNameOnlyRunner(ctx context.Context, dir, from, to string) ([]string, error) {
	// from/to originate from webhook payload data (resolved deployed SHA and
	// release target_commitish) — not operator-authored config. --end-of-options
	// stops git from parsing either as a flag (e.g. a target_commitish of
	// "--output=/path" would otherwise make git diff write to an arbitrary
	// file instead of being read as a revision).
	cmd := exec.CommandContext(ctx, "git", "diff", "--name-only", "--end-of-options", from, to)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git diff --name-only %s %s: %w", from, to, err)
	}
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return nil, nil
	}
	return strings.Split(trimmed, "\n"), nil
}

// releaseChangedFiles resolves the real set of files changed by a GitHub
// "release published" event, for feeding into the existing BuildPaths/
// SkipPaths filter (skipByPathFilter, webhook_dispatch.go). A release webhook
// payload structurally carries no per-commit changed-files list (unlike a
// push payload's commits[].added/modified/removed) — left unaddressed,
// push.changedFiles() always returns empty for a release event, and
// skipByPathFilter's "GitHub elided the diff — be conservative and build"
// fallback fires unconditionally for EVERY release, silently bypassing
// build_paths/skip_paths on every release-please cut.
//
// Computes: git diff --name-only <last-deployed-SHA> <targetCommitish>, in
// the directory dozor would build from (buildDirForConfig — DeployClonePath
// if set, else SourcePath; see debounce_persist.go), resolving "last-deployed"
// via resolveSHA — the same shaResolverFunc primitive (resolveGitSHA by
// default) the debounce-persistence layer already uses to detect a stale
// rebuild.
//
// ok=false whenever there is no POSITIVE evidence of the changed files: the
// build/source dir is unknown, the resolver is nil, the deployed SHA can't be
// resolved (fresh repo, never deployed), or the git diff itself errors (e.g.
// the deployed SHA isn't a valid revision in this checkout). Callers MUST
// treat ok=false as "fall back to the original conservative default" — never
// as "no files changed".
//
// Note: a resolved-but-empty diff (files=nil, ok=true — e.g. deployed already
// equals targetCommitish) currently degrades to the SAME "build conservatively"
// outcome as ok=false, because skipByPathFilter's own "elided diff" fallback
// also fires on a zero-length changed-files list (webhook_dispatch.go). That
// is safe (never a wasted skip) but not yet a distinct "definitely nothing
// changed, skip" path — a possible future refinement, out of scope here.
func releaseChangedFiles(ctx context.Context, rc *RepoConfig, targetCommitish string, resolveSHA shaResolverFunc) (files []string, ok bool) {
	if resolveSHA == nil {
		return nil, false
	}
	dir := buildDirForConfig(*rc)
	if dir == "" {
		return nil, false
	}
	deployed := resolveSHA(ctx, dir)
	if deployed == "" || deployed == "unknown" {
		return nil, false
	}
	diffed, err := gitDiffNameOnlyRunner(ctx, dir, deployed, targetCommitish)
	if err != nil {
		slog.Warn("deploy/webhook: release diff resolution failed, building conservatively",
			"dir", dir, "deployed", deployed, "target", targetCommitish, "error", err)
		return nil, false
	}
	return diffed, true
}

// attachReleaseDiff populates push.Commits with the real changed-files diff
// for a release event, when BuildPaths gating is configured for rc and the
// diff can be resolved. A no-op (push left unmodified) when BuildPaths is
// empty or the diff can't be resolved — skipByPathFilter's pre-existing
// "GitHub elided the diff — be conservative and build" fallback then applies
// exactly as it did before this feature existed.
func attachReleaseDiff(ctx context.Context, push *pushEvent, rc *RepoConfig, resolveSHA shaResolverFunc) {
	if len(rc.BuildPaths) == 0 {
		return
	}
	files, ok := releaseChangedFiles(ctx, rc, push.HeadCommit.ID, resolveSHA)
	if !ok {
		return
	}
	push.Commits = []struct {
		ID       string   `json:"id"`
		Added    []string `json:"added"`
		Removed  []string `json:"removed"`
		Modified []string `json:"modified"`
	}{{ID: push.HeadCommit.ID, Modified: files}}
}
