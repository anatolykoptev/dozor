package agent

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/anatolykoptev/dozor/internal/provider"
)

const (
	compactionThreshold = 24
	compactionKeep      = 8
)

// CompactSession summarizes old messages via LLM and truncates history.
// No-op if session has fewer than compactionThreshold messages.
func (l *Loop) CompactSession(ctx context.Context, sessionKey string) {
	if l.sessions == nil || l.sessions.Len(sessionKey) < compactionThreshold {
		return
	}

	removed := l.sessions.Truncate(sessionKey, compactionKeep)
	if len(removed) == 0 {
		return
	}

	var sb strings.Builder
	for _, m := range removed {
		sb.WriteString(fmt.Sprintf("[%s]: %s\n", m.Role, m.Content))
	}

	existingSummary := l.sessions.GetSummary(sessionKey)
	prompt := "Summarize this conversation concisely, preserving key facts, decisions, and action items. "
	if existingSummary != "" {
		prompt += "Previous summary:\n" + existingSummary + "\n\nNew messages:\n"
	} else {
		prompt += "Messages:\n"
	}
	prompt += sb.String()

	resp, err := l.provider.Chat(
		[]provider.Message{
			{Role: "system", Content: "You are a concise conversation summarizer. Output only the summary, no preamble."},
			{Role: "user", Content: prompt},
		},
		nil,
	)
	if err != nil {
		slog.Warn("compaction summarization failed", slog.Any("error", err))
		return
	}

	summary := strings.TrimSpace(resp.Content)
	if summary != "" {
		l.sessions.SetSummary(sessionKey, summary)
		_ = l.sessions.Save(sessionKey)
		slog.Info("session compacted", slog.String("key", sessionKey),
			slog.Int("removed", len(removed)), slog.Int("summary_len", len(summary)))
	}
}
