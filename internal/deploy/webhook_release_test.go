package deploy

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Regression coverage for the release-event BuildPaths bypass: a GitHub
// "release published" webhook carries no per-commit changed-files list
// (unlike push's commits[].added/modified/removed), so before this fix
// skipByPathFilter's "GitHub elided the diff — be conservative and build"
// fallback fired unconditionally for EVERY release, ignoring BuildPaths/
// SkipPaths entirely. Empirically confirmed against go-grad's v0.1.0 release:
// a CHANGELOG-only commit already correctly skipped by the push-event path
// still triggered a real rebuild via the release-event path.

// releasePayload builds a minimal GitHub "release" webhook JSON body
// (action=published, matching what ServeHTTP's release branch expects).
func releasePayload(repo, tagName, targetCommitish string) string {
	body := struct {
		Action  string `json:"action"`
		Release struct {
			TagName         string `json:"tag_name"`
			TargetCommitish string `json:"target_commitish"`
		} `json:"release"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
	}{}
	body.Action = "published"
	body.Release.TagName = tagName
	body.Release.TargetCommitish = targetCommitish
	body.Repository.FullName = repo
	out, _ := json.Marshal(body)
	return string(out)
}

func postRelease(h *Handler, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/deploy/github", strings.NewReader(body))
	req.Header.Set("X-GitHub-Event", "release")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// mustRunOutput runs a command in dir and returns its trimmed stdout, failing
// the test on error. Sibling of mustRun (queue_clone_pull_test.go) for the
// rare case a test needs the command's output, not just success/failure.
func mustRunOutput(t *testing.T, dir string, name string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), name, args...) //nolint:gosec // test helper, trusted args
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("command %q %v failed in %s: %v", name, args, dir, err)
	}
	return strings.TrimSpace(string(out))
}

// buildTwoCommitFixture creates a real local git repo (no remote — the
// release-diff code only ever runs `git diff --name-only`, which needs no
// remote) with two commits: the first touches a build-relevant path
// (app/main.go, standing in for "currently deployed"); the second touches
// secondCommitPath/secondCommitContent (standing in for the release-please
// commit at target_commitish). Returns the repo dir and both commits' full
// SHAs.
func buildTwoCommitFixture(t *testing.T, secondCommitPath, secondCommitContent string) (dir, firstSHA, secondSHA string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found, skipping")
	}

	dir = t.TempDir()
	mustRun(t, dir, "git", "init", "--initial-branch=main")
	mustRun(t, dir, "git", "config", "user.email", "test@test.com")
	mustRun(t, dir, "git", "config", "user.name", "Test")

	if err := os.MkdirAll(filepath.Join(dir, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app", "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, dir, "git", "add", ".")
	mustRun(t, dir, "git", "commit", "-m", "feat: app code")
	firstSHA = mustRunOutput(t, dir, "git", "rev-parse", "HEAD")

	target := filepath.Join(dir, secondCommitPath)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(secondCommitContent), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, dir, "git", "add", ".")
	mustRun(t, dir, "git", "commit", "-m", "chore: release v1.0.0")
	secondSHA = mustRunOutput(t, dir, "git", "rev-parse", "HEAD")

	return dir, firstSHA, secondSHA
}

// TestHandler_Release_PathFilter_SkipsChangelogOnlyRelease is the core
// regression test: a release whose real diff (against the last-deployed SHA)
// touches ONLY a path outside BuildPaths must be skipped, exactly like the
// equivalent push would be.
func TestHandler_Release_PathFilter_SkipsChangelogOnlyRelease(t *testing.T) {
	t.Parallel()

	dir, deployedSHA, targetSHA := buildTwoCommitFixture(t, "CHANGELOG.md", "# Changelog\n")

	cfg := &Config{
		Repos: map[string]RepoConfig{
			"anatolykoptev/memdb": {
				ComposePath: dir,
				SourcePath:  dir,
				Services:    []string{"memdb-go"},
				BuildPaths:  []string{"app/**"},
			},
		},
	}
	q, _ := newTestQueue()
	h := NewHandler(cfg, q, func(string) {})
	defer h.Close()
	h.shaResolver = func(context.Context, string) string { return deployedSHA }

	body := releasePayload("anatolykoptev/memdb", "v1.0.0", targetSHA)
	w := postRelease(h, body)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var resp map[string]string
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "skipped" || resp["reason"] != "no_relevant_paths" {
		t.Errorf("response = %+v, want status=skipped reason=no_relevant_paths "+
			"(release touched only CHANGELOG.md, outside build_paths app/**)", resp)
	}
}

// TestHandler_Release_PathFilter_BuildsOnRelevantChange is the positive
// control for the test above: a release whose real diff DOES touch a
// BuildPaths-matching file must still build. Proves the fix gates correctly
// rather than degenerating into "always skip releases now".
func TestHandler_Release_PathFilter_BuildsOnRelevantChange(t *testing.T) {
	t.Parallel()

	dir, deployedSHA, targetSHA := buildTwoCommitFixture(t, "app/handler.go", "package main\n\nfunc handle() {}\n")

	cfg := &Config{
		Repos: map[string]RepoConfig{
			"anatolykoptev/memdb": {
				ComposePath: dir,
				SourcePath:  dir,
				Services:    []string{"memdb-go"},
				BuildPaths:  []string{"app/**"},
			},
		},
	}
	q, _ := newTestQueue()
	h := NewHandler(cfg, q, func(string) {})
	defer h.Close()
	h.shaResolver = func(context.Context, string) string { return deployedSHA }

	body := releasePayload("anatolykoptev/memdb", "v1.0.0", targetSHA)
	w := postRelease(h, body)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var resp map[string]string
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "queued" {
		t.Errorf("response status = %q, want queued (release touched app/handler.go, inside build_paths app/**)",
			resp["status"])
	}
}

// TestHandler_Release_PathFilter_FallsBackToBuildWhenSHAUnresolvable covers
// fix requirement #4: a fresh repo / never-deployed target (the resolver
// can't answer "what's currently deployed", mirroring resolveGitSHA's own
// "unknown" sentinel on a real resolution failure) must preserve the
// ORIGINAL "be conservative and build" fallback — never silently skip when
// there is no positive evidence of the changed files.
func TestHandler_Release_PathFilter_FallsBackToBuildWhenSHAUnresolvable(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Repos: map[string]RepoConfig{
			"anatolykoptev/memdb": {
				ComposePath: "/tmp", SourcePath: "/tmp",
				Services:   []string{"memdb-go"},
				BuildPaths: []string{"app/**"},
			},
		},
	}
	q, _ := newTestQueue()
	h := NewHandler(cfg, q, func(string) {})
	defer h.Close()
	h.shaResolver = func(context.Context, string) string { return "unknown" }

	body := releasePayload("anatolykoptev/memdb", "v1.0.0", "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	w := postRelease(h, body)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var resp map[string]string
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "queued" {
		t.Errorf("response status = %q, want queued "+
			"(deployed SHA unresolvable — must fall back to conservative build, not skip)", resp["status"])
	}
}

// TestHandler_Release_PathFilter_FallsBackToBuildWhenTargetCommitishInvalid
// covers the other half of fix requirement #4: the deployed SHA resolves
// fine (unlike the test above), but target_commitish itself is not a valid
// revision in the checkout — `git diff --name-only` errors out. Must
// degrade to the same conservative "build" fallback, not panic or hang.
// Also exercises defaultGitDiffNameOnlyRunner's real error path (the
// fixture uses no resolver override for the diff runner itself, only for
// shaResolver), which the SHA-unresolvable test above never reaches since
// it short-circuits before any git diff is attempted.
func TestHandler_Release_PathFilter_FallsBackToBuildWhenTargetCommitishInvalid(t *testing.T) {
	t.Parallel()

	dir, deployedSHA, _ := buildTwoCommitFixture(t, "CHANGELOG.md", "# Changelog\n")

	cfg := &Config{
		Repos: map[string]RepoConfig{
			"anatolykoptev/memdb": {
				ComposePath: dir,
				SourcePath:  dir,
				Services:    []string{"memdb-go"},
				BuildPaths:  []string{"app/**"},
			},
		},
	}
	q, _ := newTestQueue()
	h := NewHandler(cfg, q, func(string) {})
	defer h.Close()
	h.shaResolver = func(context.Context, string) string { return deployedSHA }

	body := releasePayload("anatolykoptev/memdb", "v1.0.0", "not-a-real-revision-in-this-repo")
	w := postRelease(h, body)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var resp map[string]string
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "queued" {
		t.Errorf("response status = %q, want queued "+
			"(target_commitish invalid — git diff errors, must fall back to conservative build, not skip)",
			resp["status"])
	}
}

// TestHandler_Release_PathFilter_MultiRepo_DiffsInSourceClone is the core
// regression for issue #160: when a repo's DeployClonePath points to a
// DIFFERENT repo's clone (e.g. vaelor's compose lives in the krolik-server
// deploy clone) and SourcePath points to the webhook's own source clone,
// the release diff MUST be resolved in the SourcePath clone — where both
// the deployed and target SHAs exist — not in the DeployClonePath clone
// where the target SHA is a foreign revision that git diff cannot resolve.
//
// With the bug (diff in DeployClonePath): target SHA unresolvable → exit 128
// → conservative build → "queued" (CHANGELOG.md never gets evaluated).
// With the fix   (diff in SourcePath):      both SHAs resolve → diff yields
// CHANGELOG.md → outside build_paths app/** → "skipped".
func TestHandler_Release_PathFilter_MultiRepo_DiffsInSourceClone(t *testing.T) {
	t.Parallel()

	// Source clone: the webhook's own repo (e.g. vaelor). Two commits —
	// first creates app/main.go, second touches ONLY CHANGELOG.md.
	sourceDir, deployedSHA, targetSHA := buildTwoCommitFixture(t, "CHANGELOG.md", "# Changelog\n")

	// Deploy clone: a DIFFERENT repo (e.g. krolik-server). Has its own
	// history; neither of the webhook's SHAs resolves here.
	deployDir := t.TempDir()
	mustRun(t, deployDir, "git", "init", "--initial-branch=main")
	mustRun(t, deployDir, "git", "config", "user.email", "test@test.com")
	mustRun(t, deployDir, "git", "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(deployDir, "docker-compose.yml"), []byte("services:\n  vaelor:\n    build: .\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, deployDir, "git", "add", ".")
	mustRun(t, deployDir, "git", "commit", "-m", "compose file for vaelor")

	cfg := &Config{
		Repos: map[string]RepoConfig{
			"anatolykoptev/vaelor": {
				DeployClonePath: deployDir,
				SourcePath:      sourceDir,
				ComposePath:     filepath.Join(deployDir, "docker-compose.yml"),
				Services:        []string{"vaelor"},
				BuildPaths:      []string{"app/**"},
			},
		},
	}
	q, _ := newTestQueue()
	h := NewHandler(cfg, q, func(string) {})
	defer h.Close()
	h.shaResolver = func(context.Context, string) string { return deployedSHA }

	body := releasePayload("anatolykoptev/vaelor", "v1.0.0", targetSHA)
	w := postRelease(h, body)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var resp map[string]string
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "skipped" || resp["reason"] != "no_relevant_paths" {
		t.Errorf("response = %+v, want status=skipped reason=no_relevant_paths "+
			"(diff must resolve in source clone where both SHAs exist; "+
			"CHANGELOG.md is outside build_paths app/**)", resp)
	}
}

// TestHandler_Release_PathFilter_UnresolvableSHA_LogsError verifies that a
// target SHA absent from the clone being diffed is reported as a
// CONFIGURATION ERROR at slog.Error (not silently downgraded to WARN), with
// the log message naming both the clone dir and the unresolvable SHA. The
// build still proceeds conservatively (never skip a build that might be
// needed), but the error is no longer silent.
func TestHandler_Release_PathFilter_UnresolvableSHA_LogsError(t *testing.T) {
	t.Parallel()

	dir, deployedSHA, _ := buildTwoCommitFixture(t, "CHANGELOG.md", "# Changelog\n")

	var buf strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelError})))
	defer slog.SetDefault(prev)

	cfg := &Config{
		Repos: map[string]RepoConfig{
			"anatolykoptev/memdb": {
				ComposePath: dir,
				SourcePath:  dir,
				Services:    []string{"memdb-go"},
				BuildPaths:  []string{"app/**"},
			},
		},
	}
	q, _ := newTestQueue()
	h := NewHandler(cfg, q, func(string) {})
	defer h.Close()
	h.shaResolver = func(context.Context, string) string { return deployedSHA }

	foreignSHA := "dd8d0d9eb825f48d0de27008d6d7ef5ad04e4497"
	body := releasePayload("anatolykoptev/memdb", "v1.0.0", foreignSHA)
	w := postRelease(h, body)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var resp map[string]string
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "queued" {
		t.Errorf("response status = %q, want queued "+
			"(unresolvable SHA must still trigger conservative build, not skip)", resp["status"])
	}

	logOut := buf.String()
	if !strings.Contains(logOut, "level=ERROR") {
		t.Errorf("log must contain level=ERROR for unresolvable SHA, got:\n%s", logOut)
	}
	if !strings.Contains(logOut, foreignSHA) {
		t.Errorf("log must name the unresolvable SHA %q, got:\n%s", foreignSHA, logOut)
	}
	if !strings.Contains(logOut, dir) {
		t.Errorf("log must name the clone dir %q, got:\n%s", dir, logOut)
	}
}
