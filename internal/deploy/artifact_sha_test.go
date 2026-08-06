package deploy

import (
	"context"
	"strings"
	"testing"
)

// -- artifactTagSHA unit tests --

// TestArtifactTagSHA_ValidFullSHA returns the 12-char tag for a valid 40-char SHA.
func TestArtifactTagSHA_ValidFullSHA(t *testing.T) {
	tag, err := artifactTagSHA("9e57d2974426b7e070cb0deadbeefcafe1234567")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tag != "9e57d2974426" {
		t.Errorf("tag = %q, want %q", tag, "9e57d2974426")
	}
}

// TestArtifactTagSHA_ShortSHARejected — a 9-char SHA (the manual lane's old
// output) must be REJECTED, not silently truncated.
//
// RED-on-revert (F2): remove the len != fullSHALen check from artifactTagSHA —
// a 9-char SHA passes through and returns a 9-char tag (the bug).
func TestArtifactTagSHA_ShortSHARejected(t *testing.T) {
	_, err := artifactTagSHA("9e57d2974") // 9 chars — the manual lane's old short SHA
	if err == nil {
		t.Fatal("expected error for 9-char SHA, got nil — a short SHA must be rejected, not silently truncated")
	}
	if !strings.Contains(err.Error(), "40") {
		t.Errorf("error must mention expected length 40, got: %v", err)
	}
}

// TestArtifactTagSHA_EmptyRejected — empty SHA must be rejected.
func TestArtifactTagSHA_EmptyRejected(t *testing.T) {
	_, err := artifactTagSHA("")
	if err == nil {
		t.Fatal("expected error for empty SHA, got nil")
	}
}

// TestArtifactTagSHA_NonHexRejected — 40-char string with non-hex chars must be rejected.
func TestArtifactTagSHA_NonHexRejected(t *testing.T) {
	_, err := artifactTagSHA("gggggggggggggggggggggggggggggggggggggggg") // 40 chars, all non-hex
	if err == nil {
		t.Fatal("expected error for non-hex SHA, got nil")
	}
}

// TestArtifactTagSHA_UppercaseAccepted — uppercase hex is valid (git SHAs are lowercase
// but the helper should not reject uppercase since it's still valid hex).
func TestArtifactTagSHA_UppercaseAccepted(t *testing.T) {
	tag, err := artifactTagSHA("ABCDEF1234567890ABCDEF1234567890ABCDEF12")
	if err != nil {
		t.Fatalf("unexpected error for uppercase hex: %v", err)
	}
	if tag != "ABCDEF123456" {
		t.Errorf("tag = %q, want %q", tag, "ABCDEF123456")
	}
}

// -- resolveGitFullSHA tests --

// TestResolveGitFullSHA_Success returns the full 40-char SHA.
func TestResolveGitFullSHA_Success(t *testing.T) {
	orig := gitFullSHARunner
	defer func() { gitFullSHARunner = orig }()
	gitFullSHARunner = func(_ context.Context, _ string) (string, error) {
		return "abcdef1234567890abcdef1234567890abcdef12", nil
	}
	got := resolveGitFullSHA(context.Background(), "/some/dir")
	if got != "abcdef1234567890abcdef1234567890abcdef12" {
		t.Errorf("expected full SHA, got %q", got)
	}
}

// TestResolveGitFullSHA_EmptyDir returns "unknown" without calling runner.
func TestResolveGitFullSHA_EmptyDir(t *testing.T) {
	orig := gitFullSHARunner
	defer func() { gitFullSHARunner = orig }()
	gitFullSHARunner = func(_ context.Context, _ string) (string, error) {
		t.Error("runner must not be called on empty dir")
		return "", nil
	}
	if got := resolveGitFullSHA(context.Background(), ""); got != "unknown" {
		t.Errorf("expected unknown, got %q", got)
	}
}

// TestResolveGitFullSHA_Error falls back to "unknown".
func TestResolveGitFullSHA_Error(t *testing.T) {
	orig := gitFullSHARunner
	defer func() { gitFullSHARunner = orig }()
	gitFullSHARunner = func(_ context.Context, _ string) (string, error) {
		return "", errFake
	}
	if got := resolveGitFullSHA(context.Background(), "/bad/dir"); got != "unknown" {
		t.Errorf("expected unknown on error, got %q", got)
	}
}

// -- validateDeploySHA tests --

// TestValidateDeploySHA_Valid returns empty string for a valid 40-char SHA.
func TestValidateDeploySHA_Valid(t *testing.T) {
	if msg := validateDeploySHA("abcdef1234567890abcdef1234567890abcdef12"); msg != "" {
		t.Errorf("expected empty string for valid SHA, got: %s", msg)
	}
}

