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
	"github.com/prometheus/client_golang/prometheus"
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

Aggregates: source git HEAD, smoke_url probe, the deploy pipeline (a debounced
deploy still waiting to fire, the in-flight build, the last build outcome and
deployed commit), container status (flagging an image newer than dozor's last
deploy = deployed by hand), and build counters (success / failure / timeout /
debounced / superseded / deduplicated).

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
	return assembleDeployCheck(ctx, input, cfg)
}

// assembleDeployCheck builds the full tool output for a loaded config. Split
// from handleDeployCheck so tests drive the exact assembly production uses.
func assembleDeployCheck(ctx context.Context, input DeployCheckInput, cfg *deploy.Config) (string, error) {
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
		fmt.Fprintf(&b, "HEAD:    %s %q (source checkout — not necessarily what is running; see Last deploy)\n", deploy.ShortSHA(sha), subject)
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

	// 3. deploy pipeline: debounced-but-unfired, queued/in-flight, last outcome.
	// Written before container status so "a deploy is on its way" is not buried.
	pending, pendingOK := deploy.PendingDebounces()
	var queueSnap []deploy.ServiceQueueState
	queueOK := false
	if q := deploy.ActiveQueue(); q != nil {
		queueSnap, queueOK = q.Snapshot(), true
	}
	lastSuccess := writeDeployPipeline(&b, time.Now(), repo, rc.Services, pending, pendingOK, queueSnap, queueOK)

	// 4. container status (compose services only — binary/static have no docker)
	if effectiveKind(rc) == "compose" {
		for _, svc := range rc.Services {
			fmt.Fprintf(&b, "Docker:  %s\n", dockerStatus(ctx, svc))
			if created, err := containerImageCreated(ctx, svc); err == nil && !lastSuccess.IsZero() &&
				imageNewerThanDeploy(created, lastSuccess) {
				fmt.Fprintf(&b, "WARNING: %s runs an image created %s, after dozor's last successful deploy (%s) — likely deployed by hand\n",
					svc, created.UTC().Format(time.RFC3339), lastSuccess.UTC().Format(time.RFC3339))
			}
		}
	}

	// 5. counters
	fmt.Fprintf(&b, "\nCounters since dozor start (%s):\n", repo)
	writeCounters(&b, repo, rc.Services)

	return b.String(), nil
}

// writeDeployPipeline renders the three stages a webhook passes through, in
// order: debounce window (Pending), build queue (Queue), and the recorded
// outcome (Last deploy). Showing all of them is the point — any single one
// reads "idle" while a deploy is on its way. A nil queue (early init, before
// NewQueue ran) is made visible rather than silently skipped. Returns the
// finish time of the last successful deploy (zero if unknown).
func writeDeployPipeline(b *strings.Builder, now time.Time, repo string, services []string,
	pending []deploy.PendingDebounce, pendingOK bool, queueSnap []deploy.ServiceQueueState, queueOK bool,
) time.Time {
	writePendingDebounce(b, now, services, pending, pendingOK)
	if queueOK {
		renderQueueState(b, now, services, queueSnap)
	} else {
		b.WriteString("Queue:   (not yet started)\n")
	}
	return writeLastDeploy(b, repo, services, now)
}

// renderQueueState is the pure half of writeQueueState.
func renderQueueState(b *strings.Builder, now time.Time, services []string, snap []deploy.ServiceQueueState) {
	want := make(map[string]struct{}, len(services))
	for _, s := range services {
		want[s] = struct{}{}
	}
	for _, state := range snap {
		// Match if any of the snapshot's service names is in our request.
		hit := false
		for _, s := range state.Services {
			if _, ok := want[s]; ok {
				hit = true
				break
			}
		}
		if !hit {
			continue
		}
		fmt.Fprintf(b, "Queue:   ")
		if state.BuildingSHA != "" {
			fmt.Fprintf(b, "building %s", deploy.ShortSHA(state.BuildingSHA))
			if !state.BuildingSince.IsZero() {
				fmt.Fprintf(b, " (started %s, running %s)", fmtTS(state.BuildingSince), now.Sub(state.BuildingSince).Round(time.Second))
			}
		}
		if state.PendingSHA != "" {
			if state.BuildingSHA != "" {
				fmt.Fprintf(b, ", ")
			}
			fmt.Fprintf(b, "pending %s", deploy.ShortSHA(state.PendingSHA))
		}
		b.WriteString("\n")
		return
	}
	// Service known to config but absent from the snapshot — queue is idle for it.
	fmt.Fprintf(b, "Queue:   idle\n")
}

