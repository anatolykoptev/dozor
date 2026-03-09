//go:build integration

package main

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/anatolykoptev/dozor/internal/engine"
)

// TestAutoRemediateIntegration is a live test that:
// 1. Runs triage against real Docker
// 2. Checks if ox-whisper is detected as CRITICAL
// 3. Runs tryAutoRemediate to restart it
// 4. Verifies ox-whisper is running again
//
// Prerequisite: docker compose stop ox-whisper
// Run: go test -tags=integration -v -run TestAutoRemediateIntegration ./cmd/dozor/
func TestAutoRemediateIntegration(t *testing.T) {
	loadDotenv(os.Getenv("HOME") + "/src/dozor/.env")

	cfg := engine.Init()
	eng := engine.NewAgent(cfg)
	defer eng.Close()

	ctx := context.Background()

	// 1. Run triage
	result := eng.Triage(ctx, nil)
	t.Logf("Triage result (%d bytes):\n%s", len(result), result)

	// 2. Extract issues
	issues := engine.ExtractIssues(result)
	t.Logf("Extracted %d issues", len(issues))
	for _, issue := range issues {
		t.Logf("  Service=%s Level=%s Desc=%s",
			issue.Service,
			extractIssueLevel(result, issue.Service),
			issue.Description)
	}

	if len(issues) == 0 {
		t.Skip("no issues detected — is ox-whisper stopped?")
	}

	// 3. Check ox-whisper is CRITICAL
	ox-whisperLevel := extractIssueLevel(result, "ox-whisper")
	if ox-whisperLevel != "CRITICAL" {
		t.Skipf("ox-whisper level is %q, expected CRITICAL — is ox-whisper stopped?", ox-whisperLevel)
	}

	// 4. Run auto-remediation with a captured notification
	var notified string
	notify := func(text string) {
		notified = text
		t.Logf("Notification sent:\n%s", text)
	}

	handled := tryAutoRemediate(ctx, eng, cfg, result, notify)
	t.Logf("tryAutoRemediate returned: %v", handled)

	// 5. Verify ox-whisper is running
	time.Sleep(3 * time.Second)
	status := eng.GetServiceStatus(ctx, "ox-whisper")
	t.Logf("ox-whisper state after remediation: %s", status.State)

	if status.State != engine.StateRunning {
		t.Errorf("ox-whisper should be running after auto-remediation, got %s", status.State)
	}

	if !handled {
		t.Logf("tryAutoRemediate returned false — there may be other unhandled issues besides ox-whisper")
	}

	if notified == "" && handled {
		t.Error("expected notification to be sent")
	}

	fmt.Println("\nNotification content:")
	fmt.Println(notified)
}
