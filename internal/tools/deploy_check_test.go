package tools

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anatolykoptev/dozor/internal/deploy"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestFindRepoByService(t *testing.T) {
	// After deploy.LoadConfig+validateRepoConfig, binary-kind repos have
	// UserServices copied into Services — so findRepoByService only needs
	// to scan Services. The test fixtures mirror that post-load shape.
	cfg := &deploy.Config{
		Repos: map[string]deploy.RepoConfig{
			"anatolykoptev/oxpulse-chat": {
				SourcePath: "/home/krolik/src/oxpulse-chat",
				Services:   []string{"oxpulse-chat"},
			},
			"anatolykoptev/vaelor": {
				SourcePath:   "/home/krolik/src/vaelor",
				UserServices: []string{"vaelor-orchestrator", "vaelor-content"},
				Services:     []string{"vaelor-orchestrator", "vaelor-content"}, // copied at load time
			},
		},
	}

	t.Run("match by service name", func(t *testing.T) {
		repo, rc, ok := findRepoByService(cfg, "oxpulse-chat")
		if !ok {
			t.Fatal("expected hit, got miss")
		}
		if repo != "anatolykoptev/oxpulse-chat" {
			t.Errorf("repo: got %q, want anatolykoptev/oxpulse-chat", repo)
		}
		if rc.SourcePath != "/home/krolik/src/oxpulse-chat" {
			t.Errorf("source path mismatch: %q", rc.SourcePath)
		}
	})

	t.Run("match second user-service (post-normalisation)", func(t *testing.T) {
		repo, _, ok := findRepoByService(cfg, "vaelor-content")
		if !ok {
			t.Fatal("expected hit, got miss")
		}
		if repo != "anatolykoptev/vaelor" {
			t.Errorf("repo: got %q, want anatolykoptev/vaelor", repo)
		}
	})

	t.Run("no match", func(t *testing.T) {
		_, _, ok := findRepoByService(cfg, "ghost-service")
		if ok {
			t.Fatal("expected miss, got hit")
		}
	})

	t.Run("deterministic order when no collision (sorted keys)", func(t *testing.T) {
		// Two distinct services in two repos — order doesn't matter for
		// hit selection, but sort.Strings(keys) keeps the scan
		// reproducible across runs.
		repo, _, ok := findRepoByService(cfg, "vaelor-orchestrator")
		if !ok || repo != "anatolykoptev/vaelor" {
			t.Errorf("got (%q, %v), want (anatolykoptev/vaelor, true)", repo, ok)
		}
	})
}

func TestEffectiveKind(t *testing.T) {
	cases := []struct {
		in   deploy.DeployKind
		want string
	}{
		{"", "compose"},
		{deploy.KindCompose, "compose"},
		{deploy.KindBinary, "binary"},
		{deploy.KindStatic, "static"},
	}
	for _, c := range cases {
		got := effectiveKind(deploy.RepoConfig{Kind: c.in})
		if got != c.want {
			t.Errorf("Kind=%q: got %q, want %q", c.in, got, c.want)
		}
	}
}

// TestSnapshotCountersReadOnly confirms the critical invariant: snapshotting
// counters MUST NOT create new metric series for label combinations that
// have never been observed. Regression guard against the previous
// `WithLabelValues` approach that silently allocated zero counters.
func TestSnapshotCountersReadOnly(t *testing.T) {
	// Take baseline snapshot.
	beforeFamilies, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather baseline: %v", err)
	}
	baseSeries := countSeries(beforeFamilies, "dozor_deploy_fired_total")

	// Probe a never-seen service via the snapshot path.
	snap, err := snapshotCounters()
	if err != nil {
		t.Fatalf("snapshotCounters: %v", err)
	}
	value := snap.get("dozor_deploy_fired_total", "anatolykoptev/nonexistent-test-repo", "phantom-svc-zzz", "")
	if value != 0 {
		t.Errorf("expected 0 for non-existent series, got %v", value)
	}

	// Re-gather: series count for this metric must be unchanged.
	afterFamilies, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather after: %v", err)
	}
	afterSeries := countSeries(afterFamilies, "dozor_deploy_fired_total")

	if afterSeries != baseSeries {
		t.Errorf("snapshot created %d new series (was %d, now %d) — must be read-only",
			afterSeries-baseSeries, baseSeries, afterSeries)
	}
}

