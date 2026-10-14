package deploy

import (
	"encoding/json"
	"log/slog"
	"os"
	"time"
)

// Last-build-outcome record, read by server_deploy_check so an operator can
// tell "my merge deployed", "it failed" and "it was replaced by a newer
// commit" apart from the live source HEAD (which says nothing about what was
// built). Stored in the existing deployed-sha.json receipt under "outcomes"
// (keyed by service name), so it survives a dozor restart whenever
// ConfigureDeployedSHAPersistence is configured; otherwise it is in-memory
// only (see BuildOutcomesDurable).

// Outcome statuses. success/failure/timeout mirror dozor_build_result_total.
const (
	OutcomeSuperseded  = "superseded"   // pending build replaced by a newer commit before it ran
	OutcomeManualGated = "manual_gated" // built, but deploy_on=manual withheld the deploy
	maxOutcomeErrLen   = 200
)

// BuildOutcome is the most recent terminal state of a build for a service.
type BuildOutcome struct {
	Repo       string    `json:"repo"`
	Services   []string  `json:"services,omitempty"`
	CommitSHA  string    `json:"commit"`
	Status     string    `json:"status"` // success|failure|timeout|superseded|manual_gated
	FinishedAt time.Time `json:"finished_at"`
	Duration   string    `json:"duration,omitempty"`
	Error      string    `json:"error,omitempty"`
}

// buildOutcomes is the in-memory mirror, service name → last outcome.
// Guarded by pendingDeployMu (same lock as the deployed-SHA receipt).
var buildOutcomes = map[string]BuildOutcome{}

func truncateOutcomeError(e string) string {
	if len(e) > maxOutcomeErrLen {
		return e[:maxOutcomeErrLen] + "…"
	}
	return e
}

// recordBuildOutcome stores o for every service it covers and, when the
// receipt file is configured, persists it. Best-effort: a write failure logs.
func recordBuildOutcome(o BuildOutcome) {
	if len(o.Services) == 0 {
		return
	}
	pendingDeployMu.Lock()
	defer pendingDeployMu.Unlock()
	for _, svc := range o.Services {
		buildOutcomes[svc] = o
	}
	if deployedSHAPersistPath == "" {
		return
	}
	doc := deployedSHAFile{}
	if data, err := os.ReadFile(deployedSHAPersistPath); err == nil { //nolint:gosec // trusted workspace path
		_ = json.Unmarshal(data, &doc)
	}
	if doc.Outcomes == nil {
		doc.Outcomes = map[string]BuildOutcome{}
	}
	for _, svc := range o.Services {
		doc.Outcomes[svc] = o
	}
	if err := writeJSONAtomic(deployedSHAPersistPath, doc); err != nil {
		slog.Warn("deploy: failed to persist build outcome",
			"path", deployedSHAPersistPath, "error", err)
	}
}

// LastBuildOutcome returns the most recent recorded outcome for service.
func LastBuildOutcome(service string) (BuildOutcome, bool) {
	pendingDeployMu.Lock()
	defer pendingDeployMu.Unlock()
	o, ok := buildOutcomes[service]
	return o, ok
}

// BuildOutcomesDurable reports whether outcomes are persisted across a dozor
// restart (receipt file configured) or only held since process start.
func BuildOutcomesDurable() bool {
	pendingDeployMu.Lock()
	defer pendingDeployMu.Unlock()
	return deployedSHAPersistPath != ""
}

// DeployedSHA returns the last SHA dozor successfully built and brought up for
// repo (the deploy receipt), or "" when none is recorded.
func DeployedSHA(repo string) string { return lookupDeployedSHA(repo) }
