package tools

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/anatolykoptev/dozor/internal/deploy"
	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	dto "github.com/prometheus/client_model/go"
)

// DeployCheckInput selects which service to summarise. Match is by service
// name (the value under `services:` in deploy-repos.yaml), not repo path.
type DeployCheckInput struct {
	Service string `json:"service" jsonschema:"Service name as configured in deploy-repos.yaml (e.g. oxpulse-chat, go-job)"`
}

// registerDeployCheck wires server_deploy_check.
//
// Aggregator over: deploy-repos.yaml config, `git rev-parse HEAD` in
// SourcePath, smoke_url probe, `docker ps`, and in-process Prometheus
// counters (BuildResultTotal etc). Read-only — never mutates state.
func registerDeployCheck(server *mcp.Server, _ *engine.ServerAgent) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "server_deploy_check",
		Description: `Check the current deploy state of a webhook-driven service.

Aggregates: source git HEAD, smoke_url probe, container status, build counters
(success / failure / timeout / debounced / superseded / deduplicated).

Use to answer "did my last merge deploy?" without grepping /metrics by hand.
Input: { "service": "oxpulse-chat" }`,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input DeployCheckInput) (*mcp.CallToolResult, engine.TextOutput, error) {
		text, err := handleDeployCheck(ctx, input)
		return nil, engine.TextOutput{Text: text}, err
	})
}

func handleDeployCheck(ctx context.Context, input DeployCheckInput) (string, error) {
	if strings.TrimSpace(input.Service) == "" {
		return "", fmt.Errorf("service is required")
	}

	cfg, err := deploy.LoadConfig(deploy.DefaultConfigPath())
	if err != nil {
		return "", fmt.Errorf("load deploy config: %w", err)
	}

	repo, rc, ok := findRepoByService(cfg, input.Service)
	if !ok {
		return "", fmt.Errorf("service %q not found in deploy-repos.yaml", input.Service)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Service: %s\nRepo:    %s\nSource:  %s\nKind:    %s\n",
		input.Service, repo, rc.SourcePath, effectiveKind(rc))

	// 1. git HEAD
	sha, subject := readGitHead(ctx, rc.SourcePath)
	if sha != "" {
		fmt.Fprintf(&b, "HEAD:    %s %q\n", short(sha), subject)
	} else {
		fmt.Fprintf(&b, "HEAD:    (unreadable — %s)\n", subject)
	}

	// 2. smoke probe
	if rc.SmokeURL != "" {
		status, snippet := probeSmoke(ctx, rc.SmokeURL)
		fmt.Fprintf(&b, "Smoke:   %s → %s", rc.SmokeURL, status)
		if snippet != "" {
			fmt.Fprintf(&b, " (%s)", snippet)
		}
		b.WriteString("\n")
	}

	// 3. container status (compose services only — binary/static have no docker)
	if effectiveKind(rc) == "compose" {
		for _, svc := range rc.Services {
			fmt.Fprintf(&b, "Docker:  %s\n", dockerStatus(ctx, svc))
		}
	}

	// 4. counters
	fmt.Fprintf(&b, "\nCounters since dozor start (%s):\n", repo)
	writeCounters(&b, repo, rc.Services)

	return b.String(), nil
}

// findRepoByService scans the loaded config for any repo whose Services list
// includes the requested name. Returns the full "owner/repo" key and config.
func findRepoByService(cfg *deploy.Config, service string) (string, deploy.RepoConfig, bool) {
	// Deterministic order so two calls return the same hit if multiple match.
	keys := make([]string, 0, len(cfg.Repos))
	for k := range cfg.Repos {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		rc := cfg.Repos[k]
		for _, s := range rc.Services {
			if s == service {
				return k, rc, true
			}
		}
		for _, s := range rc.UserServices {
			if s == service {
				return k, rc, true
			}
		}
	}
	return "", deploy.RepoConfig{}, false
}

func effectiveKind(rc deploy.RepoConfig) string {
	if rc.Kind == "" {
		return "compose"
	}
	return string(rc.Kind)
}

func readGitHead(ctx context.Context, sourcePath string) (sha, subject string) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	shaOut, err := exec.CommandContext(ctx, "git", "-C", sourcePath, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", err.Error()
	}
	sha = strings.TrimSpace(string(shaOut))

	subjOut, err := exec.CommandContext(ctx, "git", "-C", sourcePath, "log", "-1", "--format=%s").Output()
	if err != nil {
		return sha, "(no subject)"
	}
	return sha, strings.TrimSpace(string(subjOut))
}

func probeSmoke(ctx context.Context, url string) (status, snippet string) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "BAD URL", err.Error()
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "UNREACHABLE", err.Error()
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
	bodyStr := strings.TrimSpace(string(body))
	if len(bodyStr) > 80 {
		bodyStr = bodyStr[:80] + "…"
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return fmt.Sprintf("HTTP %d ✓", resp.StatusCode), bodyStr
	}
	return fmt.Sprintf("HTTP %d ✗", resp.StatusCode), bodyStr
}

func dockerStatus(ctx context.Context, container string) string {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "docker", "ps", "--all",
		"--filter", "name=^"+container+"$",
		"--format", "{{.Names}} {{.Status}}").Output()
	if err != nil {
		return container + " (docker ps failed: " + err.Error() + ")"
	}
	line := strings.TrimSpace(string(out))
	if line == "" {
		return container + " (not present)"
	}
	return line
}

// writeCounters formats per-service Prometheus counter snapshots. Reads
// values directly from the in-process registry — same numbers /metrics
// would emit, no HTTP roundtrip.
func writeCounters(b *strings.Builder, repo string, services []string) {
	read := func(c interface{ Write(*dto.Metric) error }) float64 {
		var m dto.Metric
		if err := c.Write(&m); err != nil {
			return 0
		}
		if m.Counter != nil {
			return m.Counter.GetValue()
		}
		return 0
	}

	for _, svc := range services {
		fmt.Fprintf(b, "  %s:\n", svc)
		fmt.Fprintf(b, "    fired:        %.0f\n", read(deploy.FiredTotal.WithLabelValues(repo, svc)))
		fmt.Fprintf(b, "    debounced:    %.0f\n", read(deploy.DebouncedTotal.WithLabelValues(repo, svc)))
		fmt.Fprintf(b, "    superseded:   %.0f\n", read(deploy.SupersededTotal.WithLabelValues(repo, svc)))
		fmt.Fprintf(b, "    deduplicated: %.0f\n", read(deploy.DeduplicatedTotal.WithLabelValues(repo, svc)))
		fmt.Fprintf(b, "    build success: %.0f\n", read(deploy.BuildResultTotal.WithLabelValues(repo, svc, "success")))
		fmt.Fprintf(b, "    build failure: %.0f\n", read(deploy.BuildResultTotal.WithLabelValues(repo, svc, "failure")))
		fmt.Fprintf(b, "    build timeout: %.0f\n", read(deploy.BuildResultTotal.WithLabelValues(repo, svc, "timeout")))
		for _, reason := range []string{"no_relevant_paths", "explicit_skip"} {
			v := read(deploy.SkippedTotal.WithLabelValues(repo, svc, reason))
			if v > 0 {
				fmt.Fprintf(b, "    skipped (%s): %.0f\n", reason, v)
			}
		}
	}
}

func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}
