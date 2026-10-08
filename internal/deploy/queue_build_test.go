package deploy

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// makeReq returns a minimal BuildRequest for executeBuild tests.
// SourcePath is empty to skip git steps; ComposePath is set to trigger docker steps.
func makeReq(composePath string) BuildRequest {
	return BuildRequest{
		Repo:      "test/repo",
		CommitSHA: "abc1234567890",
		Config: RepoConfig{
			ComposePath: composePath,
			Services:    []string{"svc"},
		},
	}
}

// composeSub returns the compose subcommand in an argv produced by
// composeArgv — {"compose", "-f", "<file>", "<sub>", ...} → "<sub>" — so
// test outputRunner stubs keep working now that -f shifts the subcommand
// off args[1]. Returns "" for argv that is not a compose call.
func composeSub(args []string) string {
	if len(args) < 4 || args[0] != "compose" || args[1] != "-f" {
		return ""
	}
	return args[3]
}

// zeroDelays sets healthWait, upRetryDelay, and portRecoveryWait to zero for fast tests,
// stubs buildRunner to a no-op, and stubs upRunner to a no-op so executeBuild tests
// don't shell out to real docker. Returns a restore function to be called via defer.
func zeroDelays(t *testing.T) func() {
	t.Helper()
	origHealth := healthWait
	origRetry := upRetryDelay
	origRecovery := portRecoveryWait
	origBuild := buildRunner
	origUp := upRunner
	healthWait = 0
	upRetryDelay = 0
	portRecoveryWait = 0
	buildRunner = func(_ context.Context, _ string, _ []string) ([]byte, error) {
		return nil, nil
	}
	upRunner = func(_ context.Context, _ string, _ []string) ([]byte, error) {
		return nil, nil
	}
	return func() {
		healthWait = origHealth
		upRetryDelay = origRetry
		portRecoveryWait = origRecovery
		buildRunner = origBuild
		upRunner = origUp
	}
}

func TestExecuteBuild_RetryThenSuccess(t *testing.T) {
	defer zeroDelays(t)()

	calls := 0
	upRunner = func(_ context.Context, _ string, _ []string) ([]byte, error) {
		calls++
		if calls == 1 {
			return []byte("transient error output"), errors.New("exit status 1")
		}
		return nil, nil
	}

	ctx := context.Background()
	q := NewQueue(ctx, func(string) {})
	defer q.Close()

	result := q.executeBuild(ctx, makeReq("/tmp"))

	if calls != 2 {
		t.Fatalf("expected 2 docker up calls (retry), got %d", calls)
	}
	if strings.Contains(result.Error, "docker up") {
		t.Errorf("expected to pass docker up step, got error: %s", result.Error)
	}
}

func TestExecuteBuild_AllRetriesFail(t *testing.T) {
	defer zeroDelays(t)()

	upRunner = func(_ context.Context, _ string, _ []string) ([]byte, error) {
		return []byte("permanent failure output"), errors.New("permanent failure")
	}

	ctx := context.Background()
	q := NewQueue(ctx, func(string) {})
	defer q.Close()

	result := q.executeBuild(ctx, makeReq("/tmp"))

	if result.Success {
		t.Fatal("expected failure")
	}
	want := "after 2 attempts"
	if !strings.Contains(result.Error, want) {
		t.Errorf("expected error to contain %q, got: %s", want, result.Error)
	}
}

func TestExecuteBuild_ContextCancelledDuringRetry(t *testing.T) {
	defer zeroDelays(t)()

	ctx, cancel := context.WithCancel(context.Background())

	upRunner = func(_ context.Context, _ string, _ []string) ([]byte, error) {
		cancel()
		return []byte("up failed output"), errors.New("up failed")
	}

	q := NewQueue(context.Background(), func(string) {})
	defer q.Close()

	result := q.executeBuild(ctx, makeReq("/tmp"))

	if result.Success {
		t.Fatal("expected failure on context cancellation")
	}
	want := "context cancelled during retry"
	if !strings.Contains(result.Error, want) {
		t.Errorf("expected error to contain %q, got: %s", want, result.Error)
	}
}