// shortOrDash abbreviates a SHA; a no-build manual deploy has none.
func shortOrDash(sha string) string {
	if sha == "" {
		return "-"
	}
	return deploy.ShortSHA(sha)
}

func fmtTS(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05Z") }

// writePendingDebounce renders debounced deploys that have not fired yet —
// the window between "webhook received" and "build queued" in which HEAD,
// Queue and the counters all still look idle. ok=false means the webhook
// handler (and so the debouncer) is not running in this process.
func writePendingDebounce(b *strings.Builder, now time.Time, services []string, pending []deploy.PendingDebounce, ok bool) {
	if !ok {
		b.WriteString("Pending: (debounce state unavailable — webhook handler not started)\n")
		return
	}
	want := make(map[string]struct{}, len(services))
	for _, s := range services {
		want[s] = struct{}{}
	}
	found := false
	for _, p := range pending {
		hit := false
		for _, s := range p.Services {
			if _, in := want[s]; in {
				hit = true
				break
			}
		}
		if !hit {
			continue
		}
		found = true
		fires := "firing now"
		if d := p.FiresAt.Sub(now); d > 0 {
			fires = fmt.Sprintf("fires %s (in %s)", fmtTS(p.FiresAt), d.Round(time.Second))
		}
		fmt.Fprintf(b, "Pending: %s debounced, NOT yet building — burst started %s, last webhook %s, %s, %d webhook(s)\n",
			deploy.ShortSHA(p.CommitSHA), fmtTS(p.FirstSeen), fmtTS(p.LastSeen), fires, p.HitCount)
	}
	if !found {
		b.WriteString("Pending: none (no debounced deploy waiting)\n")
	}
}

// writeLastDeploy renders the last recorded build outcome for the service
// group and the deploy receipt. Returns the finish time of the last
// successful deploy (zero if none known) for the hand-deploy check.
func writeLastDeploy(b *strings.Builder, repo string, services []string, now time.Time) time.Time {
	scope := "since dozor start"
	if deploy.BuildOutcomesDurable() {
		scope = "persisted across restarts"
	}
	shown := false
	for _, svc := range services {
		o, ok := deploy.LastBuildOutcome(svc)
		if !ok {
			continue
		}
		shown = true
		fmt.Fprintf(b, "Last deploy: %s %s finished %s (%s ago)", shortOrDash(o.CommitSHA), o.Status, fmtTS(o.FinishedAt),
			now.Sub(o.FinishedAt).Round(time.Second))
		if o.Duration != "" {
			fmt.Fprintf(b, ", took %s", o.Duration)
		}
		if len(services) > 1 {
			fmt.Fprintf(b, " [%s]", svc)
		}
		if o.Error != "" {
			fmt.Fprintf(b, " — %s", o.Error)
		}
		b.WriteString("\n")
	}
	// Last successful deploy, tracked apart from the latest outcome so a later
	// failure/timeout/superseded record neither hides it nor disables the
	// hand-deploy check.
	var lastSuccess time.Time
	for _, svc := range services {
		o, ok := deploy.LastBuildOutcome(svc)
		if ok && o.IsDeploySuccess() {
			continue // already shown above as the latest outcome
		}
		if s, ok := deploy.LastSuccessfulDeploy(svc); ok {
			fmt.Fprintf(b, "Last success: %s finished %s (%s ago) [%s]\n", shortOrDash(s.CommitSHA), fmtTS(s.FinishedAt),
				now.Sub(s.FinishedAt).Round(time.Second), s.Status)
		}
	}
	for _, svc := range services {
		if s, ok := deploy.LastSuccessfulDeploy(svc); ok && s.FinishedAt.After(lastSuccess) {
			lastSuccess = s.FinishedAt
		}
	}
	if !shown {
		fmt.Fprintf(b, "Last deploy: none recorded (%s)\n", scope)
	} else {
		fmt.Fprintf(b, "         (last-deploy history: %s)\n", scope)
	}
	if sha := deploy.DeployedSHA(repo); sha != "" {
		fmt.Fprintf(b, "Deployed (receipt): %s\n", deploy.ShortSHA(sha))
	}
	return lastSuccess
}

// imageNewerThanDeploy reports whether a container image was created after
// dozor's last successful deploy finished. dozor's own builds finish before
// they are recorded, so a strictly later image can only come from a deploy
// done outside dozor.
func imageNewerThanDeploy(imageCreated, lastSuccess time.Time) bool {
	return imageCreated.After(lastSuccess)
}

// containerImageCreated returns the creation time of the image a container is
// running (not of whatever its tag points at now).
func containerImageCreated(ctx context.Context, container string) (time.Time, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	idOut, err := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{.Image}}", container).Output()
	if err != nil {
		return time.Time{}, err
	}
	out, err := exec.CommandContext(ctx, "docker", "image", "inspect", "--format", "{{.Created}}",
		strings.TrimSpace(string(idOut))).Output()
	if err != nil {
		return time.Time{}, err
	}
	return time.Parse(time.RFC3339Nano, strings.TrimSpace(string(out)))
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
		// rc.Services is normalised by validateRepoConfig: binary repos copy
		// UserServices → Services at load time (deploy/config.go), so iterating
		// rc.Services alone is sufficient post-LoadConfig.
		for _, s := range rc.Services {
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
	// dockerd response can lag significantly under heavy host load
	// (deploy-queue bursts, OOM pressure). 10s gives headroom without
	// making the tool feel slow on an idle host — `docker ps` returns in
	// <100ms when uncontended.
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
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

// writeCounters formats per-service Prometheus counter snapshots.
//
// Reads via Gather() — NOT WithLabelValues — to keep this tool truly
// read-only. WithLabelValues / GetMetricWithLabelValues both create a
// zero-valued metric for any previously-unseen label combination, which
// would silently bloat /metrics output every time the operator inspected
// a service that hasn't fired a deploy yet.
func writeCounters(b *strings.Builder, repo string, services []string) {
	snap, err := snapshotCounters()
	if err != nil {
		fmt.Fprintf(b, "  (counter snapshot failed: %v)\n", err)
		return
	}
	for _, svc := range services {
		fmt.Fprintf(b, "  %s:\n", svc)
		fmt.Fprintf(b, "    fired:        %.0f\n", snap.get("dozor_deploy_fired_total", repo, svc, ""))
		fmt.Fprintf(b, "    debounced:    %.0f\n", snap.get("dozor_deploy_debounced_total", repo, svc, ""))
		fmt.Fprintf(b, "    superseded:   %.0f\n", snap.get("dozor_deploy_superseded_total", repo, svc, ""))
		fmt.Fprintf(b, "    deduplicated: %.0f\n", snap.get("dozor_deploy_deduplicated_total", repo, svc, ""))
		fmt.Fprintf(b, "    build success: %.0f\n", snap.get("dozor_build_result_total", repo, svc, "success"))
		fmt.Fprintf(b, "    build failure: %.0f\n", snap.get("dozor_build_result_total", repo, svc, "failure"))
		fmt.Fprintf(b, "    build timeout: %.0f\n", snap.get("dozor_build_result_total", repo, svc, "timeout"))
		for _, reason := range []string{"no_relevant_paths", "explicit_skip"} {
			v := snap.get("dozor_deploy_skipped_total", repo, svc, reason)
			if v > 0 {
				fmt.Fprintf(b, "    skipped (%s): %.0f\n", reason, v)
			}
		}
	}
}

// counterSnapshot is the result of one Gather() pass: maps metric name →
// list of (label set, value). Read-only; never created counters.
type counterSnapshot struct {
	byName map[string][]*dto.Metric
}

func snapshotCounters() (*counterSnapshot, error) {
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		return nil, err
	}
	s := &counterSnapshot{byName: make(map[string][]*dto.Metric, len(families))}
	for _, fam := range families {
		if fam.GetType() != dto.MetricType_COUNTER {
			continue
		}
		s.byName[fam.GetName()] = fam.GetMetric()
	}
	return s, nil
}

// get returns the value of a counter with the given (repo, service, qual)
// labels. `qual` matches whichever 3rd label the counter uses — `status` for
// BuildResultTotal, `reason` for SkippedTotal — handled separately to avoid
// silently conflating distinct label semantics. Pass qual="" for counters
// with only repo+service labels (FiredTotal, DebouncedTotal, etc.). Returns
// 0 if no matching series exists — and, crucially, does NOT create one.
func (s *counterSnapshot) get(name, repo, service, qual string) float64 {
	for _, m := range s.byName[name] {
		var gotRepo, gotService, gotStatus, gotReason string
		for _, l := range m.GetLabel() {
			switch l.GetName() {
			case "repo":
				gotRepo = l.GetValue()
			case "service":
				gotService = l.GetValue()
			case "status":
				gotStatus = l.GetValue()
			case "reason":
				gotReason = l.GetValue()
			}
		}
		// Either the metric's status OR reason label must equal qual. They
		// are mutually exclusive in dozor today (a counter has one or the
		// other, never both). qual="" matches series where both are empty.
		if gotRepo != repo || gotService != service {
			continue
		}
		if gotStatus == qual && gotReason == "" {
			return m.GetCounter().GetValue()
		}
		if gotReason == qual && gotStatus == "" {
			return m.GetCounter().GetValue()
		}
	}
	return 0
}
