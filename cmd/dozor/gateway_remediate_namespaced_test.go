package main

import (
	"context"
	"testing"

	"github.com/anatolykoptev/dozor/internal/engine"
)

// TestRouteIssue_NamespacedCritical_NotRemediated asserts that a CRITICAL
// remote/LLM alert (a first-class TriageIssue since the unified-alert change)
// is classified report-only and NEVER enters the restart arm.
//
// The "RestartService is NOT called" guarantee is proven structurally: a nil
// *engine.ServerAgent is passed as the agent. If routeIssue reached the restart
// arm it would dereference nil inside eng.RestartService and panic; if it
// reached the disk arm it would deref nil in handleDiskIssue. The namespaced
// guard must short-circuit BEFORE either, so neither nil deref happens and the
// issue lands in unhandled. (Before this guard, "remote:https://x" CRITICAL fell
// into eng.RestartService → a pointless local `docker compose restart` + a false
// "restart failed" log.)
func TestRouteIssue_NamespacedCritical_NotRemediated(t *testing.T) {
	t.Parallel()

	cases := []engine.TriageIssue{
		{Service: "remote:https://x.example", Level: engine.AlertCritical, Description: "remote:https://x.example: Site unreachable"},
		{Service: "remote:nginx", Level: engine.AlertCritical, Description: "remote:nginx: Remote service nginx is failed"},
		{Service: "llm:LLM proxy gemini-3.1: rate limited (HTTP 429)", Level: engine.AlertCritical, Description: "llm: rate limited"},
	}

	for _, issue := range cases {
		// nil agent: any path that touches the agent would panic. The guard must not.
		restarted, suppressed, diskMsgs, unhandled := routeIssue(context.Background(), nil, engine.Config{}, issue)

		if len(restarted) != 0 {
			t.Errorf("issue %q: namespaced critical must not restart, got restarted=%v", issue.Service, restarted)
		}
		if len(suppressed) != 0 {
			t.Errorf("issue %q: namespaced critical must not be suppressed, got suppressed=%v", issue.Service, suppressed)
		}
		if len(diskMsgs) != 0 {
			t.Errorf("issue %q: namespaced critical must not hit disk arm, got diskMsgs=%v", issue.Service, diskMsgs)
		}
		if len(unhandled) != 1 || unhandled[0].Service != issue.Service {
			t.Errorf("issue %q: namespaced critical must land in unhandled (report-only), got unhandled=%+v", issue.Service, unhandled)
		}
	}
}

// TestRouteIssue_LocalCritical_StillRoutesToRestart is the negative control: a
// bare (non-namespaced) local service at CRITICAL must NOT be diverted by the
// namespaced guard — it still reaches the restart arm. Proven by the nil agent
// panicking: reaching eng.RestartService on a nil agent panics, which recover()
// catches. (We deliberately do not stand up a real ServerAgent here — the point
// is only that the guard does NOT swallow local services.)
func TestRouteIssue_LocalCritical_StillRoutesToRestart(t *testing.T) {
	t.Parallel()

	defer func() {
		if r := recover(); r == nil {
			t.Error("local CRITICAL issue must reach the restart arm (eng.RestartService on nil agent should panic); the namespaced guard wrongly swallowed it")
		}
	}()

	issue := engine.TriageIssue{Service: "ox-whisper", Level: engine.AlertCritical, Description: "ox-whisper: exited"}
	_, _, _, _ = routeIssue(context.Background(), nil, engine.Config{}, issue)
}

// TestTryAutoRemediate_NamespacedCriticalUnhandled asserts the same guarantee at
// the tryAutoRemediate level: a report containing only a namespaced-critical
// alert is NOT fully handled (returns false → falls through to the LLM path),
// and no restart is attempted (nil agent would panic in the restart/verify path).
func TestTryAutoRemediate_NamespacedCriticalUnhandled(t *testing.T) {
	t.Parallel()

	report := engine.AlertIssueLine(engine.Alert{
		Level:       engine.AlertCritical,
		Service:     "https://x.example",
		Title:       "Site unreachable",
		Description: "no response",
	})

	// Sanity: the report must contain a single namespaced-critical issue.
	issues := engine.ExtractIssues(report)
	if len(issues) != 1 || !engine.IsNamespacedService(issues[0].Service) || issues[0].Level != engine.AlertCritical {
		t.Fatalf("test setup: expected one namespaced critical issue, got %+v", issues)
	}

	// nil agent + nil notify: if remediation tried to restart/verify, it would panic.
	handled := tryAutoRemediate(context.Background(), nil, engine.Config{}, report, nil)
	if handled {
		t.Error("tryAutoRemediate must return false for a namespaced-critical-only report (report-only, falls through to LLM)")
	}
}
