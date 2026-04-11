package mcpclient

import (
	"errors"
	"regexp"
	"strings"
)

// Phase 6.1 — structured incident validation for memdb_save.
//
// Background: cube=devops became a feedback loop because every chat response
// and every raw webhook alert got persisted as "knowledge", then hydrated on
// the next query. The result was 46/47 entries that were either fabricated
// narratives, chat exports, or stale triage snapshots. See the 2026-04-11
// incident writeup and roadmap Phase 5.7 for the loop break, and 5.6 for
// the earlier analyze/dmesg hardening.
//
// This validator is the final gatekeeper on memdb_save. It cannot catch every
// bad save, but it catches the high-frequency contamination shapes:
//
//   1. Empty / whitespace-only content.
//   2. Raw "user: …\nassistant: …" chat transcripts (the old auto-save format).
//   3. Numeric vital claims (swap X%, load X, disk X%, etc.) with no tool
//      output citation — this is exactly how "swap 99%" ended up in the cube
//      as a recurring pattern on a host with no swap file.
//   4. Incident-shaped content (Incident:/Symptom:/Root cause:) with an empty
//      or missing Evidence: field.

// ErrInvalidSavePayload is returned by validator when content is not fit for
// long-term persistence. The wrapping caller (memdb_save tool or KBSearcher)
// surfaces the reason to the LLM so it can correct and retry.
var ErrInvalidSavePayload = errors.New("invalid memdb save payload")

// Precompiled patterns. Each pattern is intentionally loose — we want false
// positives over false negatives. It is better to force the agent to rewrite
// a legitimate save with an evidence quote than to silently accept a
// contaminating payload.
var (
	// Numeric vitals: swap/ram/memory/load/cpu/disk followed by a number and optional %.
	//
	// Loose order: `swap 99%`, `load 51.2`, `memory at 90%`, `cpu=85%`.
	numericVitalPattern = regexp.MustCompile(
		`(?i)\b(swap|ram|memory|load|cpu|disk|gpu|iowait)\b[^\n]{0,40}?\b\d+(?:\.\d+)?\s*%?`,
	)
	// Tool name citations that count as evidence. Any of these must appear
	// when a numeric vital is claimed.
	toolCitationPattern = regexp.MustCompile(
		`(?i)\b(free\s+-[hbm]|uptime|top\b|htop|ps\s+aux|ps\s+-ef|df\s+-h|iostat|vmstat|dmesg|journalctl|docker\s+stats|docker\s+ps|systemctl|nproc|/proc/|cat\s+/sys/)`,
	)
	// Raw dialog log: content starts with `user:` or `assistant:` on the first
	// non-blank line. These were auto-generated before Phase 5.7.
	dialogPrefixPattern = regexp.MustCompile(`^(?i)\s*(user|assistant)\s*:`)
	// Incident structure markers. Having any of these means the writer
	// intended a structured incident record.
	incidentMarkerPattern = regexp.MustCompile(
		`(?mi)^\s*(incident|symptom|root\s*cause|fix|resolution|prevention)\s*:`,
	)
	// Non-empty Evidence: field. Matches "Evidence:" followed by at least
	// one non-whitespace character somewhere on the same or the next line.
	// Uses `\r?\n` so content pasted from Windows terminals or webhook
	// bodies with CRLF line endings is also recognised correctly.
	evidenceFieldPattern = regexp.MustCompile(
		`(?mi)^\s*evidence\s*:[ \t]*\S|^\s*evidence\s*:\s*\r?\n[ \t]*\S`,
	)
)

// ValidateSavePayload is the public entry point. It returns a nil error when
// the content is fit for persistence, or an ErrInvalidSavePayload-wrapped
// error whose message tells the LLM exactly what to fix.
//
// Validation pipeline (first failure wins):
//  1. Empty / whitespace-only.
//  2. Raw "user:" / "assistant:" dialog log.
//  3. Numeric vital claim without tool citation.
//  4. Incident-shaped content without non-empty Evidence:.
//
// Anything else passes. A short freestanding fact like "Postgres default user
// is memos, not postgres" is accepted as-is — no numeric vital, no incident
// structure, so rules 3 and 4 do not apply.
func ValidateSavePayload(content string) error {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return wrapReason("content is empty or whitespace-only")
	}

	if dialogPrefixPattern.MatchString(trimmed) {
		return wrapReason(
			"raw dialog log rejected. Use the structured incident format:\n" +
				"  Incident: <service> <symptom>\n" +
				"  Evidence: <quoted tool output that proved the symptom>\n" +
				"  Root cause: <why>\n" +
				"  Fix: <exact commands/actions>\n" +
				"  Prevention: <how to avoid recurrence>\n" +
				"Do not persist raw `user:` / `assistant:` transcripts — they become " +
				"self-reinforcing context on the next query")
	}

	if hasNumericVital(trimmed) && !hasToolCitation(trimmed) {
		return wrapReason(
			"numeric vital claim (swap/ram/memory/load/cpu/disk/gpu/iowait with a percent " +
				"or number) requires a tool output citation. Quote the command that " +
				"produced the number — `free -h`, `uptime`, `top`, `df -h`, `iostat`, " +
				"`vmstat`, `dmesg`, `docker stats`, etc. Without a citation this save " +
				"looks like `swap 99%` on a host with no swap file (real incident 2026-04-11).")
	}

	if isIncidentStructured(trimmed) && !hasNonEmptyEvidence(trimmed) {
		return wrapReason(
			"Incident-shaped content (Incident/Symptom/Root cause/Fix/Resolution/" +
				"Prevention headers) detected but the `Evidence:` field is missing or " +
				"empty. Add an `Evidence:` section with the tool output that demonstrated " +
				"the incident. If there was nothing to quote, the save is not an incident " +
				"resolution — do not use memdb_save for speculative narratives.")
	}

	return nil
}

// hasNumericVital returns true when the content mentions a vital signal with
// a concrete number attached. Used as the trigger for Rule 3.
func hasNumericVital(content string) bool {
	return numericVitalPattern.MatchString(content)
}

// hasToolCitation returns true when the content references at least one
// tool that could have produced a vital metric.
func hasToolCitation(content string) bool {
	return toolCitationPattern.MatchString(content)
}

// isIncidentStructured returns true when the content has any of the
// structured-incident markers. Rule 4 only applies when this is true.
func isIncidentStructured(content string) bool {
	return incidentMarkerPattern.MatchString(content)
}

// hasNonEmptyEvidence returns true when an Evidence: header is present AND
// is followed by at least one non-whitespace character on the same line or
// on the following line.
func hasNonEmptyEvidence(content string) bool {
	return evidenceFieldPattern.MatchString(content)
}

// wrapReason builds the error value returned for each rejection. The reason
// text is deliberately agent-facing: it tells the LLM what to change instead
// of just saying "rejected".
func wrapReason(reason string) error {
	return &saveValidationError{reason: reason}
}

// saveValidationError wraps ErrInvalidSavePayload with a human-readable
// remediation hint. Callers unwrap to ErrInvalidSavePayload via errors.Is.
type saveValidationError struct {
	reason string
}

func (e *saveValidationError) Error() string {
	return "invalid memdb save payload: " + e.reason
}

func (e *saveValidationError) Unwrap() error {
	return ErrInvalidSavePayload
}
