package agent

import (
	"context"
	"strings"
	"time"

	"github.com/anatolykoptev/dozor/internal/mcpclient"
)

// snapshotQuery is the semantic search used at agent startup to pull the
// handful of most-relevant operational facts into the system prompt. It is
// intentionally generic so the top-k result set naturally surfaces whatever
// the KB currently knows about the infrastructure.
const snapshotQuery = "infrastructure state services configuration architecture"

// snapshotTopK is the number of memories pulled at startup. Small on purpose
// — this is background context, not exhaustive documentation.
const snapshotTopK = 5

// snapshotTimeout bounds how long the startup call can wait. Startup must
// remain fast even when MemDB is slow or unreachable; fall back to an empty
// snapshot instead of blocking.
const snapshotTimeout = 3 * time.Second

// BuildStartupSnapshot queries the configured searcher for a small digest of
// operational facts and formats it for injection into the system prompt.
//
// Returns the empty string silently in three conditions: the searcher is nil,
// the search returns no memories, or the search fails (including
// ErrKBUnavailable from an open circuit breaker). Startup MUST NOT block or
// fail on snapshot errors — the agent can operate without a snapshot.
func BuildStartupSnapshot(ctx context.Context, searcher *mcpclient.KBSearcher) string {
	if searcher == nil {
		return ""
	}
	subCtx, cancel := context.WithTimeout(ctx, snapshotTimeout)
	defer cancel()

	raw, err := searcher.Search(subCtx, snapshotQuery, snapshotTopK)
	if err != nil {
		return ""
	}
	if raw == "" || strings.Contains(raw, "No relevant knowledge found") {
		return ""
	}

	return formatSnapshot(raw)
}

// formatSnapshot wraps the raw search output in a clearly labelled XML-like
// block so the agent recognises it as boot-time context and does not treat
// it as a user message.
func formatSnapshot(raw string) string {
	var b strings.Builder
	b.WriteString("<startup_snapshot source=\"memdb_search\">\n")
	b.WriteString(strings.TrimRight(raw, "\n"))
	b.WriteString("\n</startup_snapshot>\n\n")
	b.WriteString("<!-- The snapshot above is boot-time context from MemDB. Treat entries as stable infrastructure facts that passed the Phase 6.1 save validator. Do not echo them back as if they were a user query. -->")
	return b.String()
}
