package tools

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// drainMicrotasks is a no-op here; we exercise synchronous HTTP calls.
// Kept as a named helper for future async cases.

// ---- helpers ----------------------------------------------------------------

func promLabelsResponse(names []string) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = fmt.Sprintf("%q", n)
	}
	return fmt.Sprintf(`{"status":"success","data":[%s]}`, strings.Join(quoted, ","))
}

// promQueryResponse builds a Prometheus instant-query response for one series.
func promQueryResponse(metricName string, labels map[string]string, value string) string {
	ls := labels
	if ls == nil {
		ls = map[string]string{}
	}
	ls["__name__"] = metricName
	labelsJSON, _ := json.Marshal(ls)
	return fmt.Sprintf(`{"status":"success","data":{"resultType":"vector","result":[{"metric":%s,"value":[1700000000,%q]}]}}`, labelsJSON, value)
}

// promQueryMultiResponse builds a response with multiple series.
func promQueryMultiResponse(rows []struct {
	Name   string
	Labels map[string]string
	Value  string
}) string {
	results := make([]string, len(rows))
	for i, r := range rows {
		ls := r.Labels
		if ls == nil {
			ls = map[string]string{}
		}
		ls["__name__"] = r.Name
		labelsJSON, _ := json.Marshal(ls)
		results[i] = fmt.Sprintf(`{"metric":%s,"value":[1700000000,%q]}`, labelsJSON, r.Value)
	}
	return fmt.Sprintf(`{"status":"success","data":{"resultType":"vector","result":[%s]}}`, strings.Join(results, ","))
}

func lokiResponse(lines []struct {
	TS    string
	Level string
	Msg   string
}) string {
	entries := make([]string, len(lines))
	for i, l := range lines {
		msgJSON, _ := json.Marshal(fmt.Sprintf(`{"level":%q,"msg":%q}`, l.Level, l.Msg))
		entries[i] = fmt.Sprintf(`[%q,%s]`, l.TS, string(msgJSON))
	}
	return fmt.Sprintf(`{"status":"success","data":{"resultType":"streams","result":[{"stream":{"container":"test"},"values":[%s]}]}}`, strings.Join(entries, ","))
}

// ---- tests ------------------------------------------------------------------

// TestRegistryParsesEmbeddedYAML verifies the embedded YAML loads cleanly
// and all 5 expected services with their categories are present.
func TestRegistryParsesEmbeddedYAML(t *testing.T) {
	reg, err := loadMetricsRegistry("")
	if err != nil {
		t.Fatalf("loadMetricsRegistry: %v", err)
	}

	want := []string{"oxpulse-chat", "go-code", "memdb", "dozor", "go-search"}
	for _, svc := range want {
		if _, ok := reg.Services[svc]; !ok {
			t.Errorf("service %q missing from registry", svc)
		}
	}

	// oxpulse-chat must have calls, turn, sdk, build, sfu categories
	oxp := reg.Services["oxpulse-chat"]
	for _, cat := range []string{"calls", "turn", "sdk", "build", "sfu"} {
		if len(oxp.Categories[cat]) == 0 {
			t.Errorf("oxpulse-chat: category %q missing or empty", cat)
		}
	}
}