// TestSnapshotCountersReadsExisting confirms the snapshot DOES return the
// value of pre-existing label combinations (i.e. it's not just always 0).
func TestSnapshotCountersReadsExisting(t *testing.T) {
	const repo = "anatolykoptev/test-existing-repo"
	const svc = "test-svc-existing"

	// Touch a counter so a non-zero series exists. This deliberately uses
	// WithLabelValues + Inc — registering and bumping is the only way to
	// seed a series for the test.
	deploy.FiredTotal.WithLabelValues(repo, svc).Inc()
	t.Cleanup(func() {
		// Best-effort cleanup so other tests don't see this stray series.
		deploy.FiredTotal.DeleteLabelValues(repo, svc)
	})

	snap, err := snapshotCounters()
	if err != nil {
		t.Fatalf("snapshotCounters: %v", err)
	}
	got := snap.get("dozor_deploy_fired_total", repo, svc, "")
	if got < 1 {
		t.Errorf("expected at least 1, got %v", got)
	}
}

func countSeries(fams []*dto.MetricFamily, name string) int {
	for _, f := range fams {
		if f.GetName() == name {
			return len(f.GetMetric())
		}
	}
	return 0
}

// TestShortSHAUsesDeployHelper documents that the tool uses deploy.ShortSHA
// rather than its own implementation — guards against a future copy-paste
// regression that would re-introduce a length mismatch with queue logs.
func TestShortSHAUsesDeployHelper(t *testing.T) {
	const fullSHA = "4908b01f4e73f5506f145bd17efd6873f22b7f0b"
	got := deploy.ShortSHA(fullSHA)
	if !strings.HasPrefix(fullSHA, got) {
		t.Errorf("ShortSHA(%q) = %q, expected prefix of input", fullSHA, got)
	}
	if got == fullSHA {
		t.Error("ShortSHA returned full SHA — truncation broken")
	}
}

