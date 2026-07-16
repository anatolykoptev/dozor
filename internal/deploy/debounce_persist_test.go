package deploy

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// helper: build a Debouncer wired to a persist store at a temp path.
func newPersistingDebouncer(t *testing.T, clock Clock, dispatch DispatchFunc) (*Debouncer, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "deploy-debounce.json")
	deb := NewDebouncer(clock, dispatch)
	deb.WithPersistence(path)
	return deb, path
}

// TestDebouncePersist_SubmitWritesStateFile verifies that Submit persists the
// pending entry to disk so a restart can recover it.
func TestDebouncePersist_SubmitWritesStateFile(t *testing.T) {
	t.Parallel()
	clock := newFakeClock(time.Unix(1000, 0))
	deb, path := newPersistingDebouncer(t, clock, func(PendingEvent) {})

	deb.Submit("anatolykoptev/memdb@main#memdb-go", PendingEvent{
		Repo:      "anatolykoptev/memdb",
		Service:   "memdb-go",
		CommitSHA: "aaa1111deadbeef",
	}, 3*time.Minute)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected state file written by Submit, got err: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("state file is empty after Submit")
	}
}

// TestDebouncePersist_ReloadRearmsAndFires is the core regression test for the
// VOLATILE-PENDING-STATE class: a pending debounce survives a process restart.
// We Submit (which persists), throw away the Debouncer, then Reload into a
// fresh one and confirm the build dispatches once the remaining window elapses.
func TestDebouncePersist_ReloadRearmsAndFires(t *testing.T) {
	t.Parallel()
	start := time.Unix(2000, 0)
	clock := newFakeClock(start)

	// First lifetime: submit + persist. The original Debouncer is then dropped
	// WITHOUT its timer ever firing (simulates graceful restart / crash).
	deb1, path := newPersistingDebouncer(t, clock, func(PendingEvent) {
		t.Fatal("first debouncer must not fire — it is dropped before the window")
	})
	window := 3 * time.Minute
	deb1.Submit("k", PendingEvent{Repo: "r", Service: "s", CommitSHA: "newSHA1234567"}, window)
	// Graceful restart: Stop cancels the live timer (leaving the persisted file
	// intact) so deb1's goroutine no longer watches the shared fake clock.
	deb1.Stop(context.Background())
	// Some time passes during the "downtime", but less than the window.
	clock.Advance(60 * time.Second)

	// Second lifetime: fresh Debouncer reloads the persisted entry.
	var mu sync.Mutex
	var fired []PendingEvent
	deb2 := NewDebouncer(clock, func(ev PendingEvent) {
		mu.Lock()
		fired = append(fired, ev)
		mu.Unlock()
	})
	deb2.WithPersistence(path)
	// SHA resolver returns a DIFFERENT sha → not stale → must re-arm.
	deb2.shaResolver = func(_ context.Context, _ string) string { return "oldDEPLOYED" }

	if err := deb2.Reload(context.Background()); err != nil {
		t.Fatalf("Reload failed: %v", err)
	}
	if deb2.Pending() != 1 {
		t.Fatalf("after reload expected 1 re-armed pending entry, got %d", deb2.Pending())
	}

	// Remaining window = 3m − 60s = 2m. Advance past it.
	clock.Advance(2*time.Minute + time.Second)
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(fired) == 1 }, time.Second, "one fire after reload+rearm")

	mu.Lock()
	defer mu.Unlock()
	if fired[0].CommitSHA != "newSHA1234567" {
		t.Errorf("fired commit = %q, want persisted SHA", fired[0].CommitSHA)
	}
}

// TestDebouncePersist_ReloadFiresOnBootWhenDeadlinePassed verifies that an
// entry whose absolute deadline already elapsed during downtime fires
// immediately on reload (still routed through dispatch, not skipped).
func TestDebouncePersist_ReloadFiresOnBootWhenDeadlinePassed(t *testing.T) {
	t.Parallel()
	start := time.Unix(3000, 0)
	clock := newFakeClock(start)

	deb1, path := newPersistingDebouncer(t, clock, func(PendingEvent) {})
	window := 30 * time.Second
	deb1.Submit("k", PendingEvent{Repo: "r", Service: "s", CommitSHA: "freshSHA999"}, window)
	deb1.Stop(context.Background()) // graceful restart; persisted file survives

	// Downtime far exceeds the window → deadline is in the past at reload.
	clock.Advance(10 * time.Minute)

	var mu sync.Mutex
	var fired []PendingEvent
	deb2 := NewDebouncer(clock, func(ev PendingEvent) {
		mu.Lock()
		fired = append(fired, ev)
		mu.Unlock()
	})
	deb2.WithPersistence(path)
	deb2.shaResolver = func(_ context.Context, _ string) string { return "somethingElse" }

	if err := deb2.Reload(context.Background()); err != nil {
		t.Fatalf("Reload failed: %v", err)
	}

	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(fired) == 1 }, time.Second, "fire-on-boot")
	mu.Lock()
	defer mu.Unlock()
	if fired[0].CommitSHA != "freshSHA999" {
		t.Errorf("fired commit = %q, want persisted SHA", fired[0].CommitSHA)
	}
}

