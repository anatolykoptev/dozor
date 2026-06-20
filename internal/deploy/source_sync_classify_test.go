package deploy

import (
	"bufio"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// --- DI-swap helpers for the P0/P1 seams (mirror source_sync_test.go style) ---

func withGitRevListCount(t *testing.T, fn func(context.Context, string, string) (int, error)) {
	t.Helper()
	orig := gitRevListCountRunner
	gitRevListCountRunner = fn
	t.Cleanup(func() { gitRevListCountRunner = orig })
}

func withGitLsTree(t *testing.T, fn func(context.Context, string, string) (map[string]struct{}, error)) {
	t.Helper()
	orig := gitLsTreeRunner
	gitLsTreeRunner = fn
	t.Cleanup(func() { gitLsTreeRunner = orig })
}

func withGitIsTracked(t *testing.T, fn func(context.Context, string, string) bool) {
	t.Helper()
	orig := gitIsTracked
	gitIsTracked = fn
	t.Cleanup(func() { gitIsTracked = orig })
}

func withQuarantineMover(t *testing.T, fn func(string, string) error) {
	t.Helper()
	orig := quarantineMover
	quarantineMover = fn
	t.Cleanup(func() { quarantineMover = orig })
}

func withQuarantineEnabled(t *testing.T, enabled bool) {
	t.Helper()
	orig := quarantineEnabled
	quarantineEnabled = enabled
	t.Cleanup(func() { quarantineEnabled = orig })
}

// ---------------------------------------------------------------------------
// Golden drift-guard: classifySyncState must match every fixture row. The same
// testdata/sync-classification.golden.tsv is asserted by the shell test, so the
// Go and bash classifiers cannot diverge.
// ---------------------------------------------------------------------------

func TestClassifySyncState_Golden(t *testing.T) {
	f, err := os.Open(filepath.Join("testdata", "sync-classification.golden.tsv"))
	if err != nil {
		t.Fatalf("open golden fixture: %v", err)
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	rows := 0
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		cols := strings.Split(line, "\t")
		if len(cols) != 7 {
			t.Fatalf("malformed golden row (want 7 cols, got %d): %q", len(cols), line)
		}
		name := cols[0]
		tracked := mustAtoi(t, cols[1])
		ahead := mustAtoi(t, cols[2])
		behind := mustAtoi(t, cols[3])
		collision := cols[4] == "1"
		quar := cols[5] == "1"
		want := sourceSyncOutcome(cols[6])

		got := classifySyncState(tracked, ahead, behind, collision, quar)
		if got != want {
			t.Errorf("%s: classifySyncState(tracked=%d ahead=%d behind=%d collision=%v quar=%v) = %q, want %q",
				name, tracked, ahead, behind, collision, quar, got, want)
		}
		rows++
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan golden fixture: %v", err)
	}
	if rows == 0 {
		t.Fatal("golden fixture had zero data rows — fixture not loaded")
	}
}

func mustAtoi(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		t.Fatalf("atoi %q: %v", s, err)
	}
	return n
}

// ---------------------------------------------------------------------------
// P0: the go-job regression. A clean ancestor (ahead==0) whose ff fails must be
// classified by the collision class, NOT skipped_diverged.
// ---------------------------------------------------------------------------

func TestSyncOnBranch_CleanAncestorFFFail_IsNotDiverged(t *testing.T) {
	dir := mustGitRepoWithOriginMain(t)
	withSourceSyncEnabled(t, true)
	withQuarantineEnabled(t, false) // quarantine off → untracked_collision label
	withGitIndexLock(t, func(string) bool { return false })
	withGitStatus(t, func(_ context.Context, _ string) ([]byte, error) {
		return []byte("?? docs/architecture/x.md\n"), nil // untracked only — passes the dirty gate
	})
	withGitCurrentBranch(t, func(_ context.Context, _ string) (string, error) { return "main", nil })
	withGitFetch(t, func(_ context.Context, _, _ string) error { return nil })
	// FETCH_HEAD != HEAD → an ff is attempted.
	withGitRevParse(t, func(_ context.Context, _, ref string) (string, error) {
		if ref == "FETCH_HEAD" {
			return "new", nil
		}
		return "old", nil
	})
	withGitPullFF(t, func(_ context.Context, _, _ string) error {
		return errors.New("untracked working tree files would be overwritten by merge")
	})
	// ahead == 0 → clean ancestor, NOT diverged.
	withGitRevListCount(t, func(_ context.Context, _, revRange string) (int, error) {
		if strings.HasSuffix(revRange, "..HEAD") {
			return 0, nil // ahead == 0
		}
		return 1, nil
	})

	got := syncSourceCheckout(context.Background(), "anatolykoptev/go-job", dir, "/clone")
	if got != syncUntrackedCollision {
		t.Fatalf("clean ancestor + untracked collision (quarantine off): got %q, want %q (must NOT be %q)",
			got, syncUntrackedCollision, syncDiverged)
	}
}