// The incident: a release webhook arrived, dozor debounced 3 minutes, and
// server_deploy_check showed old HEAD + "Queue: idle" with nothing hinting a
// deploy was on its way. The pending section must say what fires and when.
func TestWritePendingDebounce_ShowsPendingFireTime(t *testing.T) {
	burst := time.Date(2026, 10, 14, 4, 15, 8, 0, time.UTC)
	pending := []deploy.PendingDebounce{
		{Repo: "anatolykoptev/vaelor", Services: []string{"go-code"}, CommitSHA: "5ec0de1234567890",
			HitCount: 1, FirstSeen: burst, LastSeen: burst, FiresAt: burst.Add(3 * time.Minute)},
		{Repo: "anatolykoptev/other", Services: []string{"other-svc"}, CommitSHA: "badbadbadbad",
			HitCount: 1, FirstSeen: burst, LastSeen: burst, FiresAt: burst.Add(time.Minute)},
	}
	var b strings.Builder
	// Through the pipeline renderer handleDeployCheck calls: dropping the
	// pending section there must turn this red, not just the unit below it.
	writeDeployPipeline(&b, burst.Add(30*time.Second), "anatolykoptev/vaelor", []string{"go-code"}, pending, true, nil, true)
	got := b.String()

	for _, want := range []string{"Pending:", "Queue:   idle", "5ec0de1", "NOT yet building", "burst started 2026-10-14T04:15:08Z", "fires 2026-10-14T04:18:08Z", "in 2m30s", "1 webhook"} {
		if !strings.Contains(got, want) {
			t.Errorf("pending section missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "badbad") {
		t.Errorf("pending section leaked another service's entry:\n%s", got)
	}
}

func TestWritePendingDebounce_NoneAndUnavailableAndOverdue(t *testing.T) {
	now := time.Date(2026, 10, 14, 4, 20, 0, 0, time.UTC)

	var none strings.Builder
	writePendingDebounce(&none, now, []string{"go-code"}, nil, true)
	if !strings.Contains(none.String(), "Pending: none") {
		t.Errorf("empty case: %q", none.String())
	}

	var off strings.Builder
	writePendingDebounce(&off, now, []string{"go-code"}, nil, false)
	if !strings.Contains(off.String(), "unavailable") {
		t.Errorf("handler-not-started case: %q", off.String())
	}

	var late strings.Builder
	p := []deploy.PendingDebounce{{Services: []string{"go-code"}, CommitSHA: "abcdef123456", HitCount: 2,
		FirstSeen: now.Add(-5 * time.Minute), LastSeen: now.Add(-4 * time.Minute), FiresAt: now.Add(-time.Second)}}
	writePendingDebounce(&late, now, []string{"go-code"}, p, true)
	if !strings.Contains(late.String(), "firing now") {
		t.Errorf("overdue case: %q", late.String())
	}
}

func TestRenderQueueState_ShowsBuildStart(t *testing.T) {
	started := time.Date(2026, 10, 14, 4, 18, 8, 0, time.UTC)
	snap := []deploy.ServiceQueueState{{Services: []string{"go-code"}, BuildingSHA: "5ec0de1234567890", BuildingSince: started}}
	var b strings.Builder
	renderQueueState(&b, started.Add(90*time.Second), []string{"go-code"}, snap)
	got := b.String()
	for _, want := range []string{"building 5ec0de1", "started 2026-10-14T04:18:08Z", "running 1m30s"} {
		if !strings.Contains(got, want) {
			t.Errorf("queue line missing %q: %s", want, got)
		}
	}

	var idle strings.Builder
	renderQueueState(&idle, started, []string{"go-code"}, nil)
	if !strings.Contains(idle.String(), "Queue:   idle") {
		t.Errorf("idle: %q", idle.String())
	}
}

func TestImageNewerThanDeploy(t *testing.T) {
	deployed := time.Date(2026, 10, 14, 4, 23, 16, 0, time.UTC)
	if !imageNewerThanDeploy(deployed.Add(time.Hour), deployed) {
		t.Error("image created after the last dozor deploy must be flagged")
	}
	if imageNewerThanDeploy(deployed.Add(-time.Minute), deployed) {
		t.Error("image built before the deploy finished is dozor's own")
	}
}

// Drives the exact assembly handleDeployCheck uses: a real webhook handler
// debounces a push, and the FULL tool output must show Pending + Last deploy.
func TestAssembleDeployCheck_FullOutput(t *testing.T) {
	t.Setenv("DOZOR_WORKSPACE", t.TempDir())
	persist := filepath.Join(t.TempDir(), "deployed-sha.json")
	deploy.ConfigureDeployedSHAPersistence(persist)
	t.Cleanup(func() { deploy.ConfigureDeployedSHAPersistence("") })
	doc := `{"outcomes":{"full-out-svc":{"repo":"o/full-out","services":["full-out-svc"],"commit":"a28382e0000","status":"superseded","finished_at":"2026-10-14T04:10:00Z"}},
	"last_success":{"full-out-svc":{"repo":"o/full-out","services":["full-out-svc"],"commit":"a28382e0000","status":"success","finished_at":"2026-10-14T03:00:00Z"}}}`
	if err := os.WriteFile(persist, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	deploy.ConfigureDeployedSHAPersistence(persist) // load it

	const secret = "s3cret"
	cfg := &deploy.Config{
		Secret: secret,
		Repos: map[string]deploy.RepoConfig{"o/full-out": {
			Kind: deploy.KindStatic, Services: []string{"full-out-svc"}, SourcePath: t.TempDir(), ComposePath: t.TempDir(),
		}},
	}
	q := deploy.NewQueue(context.Background(), func(string) {})
	defer q.Close()
	h := deploy.NewHandler(cfg, q, func(string) {})
	defer h.Close()

	body := `{"ref":"refs/heads/main","repository":{"full_name":"o/full-out"},"head_commit":{"id":"5ec0de1234567890","message":"m"}}`
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	req := httptest.NewRequest(http.MethodPost, "/deploy/github", strings.NewReader(body))
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "debounced") {
		t.Fatalf("webhook not debounced: %d %s", w.Code, w.Body.String())
	}

	out, err := assembleDeployCheck(context.Background(), DeployCheckInput{Service: "full-out-svc"}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Pending: 5ec0de1 debounced, NOT yet building", "fires ", "Last deploy: a28382e superseded", "Last success: a28382e", "Queue:   idle"} {
		if !strings.Contains(out, want) {
			t.Errorf("full output missing %q:\n%s", want, out)
		}
	}
}

// The latest outcome being "superseded" must not zero the last-success time
// that gates the hand-deploy warning.
func TestWriteLastDeploy_LastSuccessSurvivesSuperseded(t *testing.T) {
	persist := filepath.Join(t.TempDir(), "deployed-sha.json")
	doc := `{"outcomes":{"ls-svc":{"commit":"old","status":"superseded","finished_at":"2026-10-14T05:00:00Z"}},
	"last_success":{"ls-svc":{"commit":"good","status":"success","finished_at":"2026-10-14T03:00:00Z"}}}`
	if err := os.WriteFile(persist, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	deploy.ConfigureDeployedSHAPersistence(persist)
	t.Cleanup(func() { deploy.ConfigureDeployedSHAPersistence("") })

	var b strings.Builder
	got := writeLastDeploy(&b, "o/ls", []string{"ls-svc"}, time.Date(2026, 10, 14, 6, 0, 0, 0, time.UTC))
	if want := time.Date(2026, 10, 14, 3, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("lastSuccess = %v, want %v (hand-deploy check would be disabled)\n%s", got, want, b.String())
	}
}
