package deploy

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// processBuild must record the outcome of every build (any status), and the
// record must survive a "restart" (re-Configure from the same receipt file).
func TestProcessBuild_RecordsOutcomeAndSurvivesRestart(t *testing.T) {
	defer zeroDelays(t)()

	const cloneSHA = "c10a4b2d3e4f5a6b7c8d9e0f1a2b3c4d5e6f7a8b"
	withGitFetch(t, func(_ context.Context, _, _ string) error { return nil })
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

	persist := filepath.Join(t.TempDir(), "deployed-sha.json")
	ConfigureDeployedSHAPersistence(persist)
	t.Cleanup(func() { ConfigureDeployedSHAPersistence("") })

	ctx := context.Background()
	q := NewQueue(ctx, func(string) {})
	defer q.Close()
	req := makeReq("/fake/compose")
	req.Repo = "test/outcome-success"
	req.CommitSHA = "aaaa1111bbbb2222"
	req.Config.DeployClonePath = "/fake/clone"
	svc := req.Config.Services[0]

	q.processBuild(ctx, req, false)

	check := func(stage string) {
		t.Helper()
		o, ok := LastBuildOutcome(svc)
		if !ok {
			t.Fatalf("%s: no outcome recorded for %q", stage, svc)
		}
		if o.Status != "success" || o.CommitSHA != req.CommitSHA || o.FinishedAt.IsZero() {
			t.Errorf("%s: outcome = %+v, want success/%s with a finish time", stage, o, req.CommitSHA)
		}
	}
	check("after build")

	// Simulate a dozor restart: wipe memory, reload from the receipt file.
	ConfigureDeployedSHAPersistence(persist)
	check("after restart")
	if !BuildOutcomesDurable() {
		t.Error("BuildOutcomesDurable() = false with a receipt file configured")
	}
}

// A pending build replaced by a newer commit is recorded as superseded, and
// the receipt SHA is not touched.
func TestSubmit_SupersededRecordsOutcome(t *testing.T) {
	ConfigureDeployedSHAPersistence("")
	q, _ := newTestQueue()
	req := makeReq("/fake/compose")
	req.Repo = "test/outcome-superseded"
	req.CommitSHA = "old0000old0000"
	const svc = "outcome-superseded-svc" // unique: the outcome map is process-global
	req.Config.Services = []string{svc}

	q.Submit(req)
	newer := req
	newer.CommitSHA = "new1111new1111"
	q.Submit(newer)

	o, ok := LastBuildOutcome(svc)
	if !ok || o.Status != OutcomeSuperseded || o.CommitSHA != req.CommitSHA {
		t.Fatalf("outcome = %+v (ok=%v), want superseded for %s", o, ok, req.CommitSHA)
	}
	if BuildOutcomesDurable() {
		t.Error("BuildOutcomesDurable() = true with no receipt file")
	}
}

func TestDebouncer_Snapshot_ReportsFireTime(t *testing.T) {
	d := NewDebouncer(nil, func(PendingEvent) {})
	defer d.Stop(nil)
	rc := RepoConfig{Services: []string{"go-code"}}
	before := time.Now()
	d.Submit("anatolykoptev/vaelor@main#go-code", PendingEvent{
		Repo: "anatolykoptev/vaelor", Service: "go-code", CommitSHA: "ffff0000ffff0000", Config: rc,
	}, 3*time.Minute)

	got := d.Snapshot()
	if len(got) != 1 {
		t.Fatalf("Snapshot() = %d entries, want 1", len(got))
	}
	p := got[0]
	if p.CommitSHA != "ffff0000ffff0000" || p.HitCount != 1 || len(p.Services) != 1 || p.Services[0] != "go-code" {
		t.Errorf("unexpected entry: %+v", p)
	}
	if want := p.LastSeen.Add(3 * time.Minute); !p.FiresAt.Equal(want) || p.FiresAt.Before(before.Add(3*time.Minute)) {
		t.Errorf("FiresAt = %v, want LastSeen+3m = %v", p.FiresAt, want)
	}
}
