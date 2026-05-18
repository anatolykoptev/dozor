package deploy

import (
	"context"
	"errors"
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
