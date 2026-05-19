package deploy

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

// makeStaticReq returns a minimal BuildRequest for KindStatic tests.
func makeStaticReq(script string) BuildRequest {
	return BuildRequest{
		Repo:      "anatolykoptev/krolik-tools-site",
		CommitSHA: "abc1234567890",
		Config: RepoConfig{
			Kind:               KindStatic,
			SourcePath:         "/home/krolik/sites/krolik-tools-site",
			StaticDeployScript: script,
			Services:           []string{"krolik-tools-site"},
		},
	}
}

func TestExecuteStaticBuild_HappyPath(t *testing.T) {
	orig := staticScriptRunner
	defer func() { staticScriptRunner = orig }()

	var gotScript, gotRepoPath, gotSHA string
	var gotChangedPaths []string
	staticScriptRunner = func(_ context.Context, script, repoPath, commitSHA string, changedPaths []string) ([]byte, error) {
		gotScript = script
		gotRepoPath = repoPath
		gotSHA = commitSHA
		gotChangedPaths = changedPaths
		return []byte("build OK"), nil
	}

	req := makeStaticReq("/home/krolik/bin/deploy.sh")
	req.ChangedPaths = []string{"config/caddy/Caddyfile", "config/llm.env"}
	result := executeStaticBuild(context.Background(), req)

	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
	if gotScript != "/home/krolik/bin/deploy.sh" {
		t.Errorf("script = %q, want /home/krolik/bin/deploy.sh", gotScript)
	}
	if gotRepoPath != "/home/krolik/sites/krolik-tools-site" {
		t.Errorf("repoPath = %q", gotRepoPath)
	}
	if gotSHA != "abc1234567890" {
		t.Errorf("commitSHA = %q", gotSHA)
	}
	if len(gotChangedPaths) != 2 {
		t.Errorf("changedPaths = %v, want 2 entries", gotChangedPaths)
	}
}

func TestExecuteStaticBuild_ScriptFailure(t *testing.T) {
	orig := staticScriptRunner
	defer func() { staticScriptRunner = orig }()

	staticScriptRunner = func(_ context.Context, _, _, _ string, _ []string) ([]byte, error) {
		return []byte("npm run build failed: out of memory"), errors.New("exit status 1")
	}

	req := makeStaticReq("/home/krolik/bin/deploy.sh")
	result := executeStaticBuild(context.Background(), req)

	if result.Success {
		t.Fatal("expected failure")
	}
	if !strings.Contains(result.Error, "exit status 1") {
		t.Errorf("expected 'exit status 1' in error, got: %s", result.Error)
	}
	if !strings.Contains(result.Error, "out of memory") {
		t.Errorf("expected script output in error, got: %s", result.Error)
	}
}

func TestExecuteStaticBuild_RoutedFromExecuteBuild(t *testing.T) {
	// Verify that executeBuild dispatches to executeStaticBuild for KindStatic.
	orig := staticScriptRunner
	defer func() { staticScriptRunner = orig }()

	called := false
	staticScriptRunner = func(_ context.Context, _, _, _ string, _ []string) ([]byte, error) {
		called = true
		return []byte("ok"), nil
	}

	ctx := context.Background()
	q := NewQueue(ctx, func(string) {})
	defer q.Close()

	req := makeStaticReq("/home/krolik/bin/deploy.sh")
	result := q.executeBuild(ctx, req)

	if !called {
		t.Fatal("staticScriptRunner was not called via executeBuild")
	}
	if !result.Success {
		t.Fatalf("expected success, got: %s", result.Error)
	}
}

func TestDefaultStaticScriptRunner_CmdDirIsRepoPath(t *testing.T) {
	// Verify that defaultStaticScriptRunner sets cmd.Dir to repoPath so scripts
	// inherit the deploy worktree as their working directory by default.
	// We run a real shell one-liner that checks $PWD == DEPLOY_REPO_PATH.
	t.Parallel()

	// Use /tmp as a stable, existing directory we can pass as repoPath.
	repoPath := t.TempDir()
	// Script: exit 0 if PWD matches DEPLOY_REPO_PATH, exit 1 otherwise.
	script := "/bin/sh"
	ctx := context.Background()

	// We can't inject into defaultStaticScriptRunner directly since it takes a
	// script path, not a script body. Instead we use a tiny inline script via
	// the sh -c convention by using sh as the executable.
	// Use exec.Command directly to mirror what defaultStaticScriptRunner does.
	// The real check: call defaultStaticScriptRunner with a helper script.
	//
	// We write a small script to a temp file that asserts PWD == DEPLOY_REPO_PATH.
	scriptBody := `#!/bin/sh
if [ "$PWD" = "$DEPLOY_REPO_PATH" ]; then
  echo "ok: pwd=$PWD"
  exit 0
fi
echo "fail: pwd=$PWD expected=$DEPLOY_REPO_PATH"
exit 1
`
	scriptFile := repoPath + "/check_pwd.sh"
	if err := os.WriteFile(scriptFile, []byte(scriptBody), 0o700); err != nil {
		t.Fatalf("write script: %v", err)
	}
	_ = script

	out, err := defaultStaticScriptRunner(ctx, scriptFile, repoPath, "deadbeef", nil)
	if err != nil {
		t.Fatalf("script failed: %v\noutput: %s", err, out)
	}
	if !strings.Contains(string(out), "ok:") {
		t.Errorf("expected 'ok:' in output, got: %s", out)
	}
}

func TestExecuteStaticBuild_ChangedPathsPassthrough(t *testing.T) {
	// Verify ChangedPaths is forwarded to the script runner for DEPLOY_CHANGED_PATHS.
	orig := staticScriptRunner
	defer func() { staticScriptRunner = orig }()

	tests := []struct {
		name         string
		changedPaths []string
	}{
		{"known paths", []string{"config/llm.env", "config/caddy/Caddyfile"}},
		{"nil (force-push/unknown)", nil},
		{"empty slice", []string{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var gotPaths []string
			staticScriptRunner = func(_ context.Context, _, _, _ string, changedPaths []string) ([]byte, error) {
				gotPaths = changedPaths
				return []byte("ok"), nil
			}
			req := makeStaticReq("/bin/sh")
			req.ChangedPaths = tc.changedPaths
			result := executeStaticBuild(context.Background(), req)
			if !result.Success {
				t.Fatalf("unexpected failure: %s", result.Error)
			}
			// nil and non-nil distinguishable: script can detect force-push vs known paths.
			if tc.changedPaths == nil && gotPaths != nil {
				t.Errorf("want nil changedPaths forwarded, got %v", gotPaths)
			}
			if tc.changedPaths != nil && len(gotPaths) != len(tc.changedPaths) {
				t.Errorf("changedPaths len = %d, want %d; got %v", len(gotPaths), len(tc.changedPaths), gotPaths)
			}
		})
	}
}