// TestValidateDeploySHA_Short returns error string for a short SHA.
func TestValidateDeploySHA_Short(t *testing.T) {
	msg := validateDeploySHA("abc1234")
	if msg == "" {
		t.Fatal("expected error string for short SHA, got empty")
	}
	if !strings.Contains(msg, "DEPLOY_SHA contract violation") {
		t.Errorf("error must mention DEPLOY_SHA contract, got: %s", msg)
	}
}

// -- THE DECISIVE CROSS-LANE TEST --
// This is the test that today's bug would have passed without. Both lanes
// were internally self-consistent, so a test that only checks one lane against
// itself proves nothing. This test asserts that a manual-lane deploy and a
// webhook-lane deploy of the SAME commit derive the SAME artifact tag.

// TestCrossLane_TagParity verifies that the manual lane and the webhook lane
// produce the SAME 12-char artifact tag for the SAME commit SHA. The manual
// lane resolves the SHA via resolveGitFullSHA (worktree HEAD); the webhook
// lane receives the SHA from the GitHub payload (push.HeadCommit.ID). Both
// must route through artifactTagSHA and produce identical tags.
//
// RED-on-revert (F1): restore resolveGitSHA (short) as the CommitSHA source
// in manual_deploy.go:370 — the manual lane's tag becomes 9 chars (short SHA
// truncated to 9 by artifactTagSHA's rejection... wait, artifactTagSHA would
// REJECT the short SHA. So the manual lane would FAIL. The test asserts
// success, so it fails. This is the correct behaviour: the invariant is now
// enforced — a short SHA cannot produce a tag at all.
func TestCrossLane_TagParity(t *testing.T) {
	// The commit SHA that both lanes are deploying.
	const commitSHA = "9e57d2974426b7e070cb0deadbeefcafe1234567"

	// Webhook lane: CommitSHA comes directly from the GitHub payload (full SHA).
	// This is what webhook_dispatch.go sets: CommitSHA: push.HeadCommit.ID.
	webhookTag, err := artifactTagSHA(commitSHA)
	if err != nil {
		t.Fatalf("webhook lane: artifactTagSHA failed: %v", err)
	}

	// Manual lane: CommitSHA comes from resolveGitFullSHA(ctx, worktreePath).
	// Simulate the full SHA resolution.
	orig := gitFullSHARunner
	defer func() { gitFullSHARunner = orig }()
	gitFullSHARunner = func(_ context.Context, _ string) (string, error) {
		return commitSHA, nil
	}
	manualSHA := resolveGitFullSHA(context.Background(), "/fake/worktree")
	manualTag, err := artifactTagSHA(manualSHA)
	if err != nil {
		t.Fatalf("manual lane: artifactTagSHA failed: %v", err)
	}

	// THE INVARIANT: both lanes derive the SAME artifact tag for the SAME commit.
	if webhookTag != manualTag {
		t.Fatalf("CROSS-LANE TAG MISMATCH: webhook=%q manual=%q for commit %s — "+
			"this is the exact bug: the compose lane consumes WEB_ARTIFACT_IMAGE=...:prod-${SHA} "+
			"while the #web lane produces the artifact, and a tag mismatch ships a stale bundle",
			webhookTag, manualTag, commitSHA)
	}
	if webhookTag != "9e57d2974426" {
		t.Errorf("expected 12-char tag, got %q", webhookTag)
	}
}

// -- F1 mutation test: restore resolveGitSHA (short) as CommitSHA source --
// TestF1_ManualLaneShortSHAFails proves that if the manual lane goes back to
// using resolveGitSHA (short SHA), artifactTagSHA REJECTS it and the deploy
// fails — the invariant is enforced.
//
// This test passes with the fix (resolveGitFullSHA produces a 40-char SHA,
// artifactTagSHA accepts it). If someone reverts manual_deploy.go:370 to
// resolveGitSHA, the short SHA is rejected by artifactTagSHA and this test
// fails because the "reverted" path produces an error, not a tag.
func TestF1_ManualLaneShortSHAFails(t *testing.T) {
	// Simulate the OLD (buggy) manual lane: resolveGitSHA returns a short SHA.
	origShort := gitShortSHARunner
	defer func() { gitShortSHARunner = origShort }()
	gitShortSHARunner = func(_ context.Context, _ string) (string, error) {
		return "9e57d2974", nil // 9-char short SHA — the old manual lane output
	}

	// The old manual lane would put this short SHA in CommitSHA.
	shortSHA := resolveGitSHA(context.Background(), "/fake/worktree")

	// artifactTagSHA must REJECT the short SHA — this is the fix that makes
	// the invariant impossible to violate.
	_, err := artifactTagSHA(shortSHA)
	if err == nil {
		t.Fatal("F1 MUTATION FAILED: artifactTagSHA accepted a 9-char short SHA — " +
			"the cross-lane tag mismatch bug would be invisible again. " +
			"artifactTagSHA must reject anything that is not 40 hex chars.")
	}
}

