package deploy

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/anatolykoptev/dozor/internal/testutil/tempguard"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

const publickeyDenied = "git@github.com: Permission denied (publickey).\nfatal: Could not read from remote repository."

func zeroGitBackoff(t *testing.T) {
	t.Helper()
	orig := gitRetryBackoffs
	gitRetryBackoffs = []time.Duration{0, 0, 0}
	t.Cleanup(func() { gitRetryBackoffs = orig })
}

// stubHappyCompose stubs everything after the clone fetch so a processBuild
// with a working fetch ends in "Deployed".
func stubHappyCompose(t *testing.T) {
	t.Helper()
	const cloneSHA = "c10a4b2d3e4f5a6b7c8d9e0f1a2b3c4d5e6f7a8b"
	withAttachedClone(t)
	withGitStatus(t, func(_ context.Context, _ string) ([]byte, error) { return []byte(""), nil })
	withGitRevParse(t, func(_ context.Context, _, _ string) (string, error) { return cloneSHA, nil })
	withGitMergeFF(t, func(_ context.Context, _, _ string) error { return nil })
	withOutputRunner(t, func(_ context.Context, _ string, _ string, args ...string) ([]byte, error) {
		switch composeSub(args) {
		case "ps":
			return []byte(`[{"State":"running","Status":"Up","Publishers":[{"URL":"0.0.0.0","TargetPort":8080,"PublishedPort":8080,"Protocol":"tcp"}]}]`), nil
		case "config":
			return []byte(`{"name":"proj","services":{"svc":{"image":"proj-svc:latest","ports":["8080:8080"]}}}`), nil
		case "images":
			return []byte(`[{"ID":"previmg1234567","ContainerName":"svc"}]`), nil
		}
		return []byte("{}"), nil
	})
	withCmdRunner(t, func(_ context.Context, _ string, _ string, _ ...string) error { return nil })
}

func runRetryBuild(t *testing.T, repo string, fetch func(call int) error) (notified []string, calls int) {
	t.Helper()
	tempguard.Isolate(t)
	defer zeroDelays(t)()
	zeroGitBackoff(t)
	stubHappyCompose(t)
	withGitFetch(t, func(_ context.Context, _, _ string) error {
		calls++
		return fetch(calls)
	})
	ConfigureDeployedSHAPersistence("")
	ctx := context.Background()
	q := NewQueue(ctx, func(m string) { notified = append(notified, m) })
	defer q.Close()
	req := makeReq("/fake/compose")
	req.Repo = repo
	req.CommitSHA = "aaaa1111bbbb2222"
	req.Config.DeployClonePath = "/fake/clone"
	q.processBuild(ctx, req, false)
	return notified, calls
}

func countContaining(msgs []string, sub string) int {
	n := 0
	for _, m := range msgs {
		if strings.Contains(m, sub) {
			n++
		}
	}
	return n
}

// publickey denied twice, then GitHub is back: one successful deploy, no FAILED.
// Mutation (verified RED): make isTransientGitError return false (source_retry.go)
// — the first denial then fails the build.
func TestProcessBuild_TransientFetchRecovers(t *testing.T) {
	const repo = "test/transient-recovers"
	failBefore := testutil.ToFloat64(BuildResultTotal.WithLabelValues(repo, "svc", "failure"))
	notified, calls := runRetryBuild(t, repo, func(call int) error {
		if call <= 2 {
			return errors.New(publickeyDenied)
		}
		return nil
	})
	if calls != 3 {
		t.Errorf("fetch attempts = %d, want 3 (2 denied + 1 ok)", calls)
	}
	if n := countContaining(notified, "FAILED"); n != 0 {
		t.Errorf("transient fetch that recovered must not page FAILED, got %v", notified)
	}
	if n := countContaining(notified, "Deployed"); n != 1 {
		t.Errorf("want exactly one Deployed notify, got %v", notified)
	}
	if got := testutil.ToFloat64(BuildResultTotal.WithLabelValues(repo, "svc", "success")); got != 1 {
		t.Errorf("build_result{success} = %v, want 1", got)
	}
	if got := testutil.ToFloat64(BuildResultTotal.WithLabelValues(repo, "svc", "failure")) - failBefore; got != 0 {
		t.Errorf("build_result{failure} moved by %v on a recovered transient fetch", got)
	}
}

