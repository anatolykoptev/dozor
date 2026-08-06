package deploy

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
)

// fullSHALen is the length of a full git commit SHA (SHA-1 hash, 40 hex chars).
const fullSHALen = 40

// artifactTagLen is the length of the short SHA used for artifact image tags.
// Matches the ${SHA:0:12} convention used by pre-build scripts.
const artifactTagLen = 12

// gitFullSHARunner executes `git rev-parse HEAD` in dir, returning the FULL
// 40-character commit SHA. Replaceable in tests.
var gitFullSHARunner = defaultGitFullSHARunner

//nolint:unused // DI default seam — assigned to var gitFullSHARunner, swapped in tests
func defaultGitFullSHARunner(ctx context.Context, dir string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "HEAD") //nolint:gosec // trusted config
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse HEAD: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// resolveGitFullSHA returns the FULL 40-character SHA of HEAD in dir.
// Falls back to "unknown" with a WARN log on any error. Used where the full
// SHA is required (CommitSHA, DEPLOY_SHA) — as opposed to resolveGitSHA which
// returns a short SHA for display/build-arg purposes only.
func resolveGitFullSHA(ctx context.Context, dir string) string {
	if dir == "" {
		return "unknown"
	}
	sha, err := gitFullSHARunner(ctx, dir)
	if err != nil {
		slog.Warn("deploy: cannot resolve full git SHA", "dir", dir, "error", err)
		return "unknown"
	}
	return sha
}

// artifactTagSHA derives the 12-character artifact tag from a full 40-character
// commit SHA. It is the single helper used by BOTH deploy lanes (webhook and
// manual) to derive the image tag from CommitSHA, ensuring cross-lane tag
// parity.
//
// It REJECTS anything that is not exactly 40 hex characters — a silent [:12]
// on a short string is what made the cross-lane tag mismatch invisible (the
// manual lane produced a 9-char SHA, the webhook lane produced a 40-char SHA,
// and each consumer's `if len > 12 { [:12] }` was a no-op on the already-short
// value, yielding 9 chars instead of 12).
//
// Returns the 12-char tag, or an error describing why the SHA is invalid.
// The error is non-nil for: empty string, wrong length, non-hex characters.
// Callers MUST surface the error as a deploy failure — never silently truncate.
func artifactTagSHA(commitSHA string) (string, error) {
	if len(commitSHA) != fullSHALen {
		return "", fmt.Errorf(
			"artifact tag: commit SHA %q is %d chars, expected %d (full SHA required — "+
				"a short SHA here means the tag will not match cross-lane)",
			short(commitSHA), len(commitSHA), fullSHALen,
		)
	}
	for _, r := range commitSHA {
		if !isHexRune(r) {
			return "", fmt.Errorf(
				"artifact tag: commit SHA %q contains non-hex character %q",
				commitSHA, r,
			)
		}
	}
	return commitSHA[:artifactTagLen], nil
}

func isHexRune(r rune) bool {
	switch {
	case r >= '0' && r <= '9',
		r >= 'a' && r <= 'f',
		r >= 'A' && r <= 'F':
		return true
	default:
		return false
	}
}

// validateDeploySHA asserts that commitSHA is a full 40-character hex SHA
// before it crosses the shell boundary as the DEPLOY_SHA environment variable.
// Returns an error string (matching the deploy package's string-based error
// convention) if the SHA is invalid, or empty string if valid.
//
// Contract: DEPLOY_SHA MUST be a full 40-char hex commit SHA. Pre-build
// scripts in another repo do `SHORT_SHA="${SHA:0:12}"` — a short SHA here
// makes that a no-op and produces a tag that doesn't match the webhook lane's
// 12-char tag. This check makes it structurally impossible for a short value
// to leave dozor.
func validateDeploySHA(commitSHA string) string {
	if _, err := artifactTagSHA(commitSHA); err != nil {
		return fmt.Sprintf("DEPLOY_SHA contract violation: %v", err)
	}
	return ""
}