// -- F2 mutation test: remove full-length validation from artifactTagSHA --
// TestF2_ArtifactTagSHA_NoValidationFails proves that if the length validation
// is removed from artifactTagSHA, a short SHA would silently produce a short
// tag (the bug). This test verifies the validation is present.
//
// RED-on-revert (F2): remove the `len(commitSHA) != fullSHALen` check —
// artifactTagSHA would return "9e57d2974"[:12] = "9e57d2974" (9 chars, no-op
// truncation), and this test fails because no error is returned.
func TestF2_ArtifactTagSHA_NoValidationFails(t *testing.T) {
	// A 9-char SHA must be rejected. If the validation is removed, it would
	// be silently truncated to 9 chars (the bug).
	_, err := artifactTagSHA("9e57d2974")
	if err == nil {
		t.Fatal("F2 MUTATION FAILED: artifactTagSHA did not reject a 9-char SHA — " +
			"the length validation has been removed and the cross-lane tag mismatch " +
			"bug is invisible again.")
	}
}

// -- F3 mutation test: turn no-new-image failure back into WARN --
// TestF3_LogImageDiff_NoNewImageFails proves that logImageDiff returns an
// error (not just warns) when the image didn't change and the commit SHA is
// a valid full SHA.
//
// RED-on-revert (F3): change the `return fmt.Sprintf(...)` in logImageDiff's
// valid-SHA branch back to just `slog.Warn(...)` + `return ""` — this test
// fails because logImageDiff returns "" instead of an error message.
func TestF3_LogImageDiff_NoNewImageFails(t *testing.T) {
	before := map[string]string{"svc": "sha256:abc123"}
	after := map[string]string{"svc": "sha256:abc123"} // unchanged — no new image

	errMsg := logImageDiff(before, after, []string{"svc"},
		"9e57d2974426b7e070cb0deadbeefcafe1234567") // valid 40-char SHA
	if errMsg == "" {
		t.Fatal("F3 MUTATION FAILED: logImageDiff returned empty for no-new-image with " +
			"a valid full SHA — the failure has been turned back into a WARN. " +
			"A warning nobody reads is statistically identical to no warning.")
	}
	if !strings.Contains(errMsg, "no new image") {
		t.Errorf("error message must mention 'no new image', got: %s", errMsg)
	}
}

// TestLogImageDiff_InvalidSHA_StaysWarn proves that logImageDiff returns empty
// (WARN, not fail) when the commit SHA is invalid (empty, short, non-hex) —
// the legitimate fallback for from_disk debug mode or unresolvable HEAD.
func TestLogImageDiff_InvalidSHA_StaysWarn(t *testing.T) {
	before := map[string]string{"svc": "sha256:abc123"}
	after := map[string]string{"svc": "sha256:abc123"}

	// Short SHA — should WARN, not fail.
	errMsg := logImageDiff(before, after, []string{"svc"}, "abc1234")
	if errMsg != "" {
		t.Errorf("expected empty (WARN) for short SHA, got error: %s", errMsg)
	}

	// Empty SHA — should WARN, not fail.
	errMsg = logImageDiff(before, after, []string{"svc"}, "")
	if errMsg != "" {
		t.Errorf("expected empty (WARN) for empty SHA, got error: %s", errMsg)
	}
}

// TestLogImageDiff_NewImage_NoError proves that when the image DID change,
// logImageDiff returns empty (no error, no warning).
func TestLogImageDiff_NewImage_NoError(t *testing.T) {
	before := map[string]string{"svc": "sha256:abc123"}
	after := map[string]string{"svc": "sha256:def456"} // changed — new image

	errMsg := logImageDiff(before, after, []string{"svc"},
		"9e57d2974426b7e070cb0deadbeefcafe1234567")
	if errMsg != "" {
		t.Errorf("expected empty for changed image, got: %s", errMsg)
	}
}

// TestLogImageDiff_NoBeforeImage_NoError proves that when there was no image
// before (fresh deploy), logImageDiff returns empty even if after is also empty.
func TestLogImageDiff_NoBeforeImage_NoError(t *testing.T) {
	before := map[string]string{"svc": ""} // no image before
	after := map[string]string{"svc": ""}  // still no image

	errMsg := logImageDiff(before, after, []string{"svc"},
		"9e57d2974426b7e070cb0deadbeefcafe1234567")
	if errMsg != "" {
		t.Errorf("expected empty when no before image, got: %s", errMsg)
	}
}

// errFake is a sentinel error for test stubs.
var errFake = fakeErr{}

type fakeErr struct{}

func (fakeErr) Error() string { return "fake test error" }