// Always denied: exactly one "source unreachable" notify, not a FAILED, and the
// failure counter stays put; the distinct status counter moves.
func TestProcessBuild_TransientFetchExhausted(t *testing.T) {
	const repo = "test/transient-exhausted"
	notified, calls := runRetryBuild(t, repo, func(int) error { return errors.New(publickeyDenied) })
	if calls != len(gitRetryBackoffs)+1 {
		t.Errorf("fetch attempts = %d, want %d", calls, len(gitRetryBackoffs)+1)
	}
	// notify() also announces "Building..." first; only terminal notifies count.
	if n := countContaining(notified, "Source unreachable"); n != 1 {
		t.Errorf("want exactly one source-unreachable notify, got %v", notified)
	}
	if n := countContaining(notified, "FAILED"); n != 0 {
		t.Errorf("source outage must not page FAILED, got %v", notified)
	}
	if got := testutil.ToFloat64(BuildResultTotal.WithLabelValues(repo, "svc", "failure")); got != 0 {
		t.Errorf("build_result{failure} = %v, want 0", got)
	}
	if got := testutil.ToFloat64(BuildResultTotal.WithLabelValues(repo, "svc", statusSourceUnavailable)); got != 1 {
		t.Errorf("build_result{source_unavailable} = %v, want 1", got)
	}
	if o, ok := LastBuildOutcome("svc"); !ok || o.Status != statusSourceUnavailable {
		t.Errorf("recorded outcome = %+v, want status %s", o, statusSourceUnavailable)
	}
}

// A real fetch failure stays loud and is not retried.
func TestProcessBuild_NonTransientFetchStaysFailure(t *testing.T) {
	const repo = "test/transient-hard"
	notified, calls := runRetryBuild(t, repo, func(int) error {
		return errors.New("fatal: couldn't find remote ref refs/heads/gone")
	})
	if calls != 1 {
		t.Errorf("non-transient fetch error retried: %d attempts", calls)
	}
	if n := countContaining(notified, "FAILED"); n != 1 {
		t.Errorf("want one FAILED notify, got %v", notified)
	}
	if got := testutil.ToFloat64(BuildResultTotal.WithLabelValues(repo, "svc", "failure")); got != 1 {
		t.Errorf("build_result{failure} = %v, want 1", got)
	}
}

func TestIsTransientGitError(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{errors.New("git@github.com: Permission denied (publickey)."), true},
		{errors.New("fatal: Could not read from remote repository."), true},
		{errors.New("ssh: connect to host github.com port 22: Connection timed out"), true},
		{errors.New("read: Connection reset by peer"), true},
		{errors.New("Connection closed by remote host"), true},
		{errors.New("CONNECTION CLOSED BY REMOTE HOST"), true},
		// git appends "Could not read from remote repository" to a missing remote
		// too; waiting will not fix that.
		{errors.New("fatal: '/x/no-such-remote' does not appear to be a git repository\nfatal: Could not read from remote repository."), false},
		{errors.New("ERROR: Repository not found.\nfatal: Could not read from remote repository."), false},
		{errors.New("CONFLICT (content): Merge conflict in main.go"), false},
		{errors.New("fatal: not a git repository"), false},
		{context.DeadlineExceeded, false},
		{nil, false},
	} {
		if got := isTransientGitError(tc.err); got != tc.want {
			t.Errorf("isTransientGitError(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}

// ctx cancel during backoff aborts promptly and is NOT reported as an outage.
func TestRetryTransientGit_CtxCancelDuringBackoff(t *testing.T) {
	orig := gitRetryBackoffs
	gitRetryBackoffs = []time.Duration{time.Hour}
	t.Cleanup(func() { gitRetryBackoffs = orig })
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	start := time.Now()
	err := retryTransientGit(ctx, "git fetch", func() error { return errors.New(publickeyDenied) })
	if time.Since(start) > 5*time.Second {
		t.Fatal("backoff did not observe ctx cancellation")
	}
	if !errors.Is(err, context.Canceled) || isSourceUnavailable(err.Error()) {
		t.Fatalf("cancelled retry must surface ctx error, not source_unavailable: %v", err)
	}
}