// TestDebouncePersist_ReloadStaleSkip verifies that if the persisted commit is
// already the deployed HEAD, the reload does NOT rebuild (no-stale-rebuild
// invariant).
func TestDebouncePersist_ReloadStaleSkip(t *testing.T) {
	t.Parallel()
	start := time.Unix(4000, 0)
	clock := newFakeClock(start)

	deb1, path := newPersistingDebouncer(t, clock, func(PendingEvent) {})
	window := 30 * time.Second
	deb1.Submit("k", PendingEvent{
		Repo:      "r",
		Service:   "s",
		CommitSHA: "abcdef1234567890",
		Config:    RepoConfig{DeployClonePath: "/tmp/clone"},
	}, window)
	deb1.Stop(context.Background()) // graceful restart; persisted file survives
	clock.Advance(10 * time.Minute) // deadline passed; would fire if not stale

	var mu sync.Mutex
	var fired []PendingEvent
	deb2 := NewDebouncer(clock, func(ev PendingEvent) {
		mu.Lock()
		fired = append(fired, ev)
		mu.Unlock()
	})
	deb2.WithPersistence(path)
	// Deployed HEAD == persisted SHA (short form) → stale, must skip.
	deb2.shaResolver = func(_ context.Context, _ string) string { return ShortSHA("abcdef1234567890") }

	if err := deb2.Reload(context.Background()); err != nil {
		t.Fatalf("Reload failed: %v", err)
	}

	// Give any (incorrect) async dispatch a chance to land, then assert none.
	time.Sleep(20 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(fired) != 0 {
		t.Fatalf("stale entry must NOT rebuild; got %d dispatches", len(fired))
	}
	if deb2.Pending() != 0 {
		t.Fatalf("stale entry must not be re-armed; pending = %d", deb2.Pending())
	}
}

// TestDebouncePersist_DedupOneEntryPerKey verifies that coalescing multiple
// Submits for one key persists exactly one entry → at most one re-arm/fire.
func TestDebouncePersist_DedupOneEntryPerKey(t *testing.T) {
	t.Parallel()
	start := time.Unix(5000, 0)
	clock := newFakeClock(start)

	deb1, path := newPersistingDebouncer(t, clock, func(PendingEvent) {})
	window := 3 * time.Minute
	deb1.Submit("k", PendingEvent{Repo: "r", Service: "s", CommitSHA: "sha-A"}, window)
	clock.Advance(10 * time.Second)
	deb1.Submit("k", PendingEvent{Repo: "r", Service: "s", CommitSHA: "sha-B"}, window)
	clock.Advance(10 * time.Second)
	deb1.Submit("k", PendingEvent{Repo: "r", Service: "s", CommitSHA: "sha-C"}, window)
	deb1.Stop(context.Background()) // graceful restart; persisted file survives

	var mu sync.Mutex
	var fired []PendingEvent
	deb2 := NewDebouncer(clock, func(ev PendingEvent) {
		mu.Lock()
		fired = append(fired, ev)
		mu.Unlock()
	})
	deb2.WithPersistence(path)
	deb2.shaResolver = func(_ context.Context, _ string) string { return "deployed-old" }

	if err := deb2.Reload(context.Background()); err != nil {
		t.Fatalf("Reload failed: %v", err)
	}
	if deb2.Pending() != 1 {
		t.Fatalf("expected exactly 1 re-armed entry (dedup by key), got %d", deb2.Pending())
	}

	clock.Advance(window + time.Second)
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(fired) == 1 }, time.Second, "single fire")
	mu.Lock()
	defer mu.Unlock()
	if len(fired) != 1 {
		t.Fatalf("dedup invariant broken: %d fires", len(fired))
	}
	if fired[0].CommitSHA != "sha-C" {
		t.Errorf("fired commit = %q, want newest (sha-C)", fired[0].CommitSHA)
	}
}

