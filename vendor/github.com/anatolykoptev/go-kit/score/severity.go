package score

import (
	"fmt"
	"strings"
)

// Severity is the canonical 5-tier label for vulnerability / alert / health
// classifications. Maps cleanly onto Nuclei, CVSS, OSV.dev, and most
// alerting/security pipelines.
type Severity string

// Predefined severity tiers, ordered low → critical.
//
// These values are lowercase strings to match Nuclei output and the
// Prometheus / Grafana alerting convention. Callers serialising to JSON
// can rely on the exact constant value (e.g. SeverityHigh marshals as
// `"high"`).
const (
	SeverityInfo     Severity = "info"
	SeverityLow      Severity = "low"
	SeverityMedium   Severity = "medium"
	SeverityHigh     Severity = "high"
	SeverityCritical Severity = "critical"
)

// SeverityRank returns a numeric rank suitable for sorting and threshold
// comparisons:
//
//	info     → 0
//	low      → 1
//	medium   → 2
//	high     → 3
//	critical → 4
//
// Unknown values return -1 — callers must treat negative ranks as parse
// errors and not as ordered values.
func SeverityRank(s Severity) int {
	switch s {
	case SeverityInfo:
		return 0
	case SeverityLow:
		return 1
	case SeverityMedium:
		return 2
	case SeverityHigh:
		return 3
	case SeverityCritical:
		return 4
	default:
		return -1
	}
}

// ParseSeverity normalises a string to a canonical Severity value.
// Case-insensitive; trims whitespace. Returns SeverityInfo and ok=false
// for unrecognised input — callers can default to Info on parse failure
// or surface the failure as needed.
//
// Recognised aliases: "informational" → Info, "med" → Medium,
// "crit" → Critical, "warn" / "warning" → Medium (operational alerting
// convention where warnings are mid-severity), "error" → High.
func ParseSeverity(s string) (Severity, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "info", "informational":
		return SeverityInfo, true
	case "low":
		return SeverityLow, true
	case "medium", "med", "warning", "warn":
		return SeverityMedium, true
	case "high", "error":
		return SeverityHigh, true
	case "critical", "crit":
		return SeverityCritical, true
	default:
		return SeverityInfo, false
	}
}

// SeverityFromScore maps a continuous score to a 5-tier Severity label.
//
// Default thresholds (tuned for CVSS-style [0, 10] inputs after divide-by-10
// normalisation, and for cosine-style [0, 1] outputs):
//
//	score < 0.1 → info
//	score < 0.4 → low
//	score < 0.7 → medium
//	score < 0.9 → high
//	score ≥ 0.9 → critical
//
// For non-default scoring schemes, use Bucket directly with custom thresholds.
func SeverityFromScore(s float64) Severity {
	switch {
	case s < 0.1:
		return SeverityInfo
	case s < 0.4:
		return SeverityLow
	case s < 0.7:
		return SeverityMedium
	case s < 0.9:
		return SeverityHigh
	default:
		return SeverityCritical
	}
}

// SeverityAtLeast reports whether s meets or exceeds the threshold tier.
// Useful in alerting filters: `if score.SeverityAtLeast(finding.Severity, score.SeverityHigh) { ... }`.
//
// Unknown severities (rank -1) never meet the threshold and produce false.
func SeverityAtLeast(s, threshold Severity) bool {
	rs := SeverityRank(s)
	rt := SeverityRank(threshold)
	if rs < 0 || rt < 0 {
		return false
	}
	return rs >= rt
}

// String returns the Severity as a human-readable label.
// Distinct from the JSON marshalling — included for fmt.Stringer compliance.
func (s Severity) String() string {
	if s == "" {
		return fmt.Sprintf("severity(%q)", string(s))
	}
	return string(s)
}
