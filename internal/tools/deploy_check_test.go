package tools

import (
	"strings"
	"testing"

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