func TestSyncOnBranch_FFFail_AheadGreaterZero_IsDiverged(t *testing.T) {
	dir := mustGitRepoWithOriginMain(t)
	withSourceSyncEnabled(t, true)
	withGitIndexLock(t, func(string) bool { return false })
	withGitStatus(t, func(_ context.Context, _ string) ([]byte, error) { return nil, nil })
	withGitCurrentBranch(t, func(_ context.Context, _ string) (string, error) { return "main", nil })
	withGitFetch(t, func(_ context.Context, _, _ string) error { return nil })
	withGitRevParse(t, func(_ context.Context, _, ref string) (string, error) {
		if ref == "FETCH_HEAD" {
			return "new", nil
		}
		return "old", nil
	})
	withGitPullFF(t, func(_ context.Context, _, _ string) error { return errors.New("not a fast-forward") })
	withGitRevListCount(t, func(_ context.Context, _, revRange string) (int, error) {
		if strings.HasSuffix(revRange, "..HEAD") {
			return 2, nil // ahead > 0 → genuine divergence
		}
		return 0, nil
	})

	got := syncSourceCheckout(context.Background(), "r", dir, "/clone")
	if got != syncDiverged {
		t.Fatalf("ff fail with ahead>0: got %q, want %q", got, syncDiverged)
	}
}

func TestSyncOnBranch_FFFail_AheadCountError_IsError(t *testing.T) {
	dir := mustGitRepoWithOriginMain(t)
	withSourceSyncEnabled(t, true)
	withGitIndexLock(t, func(string) bool { return false })
	withGitStatus(t, func(_ context.Context, _ string) ([]byte, error) { return nil, nil })
	withGitCurrentBranch(t, func(_ context.Context, _ string) (string, error) { return "main", nil })
	withGitFetch(t, func(_ context.Context, _, _ string) error { return nil })
	withGitRevParse(t, func(_ context.Context, _, ref string) (string, error) {
		if ref == "FETCH_HEAD" {
			return "new", nil
		}
		return "old", nil
	})
	withGitPullFF(t, func(_ context.Context, _, _ string) error { return errors.New("boom") })
	withGitRevListCount(t, func(_ context.Context, _, _ string) (int, error) {
		return 0, errors.New("rev-list exploded")
	})

	got := syncSourceCheckout(context.Background(), "r", dir, "/clone")
	if got != syncError {
		t.Fatalf("ff fail with un-computable ahead: got %q, want %q", got, syncError)
	}
}

// ---------------------------------------------------------------------------
// P1: quarantine + retry behaviour.
// ---------------------------------------------------------------------------

// onBranchCollisionFixture wires the common seams for a clean-ancestor ff-fail
// (ahead==0) on-branch case, with the given untracked status output, and returns
// dir. Callers add the ls-tree / mover / retry-pull seams.
func onBranchCollisionFixture(t *testing.T, statusOut string) string {
	t.Helper()
	dir := mustGitRepoWithOriginMain(t)
	withSourceSyncEnabled(t, true)
	withGitIndexLock(t, func(string) bool { return false })
	withGitStatus(t, func(_ context.Context, _ string) ([]byte, error) { return []byte(statusOut), nil })
	withGitCurrentBranch(t, func(_ context.Context, _ string) (string, error) { return "main", nil })
	withGitFetch(t, func(_ context.Context, _, _ string) error { return nil })
	withGitRevListCount(t, func(_ context.Context, _, revRange string) (int, error) {
		if strings.HasSuffix(revRange, "..HEAD") {
			return 0, nil // ahead == 0 → clean ancestor
		}
		return 1, nil
	})
	return dir
}