// TestExecuteBuild_EveryComposeArgvPassesF — EVERY `docker compose` dispatch
// in the deploy path must pin the project file explicitly with
// `-f <ComposePath>/docker-compose.yml`. Without -f, compose auto-loads an
// untracked docker-compose.override.yml / compose.override.yml dropped into
// the deploy clone, silently extending a verified deploy (issue #239).
//
// The test captures argv at all four dispatch seams (buildRunner, upRunner,
// outputRunner, cmdRunner) across a full happy-path executeBuild and asserts
// the -f pair on every compose invocation — dropping -f from ANY call site
// (the up argv included) turns this RED.
func TestExecuteBuild_EveryComposeArgvPassesF(t *testing.T) {
	defer zeroDelays(t)()

	const composePath = "/fake/compose"
	wantFile := filepath.Join(composePath, "docker-compose.yml")

	var composeCalls [][]string
	record := func(args []string) {
		if len(args) > 0 && args[0] == "compose" {
			composeCalls = append(composeCalls, append([]string(nil), args...))
		}
	}

	buildRunner = func(_ context.Context, _ string, args []string) ([]byte, error) {
		record(args)
		return nil, nil
	}
	upRunner = func(_ context.Context, _ string, args []string) ([]byte, error) {
		record(args)
		return nil, nil
	}
	withCmdRunner(t, func(_ context.Context, _ string, name string, args ...string) error {
		if name == "docker" {
			record(args)
		}
		return nil
	})
	withOutputRunner(t, func(_ context.Context, _ string, name string, args ...string) ([]byte, error) {
		if name == "docker" {
			record(args)
		}
		switch composeSub(args) {
		case "ps":
			// Healthy, port-published container → checkHealth passes.
			return []byte(`[{"State":"running","Status":"Up","Publishers":[{"URL":"0.0.0.0","TargetPort":8080,"PublishedPort":8080,"Protocol":"tcp"}]}]`), nil
		case "config":
			// The service's image name + the port declaration verifyPortMapping checks.
			return []byte(`{"name":"proj","services":{"svc":{"image":"proj-svc:latest","ports":["8080:8080"]}}}`), nil
		case "images":
			return []byte(`[{"ID":"previmg1234567","ContainerName":"svc"}]`), nil
		}
		return nil, errors.New("unstubbed outputRunner call: " + strings.Join(args, " "))
	})

	ctx := context.Background()
	q := NewQueue(ctx, func(string) {})
	defer q.Close()

	req := makeReq(composePath)
	result := q.executeBuild(ctx, req)
	if !result.Success {
		t.Fatalf("executeBuild must succeed on the happy path, got: %s", result.Error)
	}

	if len(composeCalls) == 0 {
		t.Fatal("no docker compose calls captured — the test is not exercising the deploy path")
	}
	seen := map[string]bool{}
	for _, argv := range composeCalls {
		if len(argv) < 3 || argv[1] != "-f" || argv[2] != wantFile {
			t.Errorf("compose argv %v missing -f %s — override files in the clone would auto-load", argv, wantFile)
			continue
		}
		seen[composeSub(argv)] = true
	}
	for _, sub := range []string{"build", "up", "ps", "images", "config"} {
		if !seen[sub] {
			t.Errorf("expected a `compose %s` dispatch to be covered by the -f assertion", sub)
		}
	}
}

// TestProcessBuild_CloneRefusalVisibility — a refused deploy must be LOUD
// and recoverable (issue #239): dozor_pending_deploy=1 for the repo so the
// dashboard shows a deploy is owed, the Telegram failure notify names the
// reason and the dirty paths, and NO deployed-SHA receipt is written — a
// refusal ships nothing.
func TestProcessBuild_CloneRefusalVisibility(t *testing.T) {
	defer zeroDelays(t)()

	// The clone verifies nothing: it is dirty at build time.
	withGitFetch(t, func(_ context.Context, _, _ string) error { return nil })
	withAttachedClone(t)
	withGitStatus(t, func(_ context.Context, _ string) ([]byte, error) {
		return []byte(" M docker-compose.yml\n"), nil
	})

	const repo = "test/refusal-visible"
	const svc = "svc"
	PendingDeployGauge.WithLabelValues(repo, svc).Set(0)

	var notified []string
	ctx := context.Background()
	q := NewQueue(ctx, func(msg string) { notified = append(notified, msg) })
	defer q.Close()

	req := makeReq("/tmp")
	req.Repo = repo
	req.Config.DeployClonePath = "/fake/clone"
	q.processBuild(ctx, req, false)

	// 1. Pending gauge set — a refused deploy is a deploy still owed.
	if got := testutil.ToFloat64(PendingDeployGauge.WithLabelValues(repo, svc)); got != 1 {
		t.Errorf("dozor_pending_deploy{%s,%s} = %v, want 1 after a clone refusal", repo, svc, got)
	}

	// 2. The failure notify names the dirty path.
	var failMsg string
	for _, m := range notified {
		if strings.Contains(m, "FAILED") {
			failMsg = m
		}
	}
	if failMsg == "" {
		t.Fatalf("expected a FAILED Telegram notify on refusal, got: %v", notified)
	}
	if !strings.Contains(failMsg, "docker-compose.yml") || !strings.Contains(failMsg, "uncommitted changes") {
		t.Errorf("refusal notify must name the reason and dirty paths; got %q", failMsg)
	}

	// 3. No receipt — neither the source SHA nor the compose SHA may be
	// recorded: nothing shipped. RED-on-revert: write the receipt on the
	// refusal path and this fails.
	if got := lookupDeployedSHA(repo); got != "" {
		t.Errorf("a refused deploy must not record a deployed SHA, got receipt %q", got)
	}
	pendingDeployMu.Lock()
	_, composeReceipt := deployedComposeSHAs[repo]
	pendingDeployMu.Unlock()
	if composeReceipt {
		t.Error("a refused deploy must not record a compose SHA receipt")
	}
}
