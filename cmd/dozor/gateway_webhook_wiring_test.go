package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/prometheus/client_golang/prometheus"
)

// gatherPendingDeployLinesCmd renders the exported text-exposition lines for
// the dozor_pending_deploy gauge from the default registry. Mirrors
// gatherDozorPendingDeployLines in internal/deploy/manual_gate_test.go but
// lives in package main (cmd/dozor) so it can assert on the wiring in
// registerDeployWebhook.
func gatherPendingDeployLinesCmd(t *testing.T) []string {
	t.Helper()
	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("prometheus gather: %v", err)
	}
	var lines []string
	for _, mf := range mfs {
		if mf.GetName() != "dozor_pending_deploy" {
			continue
		}
		for _, m := range mf.GetMetric() {
			if m.GetGauge() == nil {
				continue
			}
			var labels []string
			for _, lp := range m.GetLabel() {
				labels = append(labels, fmt.Sprintf(`%s=%q`, lp.GetName(), lp.GetValue()))
			}
			sort.Strings(labels)
			lines = append(lines, fmt.Sprintf("dozor_pending_deploy{%s} %g",
				strings.Join(labels, ","), m.GetGauge().GetValue()))
		}
	}
	return lines
}

func findPendingLineCmd(t *testing.T, repo, service string) string {
	t.Helper()
	want := fmt.Sprintf(`dozor_pending_deploy{repo=%q,service=%q}`, repo, service)
	for _, l := range gatherPendingDeployLinesCmd(t) {
		if strings.HasPrefix(l, want) {
			return l
		}
	}
	return ""
}

// TestRegisterDeployWebhook_PreinitialisesPendingGauge — the wiring test
// demanded by issue #188: removing deploy.PreinitPendingDeployGauge(cfg)
// from registerDeployWebhook leaves the whole suite green. This test calls
// registerDeployWebhook with a temp workspace containing a manual repo and
// asserts the gauge series exists at 0 for that repo's service.
//
// RED-on-revert: remove the deploy.PreinitPendingDeployGauge(cfg) call from
// registerDeployWebhook — the series is never created, findPendingLineCmd
// returns "", and the anchored-0 assertion fails.
func TestRegisterDeployWebhook_PreinitialisesPendingGauge(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DOZOR_WORKSPACE", dir)
	// No token → drift checker skips GitHub API calls (safe in a test).
	t.Setenv("DOZOR_GITHUB_TOKEN", "")

	const repo = "test/wiring-manual"
	const svc = "wiring-svc"
	configYAML := "repos:\n  " + repo + ":\n    deploy_on: manual\n    compose_path: /tmp\n    source_path: /tmp\n    services:\n      - " + svc + "\n"
	if err := os.WriteFile(filepath.Join(dir, "deploy-repos.yaml"), []byte(configYAML), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mx := http.NewServeMux()
	registerDeployWebhook(ctx, mx, func(string) {}, func([]engine.Alert, string) {})

	line := findPendingLineCmd(t, repo, svc)
	wantRe := `^dozor_pending_deploy\{repo="test/wiring-manual",service="wiring-svc"\} 0$`
	if !regexp.MustCompile(wantRe).MatchString(line) {
		t.Fatalf("preinit line = %q, want anchored 0 (PreinitPendingDeployGauge wiring removed?)", line)
	}
}