func TestQuarantine_MovesColliders_RetrySucceeds_FFAfterQuarantine(t *testing.T) {
	dir := onBranchCollisionFixture(t, "?? docs/architecture/REPORTS-EXTERNAL.md\n?? scratch.txt\n")
	withQuarantineEnabled(t, true)

	// First ff attempt fails (collision), retry succeeds.
	pullCalls := 0
	withGitPullFF(t, func(_ context.Context, _, _ string) error {
		pullCalls++
		if pullCalls == 1 {
			return errors.New("untracked working tree files would be overwritten by merge")
		}
		return nil
	})
	withGitRevParse(t, func(_ context.Context, _, ref string) (string, error) {
		if ref == "FETCH_HEAD" {
			return "new", nil
		}
		return "old", nil
	})
	// Only REPORTS-EXTERNAL.md is tracked by origin; scratch.txt is not.
	withGitLsTree(t, func(_ context.Context, _, _ string) (map[string]struct{}, error) {
		return map[string]struct{}{"docs/architecture/REPORTS-EXTERNAL.md": {}}, nil
	})
	withGitIsTracked(t, func(_ context.Context, _, _ string) bool { return false }) // pre-check says untracked
	var moved []string
	withQuarantineMover(t, func(srcAbs, _ string) error { moved = append(moved, srcAbs); return nil })

	got := syncSourceCheckout(context.Background(), "anatolykoptev/go-job", dir, "/clone")
	if got != syncFFAfterQuarantine {
		t.Fatalf("quarantine+retry-ok: got %q, want %q", got, syncFFAfterQuarantine)
	}
	if pullCalls != 2 {
		t.Fatalf("expected exactly 2 ff attempts (initial + one retry), got %d", pullCalls)
	}
	if len(moved) != 1 || !strings.HasSuffix(moved[0], filepath.Join("docs", "architecture", "REPORTS-EXTERNAL.md")) {
		t.Fatalf("expected only the colliding tracked-by-origin file moved, got %v", moved)
	}
}

func TestQuarantine_NonCollidingUntrackedNotMoved(t *testing.T) {
	// An untracked file that origin does NOT track is not a collider → never moved;
	// with no colliders moved, the outcome stays the collision class (ff not retried
	// to success because nothing was cleared).
	dir := onBranchCollisionFixture(t, "?? scratch-only.txt\n")
	withQuarantineEnabled(t, true)
	withGitPullFF(t, func(_ context.Context, _, _ string) error {
		return errors.New("untracked working tree files would be overwritten by merge")
	})
	withGitRevParse(t, func(_ context.Context, _, ref string) (string, error) {
		if ref == "FETCH_HEAD" {
			return "new", nil
		}
		return "old", nil
	})
	withGitLsTree(t, func(_ context.Context, _, _ string) (map[string]struct{}, error) {
		return map[string]struct{}{"some/other/path.go": {}}, nil // scratch-only.txt not in incoming
	})
	moverCalled := false
	withQuarantineMover(t, func(_, _ string) error { moverCalled = true; return nil })

	got := syncSourceCheckout(context.Background(), "r", dir, "/clone")
	if got != syncUntrackedCollision {
		t.Fatalf("no real collider: got %q, want %q", got, syncUntrackedCollision)
	}
	if moverCalled {
		t.Fatal("no file should be moved when no untracked path intersects the incoming tree")
	}
}

func TestQuarantine_TrackedColliderNeverMoved_Aborts(t *testing.T) {
	// Defence-in-depth: if ls-files --error-unmatch says a "collider" is actually
	// tracked, the whole quarantine aborts and NO file is moved.
	dir := onBranchCollisionFixture(t, "?? go.sum\n")
	withQuarantineEnabled(t, true)
	pullCalls := 0
	withGitPullFF(t, func(_ context.Context, _, _ string) error {
		pullCalls++
		return errors.New("untracked working tree files would be overwritten by merge")
	})
	withGitRevParse(t, func(_ context.Context, _, ref string) (string, error) {
		if ref == "FETCH_HEAD" {
			return "new", nil
		}
		return "old", nil
	})
	withGitLsTree(t, func(_ context.Context, _, _ string) (map[string]struct{}, error) {
		return map[string]struct{}{"go.sum": {}}, nil
	})
	withGitIsTracked(t, func(_ context.Context, _, _ string) bool { return true }) // it IS tracked → abort
	moverCalled := false
	withQuarantineMover(t, func(_, _ string) error { moverCalled = true; return nil })

	got := syncSourceCheckout(context.Background(), "r", dir, "/clone")
	if moverCalled {
		t.Fatal("a tracked collider must NEVER be moved")
	}
	if got != syncUntrackedCollision {
		t.Fatalf("tracked-collider abort: got %q, want %q (tree intact, collision remains)", got, syncUntrackedCollision)
	}
	if pullCalls != 1 {
		t.Fatalf("ff must not be retried when quarantine aborted, got %d pulls", pullCalls)
	}
}