// TestDebouncePersist_ReloadToleratesMissingAndCorruptFile verifies dozor does
// not crash on a missing or garbage state file (deploy orchestrator must boot).
func TestDebouncePersist_ReloadToleratesMissingAndCorruptFile(t *testing.T) {
	t.Parallel()

	t.Run("missing file", func(t *testing.T) {
		t.Parallel()
		clock := newFakeClock(time.Unix(0, 0))
		deb := NewDebouncer(clock, func(PendingEvent) { t.Fatal("must not dispatch") })
		deb.WithPersistence(filepath.Join(t.TempDir(), "does-not-exist.json"))
		if err := deb.Reload(context.Background()); err != nil {
			t.Fatalf("missing file must be tolerated, got: %v", err)
		}
		if deb.Pending() != 0 {
			t.Fatalf("pending = %d, want 0", deb.Pending())
		}
	})

	t.Run("corrupt file", func(t *testing.T) {
		t.Parallel()
		clock := newFakeClock(time.Unix(0, 0))
		path := filepath.Join(t.TempDir(), "deploy-debounce.json")
		if err := os.WriteFile(path, []byte("{ not json"), 0o600); err != nil {
			t.Fatal(err)
		}
		deb := NewDebouncer(clock, func(PendingEvent) { t.Fatal("must not dispatch") })
		deb.WithPersistence(path)
		// A corrupt state file silently drops every queued build it held — the
		// recovery path's own silent-failure hole. Assert it bumps reload_error
		// so a non-zero counter can alert. Delta (not absolute) because the
		// counter is a package global; corrupt/unreadable is the ONLY path that
		// touches reload_error, so the delta is deterministic even under -parallel.
		before := testutil.ToFloat64(DebouncePersistTotal.WithLabelValues("", "", "reload_error"))
		if err := deb.Reload(context.Background()); err != nil {
			t.Fatalf("corrupt file must be tolerated (log + continue), got: %v", err)
		}
		if deb.Pending() != 0 {
			t.Fatalf("pending = %d, want 0", deb.Pending())
		}
		if delta := testutil.ToFloat64(DebouncePersistTotal.WithLabelValues("", "", "reload_error")) - before; delta != 1 {
			t.Errorf("corrupt reload must bump reload_error by 1, got delta %v", delta)
		}
	})
}

// TestDebouncePersist_RepoConfigRoundTrip guards the on-disk schema. The
// persisted entry embeds a full RepoConfig snapshot, marshalled with
// encoding/json — but RepoConfig carries ONLY yaml: tags (config.go), so JSON
// falls back to exported Go field names. That round-trips correctly for every
// field today; this test fails loudly if a future field is added that does not
// survive marshal→unmarshal (e.g. an unexported field, or a type without a
// JSON-stable representation), which would silently change the schema and break
// forward-read of an in-flight state file across the deploy that introduces it.
func TestDebouncePersist_RepoConfigRoundTrip(t *testing.T) {
	t.Parallel()
	// Every field set non-zero so a future addition that breaks the round-trip
	// is caught here, not in production.
	cfg := RepoConfig{
		Kind:                    KindBinary,
		Branch:                  "release",
		ComposePath:             "compose/app.yml",
		NoCache:                 true,
		SourcePath:              "/home/krolik/src/app",
		Services:                []string{"app", "worker"},
		BuildCmd:                []string{"go", "build", "-o", "/bin/app", "./cmd/app"},
		UserServices:            []string{"app.service", "worker.service"},
		SmokeURL:                "https://app.example/health",
		CanarySmokeTimeout:      Duration{D: 30 * time.Second},
		CanarySmokeWindow:       Duration{D: 45 * time.Second},
		BuildPaths:              []string{"app/**", "go.mod"},
		SkipPaths:               []string{"docs/**"},
		Profile:                 "go-cmd",
		BuildPathsExtra:         []string{"extra/**"},
		SkipPathsExtra:          []string{"tmp/**"},
		DebounceSeconds:         180,
		StaticDeployScript:      "/home/krolik/bin/deploy.sh",
		PruneBuildkitCache:      true,
		BuildTimeout:            Duration{D: 45 * time.Minute},
		Heavy:                   true,
		IgnoreNoAutoDeployLabel: true,
		DeployClonePath:         "/home/krolik/deploy/krolik-server",
	}
	orig := persistFile{Entries: []persistedEntry{{
		Key:      "anatolykoptev/app@release#app",
		Event:    PendingEvent{Repo: "anatolykoptev/app", Service: "app", CommitSHA: "abc123def456", Config: cfg},
		Deadline: time.Unix(1000, 0).UTC(),
	}}}

	data, err := json.MarshalIndent(orig, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got persistFile
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(got.Entries))
	}
	if !reflect.DeepEqual(got.Entries[0].Event.Config, cfg) {
		t.Errorf("RepoConfig did not survive JSON round-trip:\n got  %+v\n want %+v", got.Entries[0].Event.Config, cfg)
	}
}
