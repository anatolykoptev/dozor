package deploy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// Transient git source failures (GitHub ssh outage, network blip) are not
// build failures. On 2026-11-04 a ~1h45m GitHub ssh publickey outage turned every
// deploy into a "FAILED" page and bumped dozor_build_result_total{status=failure}
// although nothing in the code under deploy was wrong. The fetch/pull steps that
// run before a build therefore retry transport/auth errors with backoff, and when
// the retries are exhausted the build ends as the distinct outcome
// "source_unavailable" — one notification, no failure count.

// sourceUnavailableMarker is embedded in the error text of an exhausted transient
// fetch. Build steps pass errors around as strings (BuildResult.Error), so the
// marker is how processBuild recognises the outcome without a second channel.
const sourceUnavailableMarker = "[source_unavailable]"

// statusSourceUnavailable is the BuildResultTotal / BuildOutcome status for a
// build that could not start because the git remote was unreachable.
const statusSourceUnavailable = "source_unavailable"

// errSourceUnavailable wraps the last transient error after retries ran out.
var errSourceUnavailable = errors.New(sourceUnavailableMarker + " git remote unreachable")

// gitRetryBackoffs are the waits before retry 1..N (so N+1 attempts in total).
// A var so tests can zero it; production never mutates it.
var gitRetryBackoffs = []time.Duration{20 * time.Second, 60 * time.Second, 120 * time.Second}

// gitRetrySleep waits d or until ctx ends. Seam for tests.
var gitRetrySleep = func(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// transientGitMarkers are lowercase substrings of git/ssh output that mean the
// remote could not be reached or authenticated right now. Anything else (merge
// conflicts, bad refs, a dirty tree) is a real failure and is never retried.
var transientGitMarkers = []string{
	"permission denied (publickey)",
	"could not read from remote repository",
	"connection timed out",
	"connection reset",
	"connection closed by remote host",
	"connection closed by",
	"operation timed out",
	"could not resolve host",
	"the remote end hung up unexpectedly",
	"kex_exchange_identification",
	"ssh_exchange_identification",
	"early eof",
}

// isTransientGitError reports whether err looks like a transport/auth failure
// of the git remote. A cancelled/expired context is never transient.
func isTransientGitError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, m := range transientGitMarkers {
		if strings.Contains(msg, m) {
			return true
		}
	}
	return false
}

// retryTransientGit runs fn, retrying only transient git errors with the
// gitRetryBackoffs schedule, bounded by ctx. A non-transient error returns
// immediately and unchanged (stays a loud build failure). When every attempt
// fails transiently it returns an error wrapping errSourceUnavailable.
func retryTransientGit(ctx context.Context, what string, fn func() error) error {
	err := fn()
	for attempt := 0; err != nil && isTransientGitError(err); attempt++ {
		if attempt >= len(gitRetryBackoffs) {
			return fmt.Errorf("%s: %w after %d attempts: %w", what, errSourceUnavailable, attempt+1, err)
		}
		wait := gitRetryBackoffs[attempt]
		slog.Warn("deploy: transient git failure, retrying",
			"op", what, "attempt", attempt+1, "wait", wait, "error", err)
		if serr := gitRetrySleep(ctx, wait); serr != nil {
			return fmt.Errorf("%s: retry aborted: %w (last error: %w)", what, serr, err)
		}
		err = fn()
	}
	return err
}

// isSourceUnavailable reports whether a build error string came from an
// exhausted transient fetch.
func isSourceUnavailable(msg string) bool {
	return strings.Contains(msg, sourceUnavailableMarker)
}