func TestQuarantine_OverCap_NotBulkMoved(t *testing.T) {
	// >maxQuarantineColliders untracked colliders → refuse the bulk move, escalate.
	var sb strings.Builder
	incoming := make(map[string]struct{}, maxQuarantineColliders+5)
	for i := 0; i < maxQuarantineColliders+5; i++ {
		p := "f" + strconv.Itoa(i) + ".txt"
		sb.WriteString("?? " + p + "\n")
		incoming[p] = struct{}{}
	}
	dir := onBranchCollisionFixture(t, sb.String())
	withQuarantineEnabled(t, true)
	withGitPullFF(t, func(_ context.Context, _, _ string) error {
		return errors.New("untracked working tree files would be overwritten by merge")
	})
	withGitRevParse(t, func(_ context.Context, _, ref string) (string, error) {
		if ref == "FETCH_HEAD" {
			return "new", nil
		}
		return "old", nil
	})
	withGitLsTree(t, func(_ context.Context, _, _ string) (map[string]struct{}, error) { return incoming, nil })
	moverCalled := false
	withQuarantineMover(t, func(_, _ string) error { moverCalled = true; return nil })

	got := syncSourceCheckout(context.Background(), "r", dir, "/clone")
	if got != syncQuarantineCapped {
		t.Fatalf("over-cap: got %q, want %q", got, syncQuarantineCapped)
	}
	if moverCalled {
		t.Fatal("over-cap must NOT move any file (escalate, don't bulk-move)")
	}
}

func TestQuarantine_FlagOff_LeavesCollisionUntouched(t *testing.T) {
	dir := onBranchCollisionFixture(t, "?? docs/architecture/REPORTS-EXTERNAL.md\n")
	withQuarantineEnabled(t, false) // flag OFF
	withGitPullFF(t, func(_ context.Context, _, _ string) error {
		return errors.New("untracked working tree files would be overwritten by merge")
	})
	withGitRevParse(t, func(_ context.Context, _, ref string) (string, error) {
		if ref == "FETCH_HEAD" {
			return "new", nil
		}
		return "old", nil
	})
	moverCalled := false
	withQuarantineMover(t, func(_, _ string) error { moverCalled = true; return nil })
	lsTreeCalled := false
	withGitLsTree(t, func(_ context.Context, _, _ string) (map[string]struct{}, error) {
		lsTreeCalled = true
		return nil, nil
	})

	got := syncSourceCheckout(context.Background(), "r", dir, "/clone")
	if got != syncUntrackedCollision {
		t.Fatalf("flag off: got %q, want %q", got, syncUntrackedCollision)
	}
	if moverCalled || lsTreeCalled {
		t.Fatal("flag off must not detect or move anything")
	}
}

// untrackedPaths must unquote porcelain-quoted paths and ignore non-?? lines.
func TestUntrackedPaths_ParsesAndUnquotes(t *testing.T) {
	out := " M tracked.go\n?? plain.txt\n?? \"with space.md\"\nA  staged.go\n"
	got := untrackedPaths(out)
	want := []string{"plain.txt", "with space.md"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("path %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

// repoSlug must flatten owner/name so the quarantine path can't escape the root.
func TestRepoSlug_FlattensSlash(t *testing.T) {
	if got := repoSlug("anatolykoptev/go-job"); got != "anatolykoptev-go-job" {
		t.Fatalf("repoSlug: got %q", got)
	}
	if got := repoSlug(""); got != "unknown" {
		t.Fatalf("repoSlug empty: got %q", got)
	}
}