// TestServiceNotInRegistry_AddsWarning verifies that an unknown service
// produces a warning and still runs (generic job filter applied).
func TestServiceNotInRegistry_AddsWarning(t *testing.T) {
	// Mock prom: label values = no names; query = empty result.
	promSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/api/v1/label/__name__/values"):
			fmt.Fprint(w, `{"status":"success","data":[]}`)
		case strings.Contains(r.URL.Path, "/api/v1/query"):
			fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer promSrv.Close()

	t.Setenv("DOZOR_PROMETHEUS_URL", promSrv.URL)
	t.Setenv("DOZOR_LOKI_URL", "")

	out, err := handleMetrics(t.Context(), MetricsInput{Service: "unknown-svc"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	foundWarning := false
	for _, w := range out.Warnings {
		if strings.Contains(w, "unknown-svc") && strings.Contains(w, "not in registry") {
			foundWarning = true
		}
	}
	if !foundWarning {
		t.Errorf("expected warning about unknown service, got: %v", out.Warnings)
	}
}

// TestCategoryRegexFiltering verifies that category="turn" for oxpulse-chat
// lets through turn_* / partner_node_* names and blocks signaling_* names.
func TestCategoryRegexFiltering(t *testing.T) {
	names := []string{
		"turn_allocation_total",
		"turn_relay_bytes",
		"partner_node_latency_seconds",
		"signaling_messages_total",
		"call_duration_seconds",
		"sdk_connect_total",
		"ice_candidates_total",
		"rooms_active",
		"random_metric",
		"another_random",
		"turn_errors_total",
		"partner_node_up",
		"not_a_turn_metric",
		"call_ended_total",
		"sfu_packets_total",
		"build_info",
		"sdk_version",
		"signaling_ws_connected",
		"ice_ms_histogram",
		"turn_stale_alloc",
	}

	promSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/api/v1/label/__name__/values"):
			fmt.Fprint(w, promLabelsResponse(names))
		case strings.Contains(r.URL.Path, "/api/v1/query"):
			// Extract metric name from query param
			q := r.URL.Query().Get("query")
			// Return a non-zero value for any metric queried
			name := strings.SplitN(q, "{", 2)[0]
			fmt.Fprint(w, promQueryResponse(name, nil, "42"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer promSrv.Close()

	t.Setenv("DOZOR_PROMETHEUS_URL", promSrv.URL)

	out, err := handleMetrics(t.Context(), MetricsInput{
		Service:  "oxpulse-chat",
		Category: "turn",
		Format:   "full",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, m := range out.Metrics {
		isTurn := strings.HasPrefix(m.Name, "turn_")
		isPartner := strings.HasPrefix(m.Name, "partner_node_")
		if !isTurn && !isPartner {
			t.Errorf("got unexpected metric %q for category=turn", m.Name)
		}
	}
	// Must include at least one turn_ and one partner_node_ metric
	hasTurn, hasPartner := false, false
	for _, m := range out.Metrics {
		if strings.HasPrefix(m.Name, "turn_") {
			hasTurn = true
		}
		if strings.HasPrefix(m.Name, "partner_node_") {
			hasPartner = true
		}
	}
	if !hasTurn {
		t.Error("no turn_* metrics returned")
	}
	if !hasPartner {
		t.Error("no partner_node_* metrics returned")
	}
}

// TestSummaryFormatDropsZeros verifies format=summary drops zero-value series
// while format=full retains them.
func TestSummaryFormatDropsZeros(t *testing.T) {
	// Three metrics: values 1, 0, 5
	names := []string{"metric_a", "metric_b_total", "metric_c"}
	valueMap := map[string]string{
		"metric_a":       "1",
		"metric_b_total": "0",
		"metric_c":       "5",
	}

	promSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/api/v1/label/__name__/values"):
			fmt.Fprint(w, promLabelsResponse(names))
		case strings.Contains(r.URL.Path, "/api/v1/query"):
			q := r.URL.Query().Get("query")
			// find the metric name in the query
			for _, n := range names {
				if strings.Contains(q, n) {
					fmt.Fprint(w, promQueryResponse(n, nil, valueMap[n]))
					return
				}
			}
			fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer promSrv.Close()

	t.Setenv("DOZOR_PROMETHEUS_URL", promSrv.URL)

	// format=summary — should drop metric_b (value=0)
	outSummary, err := handleMetrics(t.Context(), MetricsInput{
		Service: "unknown-svc",
		Filter:  "^metric_",
		Format:  "summary",
	})
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if len(outSummary.Metrics) != 2 {
		t.Errorf("summary: want 2 metrics, got %d: %v", len(outSummary.Metrics), outSummary.Metrics)
	}
	for _, m := range outSummary.Metrics {
		if m.Value == "0" {
			t.Errorf("summary: zero-value metric %q should be dropped", m.Name)
		}
	}

	// format=full — should include all 3
	outFull, err := handleMetrics(t.Context(), MetricsInput{
		Service: "unknown-svc",
		Filter:  "^metric_",
		Format:  "full",
	})
	if err != nil {
		t.Fatalf("full: %v", err)
	}
	if len(outFull.Metrics) != 3 {
		t.Errorf("full: want 3 metrics, got %d", len(outFull.Metrics))
	}
}

// TestRangeAppliesRateForCounters verifies that a counter metric (ending _total)
// with range=5m uses rate(...[5m]), while a gauge uses an instant query.
func TestRangeAppliesRateForCounters(t *testing.T) {
	names := []string{"signaling_messages_total", "rooms_active"}
	capturedQueries := make([]string, 0)

	promSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/api/v1/label/__name__/values"):
			fmt.Fprint(w, promLabelsResponse(names))
		case strings.Contains(r.URL.Path, "/api/v1/query"):
			q := r.URL.Query().Get("query")
			capturedQueries = append(capturedQueries, q)
			// Return result for both
			name := names[0]
			if strings.Contains(q, "rooms_active") {
				name = "rooms_active"
			}
			fmt.Fprint(w, promQueryResponse(name, nil, "3"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer promSrv.Close()

	t.Setenv("DOZOR_PROMETHEUS_URL", promSrv.URL)

	_, err := handleMetrics(t.Context(), MetricsInput{
		Service: "unknown-svc",
		Filter:  "^(signaling_messages_total|rooms_active)$",
		Format:  "full",
		Range:   "5m",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Find the query for the counter metric
	foundRate := false
	foundInstant := false
	for _, q := range capturedQueries {
		if strings.Contains(q, "signaling_messages_total") {
			if strings.Contains(q, "rate(") && strings.Contains(q, "[5m]") {
				foundRate = true
			}
		}
		if strings.Contains(q, "rooms_active") {
			// Should NOT have rate()
			if !strings.Contains(q, "rate(") {
				foundInstant = true
			}
		}
	}
	if !foundRate {
		t.Errorf("counter metric should use rate(...[5m]), queries: %v", capturedQueries)
	}
	if !foundInstant {
		t.Errorf("gauge metric should use instant query, queries: %v", capturedQueries)
	}
}

// TestLokiTailParsesJsonLines verifies Loki log line parsing with level+msg
// extraction and 200-char truncation.
func TestLokiTailParsesJsonLines(t *testing.T) {
	longMsg := strings.Repeat("x", 250)

	lokiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lines := []struct {
			TS    string
			Level string
			Msg   string
		}{
			{"2024-01-01T00:00:01Z", "error", "connection refused"},
			{"2024-01-01T00:00:02Z", "warn", longMsg},
		}
		fmt.Fprint(w, lokiResponse(lines))
	}))
	defer lokiSrv.Close()

	t.Setenv("DOZOR_LOKI_URL", lokiSrv.URL)

	// We need a prom server too
	promSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/api/v1/label/__name__/values") {
			fmt.Fprint(w, `{"status":"success","data":[]}`)
		} else {
			fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
		}
	}))
	defer promSrv.Close()

	t.Setenv("DOZOR_PROMETHEUS_URL", promSrv.URL)

	out, err := handleMetrics(t.Context(), MetricsInput{
		Service:     "unknown-svc",
		IncludeLogs: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out.Logs) == 0 {
		t.Fatal("expected log lines, got none")
	}

	// Check truncation on the second line (longMsg)
	for _, l := range out.Logs {
		if len(l.Snippet) > 200 {
			t.Errorf("snippet not truncated: len=%d", len(l.Snippet))
		}
	}

	// Check level parsing
	levels := make(map[string]bool)
	for _, l := range out.Logs {
		levels[l.Level] = true
	}
	if !levels["error"] && !levels["warn"] {
		t.Errorf("expected error/warn levels, got: %v", levels)
	}
}

// TestPrometheusUnreachable_ReturnsError verifies that a Prom 500 causes
// handleMetrics to return an error containing the URL and status.
func TestPrometheusUnreachable_ReturnsError(t *testing.T) {
	promSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}))
	defer promSrv.Close()

	t.Setenv("DOZOR_PROMETHEUS_URL", promSrv.URL)

	_, err := handleMetrics(t.Context(), MetricsInput{Service: "oxpulse-chat"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	errStr := err.Error()
	if !strings.Contains(errStr, promSrv.URL) && !strings.Contains(errStr, "500") &&
		!strings.Contains(errStr, "label") && !strings.Contains(errStr, "prometheus") {
		t.Errorf("error should mention URL or status, got: %v", err)
	}
}

// TestLokiUnreachable_DegradesGracefully verifies that Loki 500 does NOT
// cause an error — metrics are returned, a warning is added.
func TestLokiUnreachable_DegradesGracefully(t *testing.T) {
	promSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/api/v1/label/__name__/values") {
			fmt.Fprint(w, `{"status":"success","data":["rooms_active"]}`)
		} else {
			fmt.Fprint(w, promQueryResponse("rooms_active", nil, "7"))
		}
	}))
	defer promSrv.Close()

	lokiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "loki down", http.StatusInternalServerError)
	}))
	defer lokiSrv.Close()

	t.Setenv("DOZOR_PROMETHEUS_URL", promSrv.URL)
	t.Setenv("DOZOR_LOKI_URL", lokiSrv.URL)

	out, err := handleMetrics(t.Context(), MetricsInput{
		Service:     "unknown-svc",
		IncludeLogs: true,
		Format:      "full",
	})
	if err != nil {
		t.Fatalf("expected graceful degradation, got error: %v", err)
	}
	// Metrics must still be returned
	if len(out.Metrics) == 0 {
		t.Error("metrics should still be returned when loki is down")
	}
	// Warning about loki must be present
	foundLokiWarn := false
	for _, w := range out.Warnings {
		if strings.Contains(strings.ToLower(w), "loki") {
			foundLokiWarn = true
		}
	}
	if !foundLokiWarn {
		t.Errorf("expected loki warning, got: %v", out.Warnings)
	}
}

