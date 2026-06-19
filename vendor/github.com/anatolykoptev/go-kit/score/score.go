// Package score provides confidence-bucket helpers for ranking outputs.
//
// Usage pattern: a ranker (e.g. rerank.LinearMinMax, a custom heuristic, or
// any (count × intensity) fusion) produces a continuous score; callers map
// it to a discrete label for human display, log filtering, or alert routing.
//
// Generic bucket APIs in this package make that mapping explicit, parametric,
// and testable across services. No external dependencies.
package score

import "fmt"

// ConfidenceLevel is the canonical 3-bucket label for ranking confidence.
type ConfidenceLevel string

// Predefined labels. Callers are free to define their own labels via Bucket
// for non-3-way classifications (e.g. severity 5-tier, risk 4-tier).
const (
	ConfidenceLow    ConfidenceLevel = "low"
	ConfidenceMedium ConfidenceLevel = "medium"
	ConfidenceHigh   ConfidenceLevel = "high"
)

// Default thresholds for ConfidenceFromScore.
//
// These are tuned for scores in roughly [0, 2] range — the output of
// rerank.LinearMinMax with two equal-weighted ScoredIDList signals.
// For other scoring schemes (RRF, cosine in [0, 1], LLM logits) prefer
// ConfidenceFromScoreThresholds with explicit cutoffs.
const (
	DefaultLowMax    = 0.2
	DefaultMediumMax = 0.7
)

// ConfidenceFromScore maps a continuous score to a 3-bucket confidence label
// using the package defaults (DefaultLowMax = 0.2, DefaultMediumMax = 0.7):
//
//	score < 0.2        → ConfidenceLow
//	0.2 ≤ score < 0.7  → ConfidenceMedium
//	score ≥ 0.7        → ConfidenceHigh
//
// Negative scores collapse to ConfidenceLow. Scores beyond DefaultMediumMax
// (including unbounded large) saturate at ConfidenceHigh.
func ConfidenceFromScore(s float64) ConfidenceLevel {
	return ConfidenceFromScoreThresholds(s, DefaultLowMax, DefaultMediumMax)
}

// ConfidenceFromScoreThresholds maps a continuous score to a 3-bucket label
// using caller-supplied cutoffs. lowMax is the upper edge of "low" (exclusive);
// mediumMax is the upper edge of "medium" (exclusive). Anything ≥ mediumMax
// is "high". Anything < lowMax is "low".
//
// Panics if lowMax > mediumMax (caller programmer error — flipped thresholds).
func ConfidenceFromScoreThresholds(s, lowMax, mediumMax float64) ConfidenceLevel {
	if lowMax > mediumMax {
		panic(fmt.Sprintf("score: lowMax (%v) must be ≤ mediumMax (%v)", lowMax, mediumMax))
	}
	switch {
	case s < lowMax:
		return ConfidenceLow
	case s < mediumMax:
		return ConfidenceMedium
	default:
		return ConfidenceHigh
	}
}

// Bucket maps score to one of len(labels) buckets defined by upper-edge
// thresholds. Generalises ConfidenceFromScoreThresholds to N-way labelling.
//
// thresholds defines exclusive upper edges: thresholds[i] is the upper edge
// (exclusive) of bucket i. The last bucket has no upper edge (saturates).
// len(labels) must equal len(thresholds) + 1.
//
// Example — severity 4-tier:
//
//	thresholds := []float64{0.25, 0.5, 0.75}
//	labels := []string{"info", "warning", "error", "critical"}
//	Bucket(0.6, thresholds, labels)  // → "error"
//
// Thresholds must be sorted ascending. Panics on:
//   - len(labels) != len(thresholds) + 1
//   - thresholds not sorted ascending
//   - any of len(labels) == 0 / len(thresholds) == 0 inputs (an empty
//     bucket schema is a programmer error, not a runtime concern)
//
// Negative-infinity scores collapse to labels[0]; +∞ saturates to
// labels[len(labels)-1].
func Bucket(s float64, thresholds []float64, labels []string) string {
	if len(labels) == 0 {
		panic("score: Bucket requires at least one label")
	}
	if len(thresholds)+1 != len(labels) {
		panic(fmt.Sprintf("score: Bucket requires len(labels)=%d == len(thresholds)+1=%d",
			len(labels), len(thresholds)+1))
	}
	for i := 1; i < len(thresholds); i++ {
		if thresholds[i] < thresholds[i-1] {
			panic(fmt.Sprintf("score: Bucket thresholds must be sorted ascending; got [%v < %v] at index %d",
				thresholds[i-1], thresholds[i], i))
		}
	}
	for i, t := range thresholds {
		if s < t {
			return labels[i]
		}
	}
	return labels[len(labels)-1]
}