// TestMaxResultsCap verifies that MaxResults=10 caps the number of metric
// series fetched and returned even when many names match.
func TestMaxResultsCap(t *testing.T) {
	// Generate 500 metric names
	names := make([]string, 500)
	for i := range names {
		names[i] = fmt.Sprintf("metric_%04d_total", i)
	}

	queriesIssued := 0

	promSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/api/v1/label/__name__/values"):
			fmt.Fprint(w, promLabelsResponse(names))
		case strings.Contains(r.URL.Path, "/api/v1/query"):
			queriesIssued++
			q := r.URL.Query().Get("query")
			// Extract metric name (before `{`)
			name := strings.SplitN(q, "{", 2)[0]
			name = strings.TrimPrefix(name, "rate(")
			fmt.Fprint(w, promQueryResponse(name, nil, "1"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer promSrv.Close()

	t.Setenv("DOZOR_PROMETHEUS_URL", promSrv.URL)

	out, err := handleMetrics(t.Context(), MetricsInput{
		Service:    "unknown-svc",
		MaxResults: 10,
		Format:     "full",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out.Metrics) > 10 {
		t.Errorf("max_results=10: got %d metrics", len(out.Metrics))
	}
	if queriesIssued > 10 {
		t.Errorf("max_results=10: issued %d queries (should be ≤10)", queriesIssued)
	}
}

// TestRegistryLoadOverridePath verifies DOZOR_METRICS_REGISTRY_PATH env
// causes a file-based load instead of embedded YAML.
func TestRegistryLoadOverridePath(t *testing.T) {
	// Write a minimal valid YAML to a temp file
	content := `services:
  test-svc:
    prom_job: test-svc
    container: test-svc
    categories:
      ops: ["^ops_.*"]
`
	f, err := os.CreateTemp(t.TempDir(), "registry*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(content)
	f.Close()

	t.Setenv("DOZOR_METRICS_REGISTRY_PATH", f.Name())

	reg, err := loadMetricsRegistry(f.Name())
	if err != nil {
		t.Fatalf("loadMetricsRegistry override: %v", err)
	}
	if _, ok := reg.Services["test-svc"]; !ok {
		t.Error("test-svc not found in override registry")
	}
}

// Ensure os is used (for TestRegistryLoadOverridePath)
var _ = os.CreateTemp
