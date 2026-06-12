package tools

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
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

// ---- new tests: Jaeger + LogQuery/TraceQuery ---------------------------------

// TestJaegerTracesParsed verifies that include_traces=true fetches Jaeger
// traces, parses durations, spans, ISO timestamps, and HasError.
func TestJaegerTracesParsed(t *testing.T) {
	// Mock Jaeger returning 2 traces (one with error=true tag, one with
	// http.status_code=500). Both should have HasError=true per the
	// response structure.
	jaegerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Validate it is the /api/traces endpoint
		if !strings.Contains(r.URL.Path, "/api/traces") {
			http.NotFound(w, r)
			return
		}
		// Two traces: durations in microseconds, spans, tags
		resp := `{
			"data": [
				{
					"traceID": "abc123",
					"spans": [
						{
							"operationName": "http.request",
							"duration": 1200000,
							"startTime": 1700000000000000,
							"tags": [
								{"key":"error","type":"bool","value":true}
							],
							"references": []
						},
						{
							"operationName": "db.query",
							"duration": 50000,
							"startTime": 1700000000100000,
							"tags": [],
							"references": [{"refType":"CHILD_OF","traceID":"abc123","spanID":"x"}]
						}
					],
					"processes": {}
				},
				{
					"traceID": "def456",
					"spans": [
						{
							"operationName": "grpc.call",
							"duration": 340000,
							"startTime": 1700000001000000,
							"tags": [
								{"key":"http.status_code","type":"int64","value":500}
							],
							"references": []
						}
					],
					"processes": {}
				}
			],
			"total": 2,
			"limit": 20,
			"errors": null
		}`
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, resp)
	}))
	defer jaegerSrv.Close()

	// Minimal prom mock (no metrics needed for this test focus)
	promSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/api/v1/label/__name__/values") {
			fmt.Fprint(w, `{"status":"success","data":[]}`)
		} else {
			fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
		}
	}))
	defer promSrv.Close()

	t.Setenv("DOZOR_PROMETHEUS_URL", promSrv.URL)
	t.Setenv("DOZOR_LOKI_URL", "")
	t.Setenv("DOZOR_JAEGER_URL", jaegerSrv.URL)

	out, err := handleMetrics(t.Context(), MetricsInput{
		Service:       "oxpulse-chat",
		IncludeTraces: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(out.Traces) != 2 {
		t.Fatalf("expected 2 traces, got %d", len(out.Traces))
	}

	// Both traces must have HasError=true
	for i, tr := range out.Traces {
		if !tr.HasError {
			t.Errorf("trace[%d] HasError should be true", i)
		}
	}

	// First trace: 2 spans, duration ~1.2s (root span is 1200000 µs)
	tr0 := out.Traces[0]
	if tr0.TraceID != "abc123" {
		t.Errorf("trace[0] TraceID: want abc123, got %q", tr0.TraceID)
	}
	if tr0.Spans != 2 {
		t.Errorf("trace[0] Spans: want 2, got %d", tr0.Spans)
	}
	if tr0.Duration == "" {
		t.Error("trace[0] Duration should not be empty")
	}
	if tr0.StartTime == "" {
		t.Error("trace[0] StartTime should not be empty")
	}
	// StartTime should be ISO 8601
	if !strings.Contains(tr0.StartTime, "T") || !strings.Contains(tr0.StartTime, "Z") {
		t.Errorf("trace[0] StartTime not ISO 8601: %q", tr0.StartTime)
	}

	// Second trace: 1 span
	tr1 := out.Traces[1]
	if tr1.TraceID != "def456" {
		t.Errorf("trace[1] TraceID: want def456, got %q", tr1.TraceID)
	}
	if tr1.Spans != 1 {
		t.Errorf("trace[1] Spans: want 1, got %d", tr1.Spans)
	}

	// Source must include "jaeger"
	if !strings.Contains(out.Source, "jaeger") {
		t.Errorf("source should include jaeger, got %q", out.Source)
	}
}

// TestJaegerUnreachable_DegradesGracefully verifies that Jaeger 500 does NOT
// cause an error — metrics + logs are still returned, warning mentions "jaeger".
func TestJaegerUnreachable_DegradesGracefully(t *testing.T) {
	jaegerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "jaeger down", http.StatusInternalServerError)
	}))
	defer jaegerSrv.Close()

	lokiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lines := []struct {
			TS    string
			Level string
			Msg   string
		}{
			{"2024-01-01T00:00:01Z", "error", "something failed"},
		}
		fmt.Fprint(w, lokiResponse(lines))
	}))
	defer lokiSrv.Close()

	promSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/api/v1/label/__name__/values") {
			fmt.Fprint(w, `{"status":"success","data":["rooms_active"]}`)
		} else {
			fmt.Fprint(w, promQueryResponse("rooms_active", nil, "5"))
		}
	}))
	defer promSrv.Close()

	t.Setenv("DOZOR_PROMETHEUS_URL", promSrv.URL)
	t.Setenv("DOZOR_LOKI_URL", lokiSrv.URL)
	t.Setenv("DOZOR_JAEGER_URL", jaegerSrv.URL)

	out, err := handleMetrics(t.Context(), MetricsInput{
		Service:       "unknown-svc",
		IncludeLogs:   true,
		IncludeTraces: true,
		Format:        "full",
	})
	if err != nil {
		t.Fatalf("expected graceful degradation, got error: %v", err)
	}

	// Metrics must still be returned
	if len(out.Metrics) == 0 {
		t.Error("metrics should still be returned when jaeger is down")
	}
	// Traces must be empty (not an error)
	if len(out.Traces) != 0 {
		t.Errorf("expected empty traces on jaeger failure, got %d", len(out.Traces))
	}
	// Warning about jaeger must be present
	foundJaegerWarn := false
	for _, w := range out.Warnings {
		if strings.Contains(strings.ToLower(w), "jaeger") {
			foundJaegerWarn = true
		}
	}
	if !foundJaegerWarn {
		t.Errorf("expected jaeger warning, got: %v", out.Warnings)
	}
}

// TestLogQueryOverridesDefaultFilter verifies that log_query="custom_pattern"
// replaces the default "error|warn|panic|..." regex in the Loki request.
func TestLogQueryOverridesDefaultFilter(t *testing.T) {
	capturedQuery := ""

	lokiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Capture the raw query param to inspect the regex used
		capturedQuery = r.URL.Query().Get("query")
		fmt.Fprint(w, lokiResponse([]struct {
			TS    string
			Level string
			Msg   string
		}{}))
	}))
	defer lokiSrv.Close()

	promSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/api/v1/label/__name__/values") {
			fmt.Fprint(w, `{"status":"success","data":[]}`)
		} else {
			fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
		}
	}))
	defer promSrv.Close()

	t.Setenv("DOZOR_PROMETHEUS_URL", promSrv.URL)
	t.Setenv("DOZOR_LOKI_URL", lokiSrv.URL)

	_, err := handleMetrics(t.Context(), MetricsInput{
		Service:     "oxpulse-chat",
		IncludeLogs: true,
		LogQuery:    "custom_pattern",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The captured query must contain "custom_pattern" NOT the default filter
	if !strings.Contains(capturedQuery, "custom_pattern") {
		t.Errorf("Loki query should contain custom_pattern, got: %q", capturedQuery)
	}
	if strings.Contains(capturedQuery, "error|warn|panic") {
		t.Errorf("Loki query should NOT contain default filter when LogQuery set, got: %q", capturedQuery)
	}
}

// ---- new tests: Loki log_query escaping + silent-drop fixes -----------------

// TestFetchLokiLogs_EscapesDoubleQuote verifies that a log_query containing a
// double-quote character is escaped to \" in the outgoing Loki URL so that the
// LogQL string literal is not broken. Without the fix the LogQL becomes
//
//	{container="X"} |~ "prefix"suffix"
//
// which Loki rejects with HTTP 400.
func TestFetchLokiLogs_EscapesDoubleQuote(t *testing.T) {
	capturedQuery := ""

	lokiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedQuery = r.URL.Query().Get("query")
		fmt.Fprint(w, lokiResponse([]struct {
			TS    string
			Level string
			Msg   string
		}{}))
	}))
	defer lokiSrv.Close()

	promSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/api/v1/label/__name__/values") {
			fmt.Fprint(w, `{"status":"success","data":[]}`)
		} else {
			fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
		}
	}))
	defer promSrv.Close()

	t.Setenv("DOZOR_PROMETHEUS_URL", promSrv.URL)
	t.Setenv("DOZOR_LOKI_URL", lokiSrv.URL)

	// log_query that contains a literal double-quote — the bug trigger.
	_, err := handleMetrics(t.Context(), MetricsInput{
		Service:     "oxpulse-chat",
		IncludeLogs: true,
		LogQuery:    `prefix"suffix`,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The raw LogQL string in the query param must contain \" (escaped), NOT a
	// bare " that would terminate the regex literal prematurely.
	if !strings.Contains(capturedQuery, `\"`) {
		t.Errorf("loki query must contain escaped double-quote, got: %q", capturedQuery)
	}
	// And must NOT contain an unescaped bare " inside the regex part after |~
	// i.e. split on |~ and check the regex half.
	parts := strings.SplitN(capturedQuery, `|~ "`, 2)
	if len(parts) == 2 {
		regexPart := parts[1]
		// Strip the closing " — if the regex part has a bare " before the end, it's broken.
		if idx := strings.Index(regexPart, `"`); idx >= 0 && !strings.HasSuffix(regexPart[:idx+1], `\"`) {
			// bare unescaped " in the middle of the regex literal
			t.Errorf("unescaped double-quote inside LogQL regex: %q", capturedQuery)
		}
	}
}

// TestFetchLokiLogs_4xxResponseSurfacedAsWarning verifies that a Loki HTTP 400
// response (e.g. LogQL parse error) is surfaced as a warning string containing
// the status code and the response body — not silently swallowed.
// Relates to investigator report: 2026-05-30-ios-reconnect-video-asymmetry.md.
func TestFetchLokiLogs_4xxResponseSurfacedAsWarning(t *testing.T) {
	lokiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `parse error at line 1, col 49: syntax error: unexpected IDENTIFIER`)
	}))
	defer lokiSrv.Close()

	promSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/api/v1/label/__name__/values") {
			fmt.Fprint(w, `{"status":"success","data":[]}`)
		} else {
			fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
		}
	}))
	defer promSrv.Close()

	t.Setenv("DOZOR_PROMETHEUS_URL", promSrv.URL)
	t.Setenv("DOZOR_LOKI_URL", lokiSrv.URL)

	out, err := handleMetrics(t.Context(), MetricsInput{
		Service:     "oxpulse-chat",
		IncludeLogs: true,
		LogQuery:    "iphone|iOS",
	})
	if err != nil {
		t.Fatalf("expected graceful degradation, got error: %v", err)
	}

	// Must emit a warning containing the HTTP status code.
	foundWarn := false
	for _, w := range out.Warnings {
		wLower := strings.ToLower(w)
		if strings.Contains(wLower, "loki") && strings.Contains(w, "400") {
			foundWarn = true
		}
	}
	if !foundWarn {
		t.Errorf("expected warning containing 'loki' and '400', got: %v", out.Warnings)
	}

	// The warning must also carry enough of the body to be useful.
	for _, w := range out.Warnings {
		if strings.Contains(w, "400") {
			if !strings.Contains(strings.ToLower(w), "parse error") &&
				!strings.Contains(strings.ToLower(w), "syntax error") &&
				!strings.Contains(strings.ToLower(w), "unexpected") {
				t.Errorf("warning should include body snippet, got: %q", w)
			}
		}
	}
}

// TestFetchLokiLogs_EmptyMatchEmitsWarning verifies that when Loki returns
// HTTP 200 with an empty result set (0 log lines), the caller receives a
// warning. Without the fix, the caller cannot distinguish "no matching logs"
// from "query silently failed" — the silent-drop bug from the empirical repro.
func TestFetchLokiLogs_EmptyMatchEmitsWarning(t *testing.T) {
	lokiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Loki 200 OK but empty result
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"streams","result":[]}}`)
	}))
	defer lokiSrv.Close()

	promSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/api/v1/label/__name__/values") {
			fmt.Fprint(w, `{"status":"success","data":[]}`)
		} else {
			fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
		}
	}))
	defer promSrv.Close()

	t.Setenv("DOZOR_PROMETHEUS_URL", promSrv.URL)
	t.Setenv("DOZOR_LOKI_URL", lokiSrv.URL)

	out, err := handleMetrics(t.Context(), MetricsInput{
		Service:     "oxpulse-chat",
		IncludeLogs: true,
		LogQuery:    "iphone|iOS|reconnect",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Must emit a warning containing "0 matching" or similar signal.
	foundWarn := false
	for _, w := range out.Warnings {
		wLower := strings.ToLower(w)
		if strings.Contains(wLower, "loki") && strings.Contains(wLower, "0") {
			foundWarn = true
		}
	}
	if !foundWarn {
		t.Errorf("expected loki empty-match warning, got: %v", out.Warnings)
	}
}

// TestFetchLokiLogs_HappyPath verifies the existing happy-path (Loki 200 with
// results) still works after the escape + warning changes.
// Mirrors the pattern from TestLokiTailParsesJsonLines but exercises the
// log_query override path to confirm escaping doesn't break valid ASCII queries.
func TestFetchLokiLogs_HappyPath(t *testing.T) {
	lokiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lines := []struct {
			TS    string
			Level string
			Msg   string
		}{
			{"2024-01-01T00:00:01Z", "error", "video track ended unexpectedly"},
			{"2024-01-01T00:00:02Z", "warn", "replaceTrack called after close"},
		}
		fmt.Fprint(w, lokiResponse(lines))
	}))
	defer lokiSrv.Close()

	promSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/api/v1/label/__name__/values") {
			fmt.Fprint(w, `{"status":"success","data":[]}`)
		} else {
			fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
		}
	}))
	defer promSrv.Close()

	t.Setenv("DOZOR_PROMETHEUS_URL", promSrv.URL)
	t.Setenv("DOZOR_LOKI_URL", lokiSrv.URL)

	out, err := handleMetrics(t.Context(), MetricsInput{
		Service:     "oxpulse-chat",
		IncludeLogs: true,
		LogQuery:    "track_ended|replaceTrack",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(out.Logs) == 0 {
		t.Fatal("expected log lines, got none")
	}
	// No loki-error warning must be present (happy path).
	for _, w := range out.Warnings {
		if strings.Contains(strings.ToLower(w), "loki http") {
			t.Errorf("unexpected loki error warning on happy path: %q", w)
		}
	}
}

// TestTraceQueryParsedToTags verifies that trace_query="error=true,http.status_code=500"
// is parsed into JSON tags and sent to Jaeger. Also verifies that empty trace_query
// defaults to {"error":"true"}.
func TestTraceQueryParsedToTags(t *testing.T) {
	capturedTagsParam := ""

	jaegerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedTagsParam = r.URL.Query().Get("tags")
		// Return empty traces response
		fmt.Fprint(w, `{"data":[],"total":0,"limit":20,"errors":null}`)
	}))
	defer jaegerSrv.Close()

	promSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/api/v1/label/__name__/values") {
			fmt.Fprint(w, `{"status":"success","data":[]}`)
		} else {
			fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
		}
	}))
	defer promSrv.Close()

	t.Setenv("DOZOR_PROMETHEUS_URL", promSrv.URL)
	t.Setenv("DOZOR_LOKI_URL", "")
	t.Setenv("DOZOR_JAEGER_URL", jaegerSrv.URL)

	// Sub-test 1: explicit trace_query with two tags
	t.Run("explicit_tags", func(t *testing.T) {
		capturedTagsParam = ""
		_, err := handleMetrics(t.Context(), MetricsInput{
			Service:       "oxpulse-chat",
			IncludeTraces: true,
			TraceQuery:    "error=true,http.status_code=500",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// tags param should be a JSON object with both keys
		var tags map[string]string
		if err := json.Unmarshal([]byte(capturedTagsParam), &tags); err != nil {
			t.Fatalf("tags param not valid JSON: %q, err: %v", capturedTagsParam, err)
		}
		if tags["error"] != "true" {
			t.Errorf("tags[error]: want 'true', got %q", tags["error"])
		}
		if tags["http.status_code"] != "500" {
			t.Errorf("tags[http.status_code]: want '500', got %q", tags["http.status_code"])
		}
	})

	// Sub-test 2: empty trace_query → default {"error":"true"}
	t.Run("default_error_tag", func(t *testing.T) {
		capturedTagsParam = ""
		_, err := handleMetrics(t.Context(), MetricsInput{
			Service:       "oxpulse-chat",
			IncludeTraces: true,
			TraceQuery:    "",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		var tags map[string]string
		if err := json.Unmarshal([]byte(capturedTagsParam), &tags); err != nil {
			t.Fatalf("default tags param not valid JSON: %q, err: %v", capturedTagsParam, err)
		}
		if tags["error"] != "true" {
			t.Errorf("default tags[error]: want 'true', got %q", tags["error"])
		}
	})
}

// ---- 5 new quality-batch tests (PR #84) --------------------------------------

// TestQueryMetrics_FallsBackToQueryNameWhenMetricNameLabelMissing verifies Fix 1:
// when a Prometheus series lacks __name__ in its metric map (recording rules,
// computed series), the sample's Name field must be the queried metric name, not
// an empty string. A single fallback warning must also be emitted.
// Cites operator empirical finding: ~10 entries with "name":"" in live call.
func TestQueryMetrics_FallsBackToQueryNameWhenMetricNameLabelMissing(t *testing.T) {
	const queriedName = "my_recording_rule"

	promSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/api/v1/label/__name__/values"):
			fmt.Fprint(w, promLabelsResponse([]string{queriedName}))
		case strings.Contains(r.URL.Path, "/api/v1/query"):
			// Intentionally omit __name__ from metric labels to trigger Fix 1.
			// This mirrors what Prometheus returns for recording-rule expressions.
			fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{"some_label":"val"},"value":[1700000000,"42"]}]}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer promSrv.Close()

	t.Setenv("DOZOR_PROMETHEUS_URL", promSrv.URL)

	out, err := handleMetrics(t.Context(), MetricsInput{
		Service: "unknown-svc",
		Filter:  "^" + queriedName + "$",
		Format:  "full",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(out.Metrics) == 0 {
		t.Fatal("expected at least one metric sample")
	}
	for _, m := range out.Metrics {
		if m.Name == "" {
			t.Errorf("sample Name must not be empty; should fall back to queried name %q", queriedName)
		}
		if m.Name != queriedName {
			t.Errorf("sample Name: want %q, got %q", queriedName, m.Name)
		}
	}

	// Exactly one fallback warning must be present.
	fallbackWarns := 0
	for _, w := range out.Warnings {
		if strings.Contains(w, "fallback") || strings.Contains(w, "__name__") {
			fallbackWarns++
		}
	}
	if fallbackWarns == 0 {
		t.Errorf("expected a fallback warning about missing __name__ label, got: %v", out.Warnings)
	}
}

// TestHandleMetrics_OutputIncludesLokiAndJaegerURLs verifies Fix 2:
// when include_logs=true and include_traces=true, the output must contain
// populated loki_url and jaeger_url fields. basis_url (prometheus) must
// still be present for backward compatibility.
func TestHandleMetrics_OutputIncludesLokiAndJaegerURLs(t *testing.T) {
	jaegerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[],"total":0,"limit":20,"errors":null}`)
	}))
	defer jaegerSrv.Close()

	lokiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"streams","result":[]}}`)
	}))
	defer lokiSrv.Close()

	promSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/api/v1/label/__name__/values") {
			fmt.Fprint(w, `{"status":"success","data":[]}`)
		} else {
			fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
		}
	}))
	defer promSrv.Close()

	t.Setenv("DOZOR_PROMETHEUS_URL", promSrv.URL)
	t.Setenv("DOZOR_LOKI_URL", lokiSrv.URL)
	t.Setenv("DOZOR_JAEGER_URL", jaegerSrv.URL)

	out, err := handleMetrics(t.Context(), MetricsInput{
		Service:       "oxpulse-chat",
		IncludeLogs:   true,
		IncludeTraces: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// basis_url must still be the prometheus URL (backward compat).
	if out.BasisURL != promSrv.URL {
		t.Errorf("basis_url: want %q, got %q", promSrv.URL, out.BasisURL)
	}
	// loki_url must be populated when include_logs=true.
	if out.LokiURL != lokiSrv.URL {
		t.Errorf("loki_url: want %q, got %q", lokiSrv.URL, out.LokiURL)
	}
	// jaeger_url must be populated when include_traces=true.
	if out.JaegerURL != jaegerSrv.URL {
		t.Errorf("jaeger_url: want %q, got %q", jaegerSrv.URL, out.JaegerURL)
	}
}

// TestFetchJaegerTraces_HandlesSpecialChars verifies Fix 3:
// a trace_query containing special characters (quotes, ampersands) must be
// correctly encoded in the outgoing Jaeger URL so that the tags= param is valid.
func TestFetchJaegerTraces_HandlesSpecialChars(t *testing.T) {
	capturedRawQuery := ""

	jaegerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedRawQuery = r.URL.RawQuery
		fmt.Fprint(w, `{"data":[],"total":0,"limit":20,"errors":null}`)
	}))
	defer jaegerSrv.Close()

	promSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/api/v1/label/__name__/values") {
			fmt.Fprint(w, `{"status":"success","data":[]}`)
		} else {
			fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
		}
	}))
	defer promSrv.Close()

	t.Setenv("DOZOR_PROMETHEUS_URL", promSrv.URL)
	t.Setenv("DOZOR_LOKI_URL", "")
	t.Setenv("DOZOR_JAEGER_URL", jaegerSrv.URL)

	// trace_query with special chars: quotes and ampersand.
	_, err := handleMetrics(t.Context(), MetricsInput{
		Service:       "oxpulse-chat",
		IncludeTraces: true,
		TraceQuery:    `error=true,http.url=/api/v1?foo=bar&baz="qux"`,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The raw query string must not contain an unencoded literal & or " outside
	// the tags= value. Verify tags= is present and URL-encoded.
	if !strings.Contains(capturedRawQuery, "tags=") {
		t.Errorf("Jaeger request missing tags= param; raw query: %q", capturedRawQuery)
	}

	// Decode the tags param value and verify it is valid JSON.
	decoded, decodeErr := url.QueryUnescape(capturedRawQuery)
	if decodeErr != nil {
		t.Fatalf("QueryUnescape failed: %v", decodeErr)
	}
	// Extract tags value: find "tags=" and take the value up to next "&".
	tagsStart := strings.Index(decoded, "tags=")
	if tagsStart < 0 {
		t.Fatalf("tags= not found after decode: %q", decoded)
	}
	tagsVal := decoded[tagsStart+len("tags="):]
	if idx := strings.Index(tagsVal, "&"); idx >= 0 {
		tagsVal = tagsVal[:idx]
	}
	var tags map[string]string
	if jsonErr := json.Unmarshal([]byte(tagsVal), &tags); jsonErr != nil {
		t.Errorf("tags= value is not valid JSON after decode: %q, err: %v", tagsVal, jsonErr)
	}
}

// TestFetchJaegerTraces_EmptyResultEmitsWarning verifies Fix 4:
// when Jaeger returns HTTP 200 with {"data":[]}, a warning must be emitted so
// the operator can distinguish "0 matching traces" from a silent failure.
// Mirrors PR #83's Loki empty-match warning (same class of bug).
func TestFetchJaegerTraces_EmptyResultEmitsWarning(t *testing.T) {
	const jaegerSvc = "oxpulse-chat"

	jaegerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[],"total":0,"limit":20,"errors":null}`)
	}))
	defer jaegerSrv.Close()

	promSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/api/v1/label/__name__/values") {
			fmt.Fprint(w, `{"status":"success","data":[]}`)
		} else {
			fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
		}
	}))
	defer promSrv.Close()

	t.Setenv("DOZOR_PROMETHEUS_URL", promSrv.URL)
	t.Setenv("DOZOR_LOKI_URL", "")
	t.Setenv("DOZOR_JAEGER_URL", jaegerSrv.URL)

	out, err := handleMetrics(t.Context(), MetricsInput{
		Service:       jaegerSvc,
		IncludeTraces: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Must emit a warning containing "jaeger" and "0".
	foundWarn := false
	for _, w := range out.Warnings {
		wLower := strings.ToLower(w)
		if strings.Contains(wLower, "jaeger") && strings.Contains(wLower, "0") {
			foundWarn = true
		}
	}
	if !foundWarn {
		t.Errorf("expected jaeger empty-traces warning, got: %v", out.Warnings)
	}
}

// TestHandleMetrics_NoMatchWarningIncludesFilterInput verifies Fix 5:
// when no metrics match the filter, the warning must include the filter value
// so the operator can see at a glance what was excluded.
func TestHandleMetrics_NoMatchWarningIncludesFilterInput(t *testing.T) {
	const noMatchFilter = "^xxxx_no_match_ever_"

	promSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return some real metric names that won't match the filter.
		if strings.Contains(r.URL.Path, "/api/v1/label/__name__/values") {
			fmt.Fprint(w, promLabelsResponse([]string{"rooms_active", "signaling_messages_total"}))
		} else {
			fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
		}
	}))
	defer promSrv.Close()

	t.Setenv("DOZOR_PROMETHEUS_URL", promSrv.URL)

	out, err := handleMetrics(t.Context(), MetricsInput{
		Service: "unknown-svc",
		Filter:  noMatchFilter,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	foundWarn := false
	for _, w := range out.Warnings {
		if strings.Contains(w, noMatchFilter) {
			foundWarn = true
		}
	}
	if !foundWarn {
		t.Errorf("no-match warning must include filter %q, got: %v", noMatchFilter, out.Warnings)
	}
}

// ---- structured-level filter tests (fix: false-positive error log counts) ----

// TestFetchLokiLogs_StructuredLevelFilter verifies that when no log_query
// override is given, the default Loki query uses JSON label parsing
// ("| json | level=~...") instead of a raw substring match, so that
// INFO-level lines whose JSON payload contains "error_class" are NOT counted
// as errors.
//
// Falsification: if the fix is reverted and the query goes back to
// "|~ error|warn|panic", the structured-query path never fires and the
// capturedStructuredQuery assertion fails — test goes RED.
func TestFetchLokiLogs_StructuredLevelFilter(t *testing.T) {
	// Track which Loki queries arrive so we can assert the query shape.
	capturedQueries := make([]string, 0)

	// lokiResponseRaw builds a raw Loki response where the server has already
	// applied label filtering -- only the lines that pass the structured filter
	// are returned. We simulate this by returning only the ERROR-level line.
	lokiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		capturedQueries = append(capturedQueries, q)

		if strings.Contains(q, "| json |") {
			// Structured query: server returns only the ERROR-level line.
			// INFO line with error_class in payload is NOT returned (server filtered it).
			lines := []struct {
				TS    string
				Level string
				Msg   string
			}{
				{"2024-01-01T00:00:01Z", "error", "real server error occurred"},
			}
			fmt.Fprint(w, lokiResponse(lines))
		} else {
			// Panic-signatures query (or any other): return empty.
			fmt.Fprint(w, `{"status":"success","data":{"resultType":"streams","result":[]}}`)
		}
	}))
	defer lokiSrv.Close()

	promSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/api/v1/label/__name__/values") {
			fmt.Fprint(w, `{"status":"success","data":[]}`)
		} else {
			fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
		}
	}))
	defer promSrv.Close()

	t.Setenv("DOZOR_PROMETHEUS_URL", promSrv.URL)
	t.Setenv("DOZOR_LOKI_URL", lokiSrv.URL)

	out, err := handleMetrics(t.Context(), MetricsInput{
		Service:     "oxpulse-chat",
		IncludeLogs: true,
		// No LogQuery override -- exercises the default structured path.
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 1. The outgoing Loki query must use JSON label parsing, NOT a raw substring.
	foundStructured := false
	for _, q := range capturedQueries {
		if strings.Contains(q, "| json |") && strings.Contains(q, "level") {
			foundStructured = true
		}
	}
	if !foundStructured {
		t.Errorf("expected a structured LogQL query with '| json | level=~'; captured queries: %v", capturedQueries)
	}

	// 2. The default query must NOT use a bare substring match on "error".
	for _, q := range capturedQueries {
		if strings.Contains(q, `|~ "(?i)(error|warn`) {
			t.Errorf("default query must not use bare substring error|warn match; got: %q", q)
		}
	}

	// 3. WARN is excluded: the level filter must not include "warn".
	for _, q := range capturedQueries {
		if strings.Contains(q, "| json |") {
			if strings.Contains(strings.ToLower(q), "warn") {
				t.Errorf("structured query must exclude WARN; got: %q", q)
			}
		}
	}

	// 4. Exactly 1 log line is returned (the ERROR one) -- the INFO line with
	//    error_class in the payload was filtered server-side by the structured query.
	if len(out.Logs) != 1 {
		t.Errorf("expected 1 error-tier log line, got %d: %v", len(out.Logs), out.Logs)
	}
	if len(out.Logs) == 1 && out.Logs[0].Level != "error" {
		t.Errorf("returned log line level: want 'error', got %q", out.Logs[0].Level)
	}
}

// TestFetchLokiLogs_GoPanicSignaturesCaught verifies that Go runtime panic
// signatures (which emit raw text with no JSON level field) are caught by
// the narrow panic-signatures raw-regex query issued alongside the structured
// level query.
//
// Falsification: if the panic sub-query is removed, the mock panic-signatures
// server path never fires and the panic line never appears in out.Logs.
func TestFetchLokiLogs_GoPanicSignaturesCaught(t *testing.T) {
	lokiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")

		if strings.Contains(q, "panic:") {
			// Panic-signatures query: return a raw panic line.
			// lokiResponse encodes as JSON-in-string; use a raw Loki response instead.
			resp := `{"status":"success","data":{"resultType":"streams","result":[{"stream":{"container":"oxpulse-chat"},"values":[["2024-01-01T00:00:01Z","goroutine 42 [running]:\nruntime/debug.Stack()"]]}]}}`
			fmt.Fprint(w, resp)
		} else {
			// Structured query: no JSON-level error lines (all lines are panics).
			fmt.Fprint(w, `{"status":"success","data":{"resultType":"streams","result":[]}}`)
		}
	}))
	defer lokiSrv.Close()

	promSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/api/v1/label/__name__/values") {
			fmt.Fprint(w, `{"status":"success","data":[]}`)
		} else {
			fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
		}
	}))
	defer promSrv.Close()

	t.Setenv("DOZOR_PROMETHEUS_URL", promSrv.URL)
	t.Setenv("DOZOR_LOKI_URL", lokiSrv.URL)

	out, err := handleMetrics(t.Context(), MetricsInput{
		Service:     "oxpulse-chat",
		IncludeLogs: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The panic line must appear in the merged output.
	if len(out.Logs) == 0 {
		t.Error("expected at least one log line from panic-signatures query, got none")
	}

	// The panic query must NOT contain "| json |" (it's a raw-regex query).
	// Checked indirectly: the panic log line arrived because a non-structured
	// query was issued. At least one log entry should contain the goroutine text.
	found := false
	for _, l := range out.Logs {
		if strings.Contains(l.Snippet, "goroutine") || strings.Contains(l.Snippet, "running") {
			found = true
		}
	}
	if !found {
		t.Errorf("panic goroutine signature not found in logs: %v", out.Logs)
	}
}

// TestFetchLokiLogs_LogQueryOverride_RawRegex verifies that a caller-supplied
// log_query still works as a raw regex (single query, backward-compat path)
// and is NOT modified to use the structured level filter.
func TestFetchLokiLogs_LogQueryOverride_RawRegex(t *testing.T) {
	capturedQueries := make([]string, 0)

	lokiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedQueries = append(capturedQueries, r.URL.Query().Get("query"))
		lines := []struct {
			TS    string
			Level string
			Msg   string
		}{
			{"2024-01-01T00:00:01Z", "error", "custom match"},
		}
		fmt.Fprint(w, lokiResponse(lines))
	}))
	defer lokiSrv.Close()

	promSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/api/v1/label/__name__/values") {
			fmt.Fprint(w, `{"status":"success","data":[]}`)
		} else {
			fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
		}
	}))
	defer promSrv.Close()

	t.Setenv("DOZOR_PROMETHEUS_URL", promSrv.URL)
	t.Setenv("DOZOR_LOKI_URL", lokiSrv.URL)

	_, err := handleMetrics(t.Context(), MetricsInput{
		Service:     "oxpulse-chat",
		IncludeLogs: true,
		LogQuery:    "my_custom_pattern",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Only 1 query must have been issued (single raw-regex, no second query).
	if len(capturedQueries) != 1 {
		t.Errorf("log_query override must issue exactly 1 Loki request, got %d: %v", len(capturedQueries), capturedQueries)
	}
	// Must use |~ raw regex, NOT | json |.
	if !strings.Contains(capturedQueries[0], "my_custom_pattern") {
		t.Errorf("expected custom pattern in query, got: %q", capturedQueries[0])
	}
	if strings.Contains(capturedQueries[0], "| json |") {
		t.Errorf("log_query override must not use structured JSON filter, got: %q", capturedQueries[0])
	}
}

// ---- failures sweep tests (category="failures") ----------------------------

// promIncreaseResponse builds a Prometheus instant-query response for a
// counter that increase() returns (no __name__ label, as Prometheus omits it
// for aggregated expressions).
func promIncreaseResponse(value string) string {
	return fmt.Sprintf(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{"result":"fail"},"value":[1700000000,%q]}]}}`, value)
}

// alertmanagerResponse returns a minimal Alertmanager /api/v2/alerts JSON payload.
func alertmanagerResponse(alerts []struct {
	Name     string
	Severity string
	StartsAt string
}) string {
	items := make([]string, len(alerts))
	for i, a := range alerts {
		items[i] = fmt.Sprintf(`{"labels":{"alertname":%q,"severity":%q},"startsAt":%q,"endsAt":"0001-01-01T00:00:00Z","status":{"state":"active"}}`,
			a.Name, a.Severity, a.StartsAt)
	}
	return "[" + strings.Join(items, ",") + "]"
}

// lokiCountResponse builds a Loki instant vector response for a count_over_time sum query.
func lokiCountResponse(containers map[string]int64) string {
	results := make([]string, 0, len(containers))
	for c, count := range containers {
		results = append(results, fmt.Sprintf(`{"metric":{"container":%q},"value":[1700000000,"%d"]}`, c, count))
	}
	return fmt.Sprintf(`{"status":"success","data":{"resultType":"vector","result":[%s]}}`, strings.Join(results, ","))
}

// TestFailuresSweep_AllSections_Happy verifies that category="failures" with a
// fully-wired service returns all 4 sections populated and a DEGRADED verdict.
// RED test: revert the failures dispatch in handleMetrics and this will fail
// because out.Failures will be nil.
func TestFailuresSweep_AllSections_Happy(t *testing.T) {
	// Prom mock: label endpoint (unused for failures path), counter queries,
	// and gauge queries.
	capturedPromQueries := make([]string, 0)
	promSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/api/v1/label/__name__/values"):
			// Should NOT be called in the failures path (no metric discovery).
			fmt.Fprint(w, promLabelsResponse([]string{}))
		case strings.Contains(r.URL.Path, "/api/v1/query"):
			q, _ := url.QueryUnescape(r.URL.Query().Get("query"))
			capturedPromQueries = append(capturedPromQueries, q)

			// Return non-zero increase for failure counters.
			// Return 0 (== condition match) for healthy gauge queries.
			if strings.Contains(q, "== 0") {
				// Gauge == 0 query: return one result (gauge is down).
				fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{"__name__":"turn_server_transport_healthy","region":"us"},"value":[1700000000,"0"]}]}}`)
				return
			}
			// Counter increase queries: return 42 increase.
			fmt.Fprint(w, promIncreaseResponse("42"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer promSrv.Close()

	// Alertmanager mock.
	amSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/api/v2/alerts") {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, alertmanagerResponse([]struct {
			Name     string
			Severity string
			StartsAt string
		}{
			{"HighErrorRate", "critical", "2026-06-12T10:00:00Z"},
		}))
	}))
	defer amSrv.Close()

	// Loki mock: capture the count query, return count data.
	capturedLokiQueries := make([]string, 0)
	lokiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q, _ := url.QueryUnescape(r.URL.Query().Get("query"))
		capturedLokiQueries = append(capturedLokiQueries, q)
		fmt.Fprint(w, lokiCountResponse(map[string]int64{
			"oxpulse-chat": 37,
		}))
	}))
	defer lokiSrv.Close()

	t.Setenv("DOZOR_PROMETHEUS_URL", promSrv.URL)
	t.Setenv("DOZOR_LOKI_URL", lokiSrv.URL)
	t.Setenv("DOZOR_ALERTMANAGER_URL", amSrv.URL)

	out, err := handleMetrics(t.Context(), MetricsInput{
		Service:  "oxpulse-chat",
		Category: "failures",
		Range:    "24h",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Must return the Failures field, not raw Metrics.
	if out.Failures == nil {
		t.Fatal("category=failures: out.Failures must not be nil")
	}
	sweep := out.Failures

	// Section 1: fail counters present with delta > 0.
	if len(sweep.FailCounters) == 0 {
		t.Error("expected fail_counters to be populated")
	}
	for _, fc := range sweep.FailCounters {
		if fc.Delta <= 0 {
			t.Errorf("all fail counters should have delta > 0, got %d for %q", fc.Delta, fc.Name)
		}
	}

	// Section 2: gauges down.
	if len(sweep.GaugesDown) == 0 {
		t.Error("expected gauges_down to be populated (turn_server_transport_healthy == 0)")
	}

	// Section 3: firing alerts.
	if len(sweep.FiringAlerts) == 0 {
		t.Error("expected firing_alerts to be populated")
	}
	if sweep.FiringAlerts[0].AlertName != "HighErrorRate" {
		t.Errorf("alertname: want HighErrorRate, got %q", sweep.FiringAlerts[0].AlertName)
	}

	// Section 4: error log counts.
	if len(sweep.ErrorLogCounts) == 0 {
		t.Error("expected error_log_counts to be populated")
	}

	// The error-log COUNT query must filter by structured level, not a raw
	// "error" substring — else INFO telemetry carrying "error_class" in its JSON
	// payload is counted as a server error (the 564-false-positive bug PR #99
	// fixed in fetchLokiLogs but missed in this sweepErrorLogCounts path).
	foundLevelCount := false
	for _, q := range capturedLokiQueries {
		if strings.Contains(q, "count_over_time") && strings.Contains(q, "| json | level=~") {
			foundLevelCount = true
		}
		if strings.Contains(q, `|~ "(?i)(error|panic|fatal)"`) {
			t.Errorf("error-log count query still uses raw substring match: %q", q)
		}
	}
	if !foundLevelCount {
		t.Errorf("expected count_over_time with '| json | level=~' filter; captured: %v", capturedLokiQueries)
	}

	// Verdict must be DEGRADED.
	if !strings.HasPrefix(sweep.Verdict, "DEGRADED") {
		t.Errorf("verdict: want DEGRADED prefix, got %q", sweep.Verdict)
	}

	// Range must be echoed.
	if sweep.Range != "24h" {
		t.Errorf("range: want 24h, got %q", sweep.Range)
	}

	// The failures path must NOT have called the label-discovery endpoint.
	for _, q := range capturedPromQueries {
		if strings.Contains(q, "__name__") {
			t.Error("failures path must not call label-discovery endpoint")
		}
	}

	// out.Metrics must be empty (failures path skips metric discovery).
	if len(out.Metrics) != 0 {
		t.Errorf("failures path: out.Metrics should be empty, got %d", len(out.Metrics))
	}
}

// TestFailuresSweep_HealthyVerdict verifies the HEALTHY verdict when all
// sections are empty (no failures, no downed gauges, no alerts, no error logs).
// RED test: modify buildSweepVerdict to always return DEGRADED and this will fail.
func TestFailuresSweep_HealthyVerdict(t *testing.T) {
	promSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/api/v1/query") {
			// All counters return 0 increase; no gauges down.
			fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
			return
		}
		fmt.Fprint(w, `{"status":"success","data":[]}`)
	}))
	defer promSrv.Close()

	amSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "[]") // no alerts
	}))
	defer amSrv.Close()

	lokiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// count_over_time returns empty (no error logs).
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
	}))
	defer lokiSrv.Close()

	t.Setenv("DOZOR_PROMETHEUS_URL", promSrv.URL)
	t.Setenv("DOZOR_LOKI_URL", lokiSrv.URL)
	t.Setenv("DOZOR_ALERTMANAGER_URL", amSrv.URL)

	out, err := handleMetrics(t.Context(), MetricsInput{
		Service:  "oxpulse-chat",
		Category: "failures",
		Range:    "1h",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Failures == nil {
		t.Fatal("out.Failures must not be nil")
	}
	sweep := out.Failures
	if len(sweep.FailCounters) != 0 {
		t.Errorf("want 0 fail counters, got %d", len(sweep.FailCounters))
	}
	if len(sweep.GaugesDown) != 0 {
		t.Errorf("want 0 gauges down, got %d", len(sweep.GaugesDown))
	}
	if len(sweep.FiringAlerts) != 0 {
		t.Errorf("want 0 alerts, got %d", len(sweep.FiringAlerts))
	}
	if len(sweep.ErrorLogCounts) != 0 {
		t.Errorf("want 0 error log containers, got %d", len(sweep.ErrorLogCounts))
	}
	if !strings.HasPrefix(sweep.Verdict, "HEALTHY") {
		t.Errorf("verdict: want HEALTHY prefix, got %q", sweep.Verdict)
	}
}

// TestFailuresSweep_TopNTruncation verifies that sections exceeding sweepTopN
// are truncated and a "+N omitted" note appears in Truncated.
// RED test: remove the truncation logic and the Truncated field will be empty.
func TestFailuresSweep_TopNTruncation(t *testing.T) {
	// Return sweepTopN+5 items from the alertmanager to trigger truncation.
	const extraAlerts = 5
	totalAlerts := sweepTopN + extraAlerts

	amSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		alerts := make([]struct {
			Name     string
			Severity string
			StartsAt string
		}, totalAlerts)
		for i := range alerts {
			alerts[i] = struct {
				Name     string
				Severity string
				StartsAt string
			}{fmt.Sprintf("Alert%02d", i), "warning", "2026-06-12T10:00:00Z"}
		}
		fmt.Fprint(w, alertmanagerResponse(alerts))
	}))
	defer amSrv.Close()

	promSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
	}))
	defer promSrv.Close()

	lokiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
	}))
	defer lokiSrv.Close()

	t.Setenv("DOZOR_PROMETHEUS_URL", promSrv.URL)
	t.Setenv("DOZOR_LOKI_URL", lokiSrv.URL)
	t.Setenv("DOZOR_ALERTMANAGER_URL", amSrv.URL)

	out, err := handleMetrics(t.Context(), MetricsInput{
		Service:  "oxpulse-chat",
		Category: "failures",
		Range:    "24h",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Failures == nil {
		t.Fatal("out.Failures must not be nil")
	}
	sweep := out.Failures

	// Alerts must be capped at sweepTopN.
	if len(sweep.FiringAlerts) != sweepTopN {
		t.Errorf("firing_alerts: want %d (capped), got %d", sweepTopN, len(sweep.FiringAlerts))
	}
	// Truncated must mention the omitted count.
	foundTrunc := false
	for _, note := range sweep.Truncated {
		if strings.Contains(note, "firing_alerts") && strings.Contains(note, fmt.Sprintf("+%d omitted", extraAlerts)) {
			foundTrunc = true
		}
	}
	if !foundTrunc {
		t.Errorf("Truncated must mention firing_alerts +%d omitted; got: %v", extraAlerts, sweep.Truncated)
	}
}

// TestFailuresSweep_AlertmanagerDown_DegradesGracefully verifies that an
// unreachable Alertmanager does not fail the whole request -- metrics and
// log counts still return, and a warning is added.
func TestFailuresSweep_AlertmanagerDown_DegradesGracefully(t *testing.T) {
	amSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "alertmanager down", http.StatusServiceUnavailable)
	}))
	defer amSrv.Close()

	promSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
	}))
	defer promSrv.Close()

	lokiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
	}))
	defer lokiSrv.Close()

	t.Setenv("DOZOR_PROMETHEUS_URL", promSrv.URL)
	t.Setenv("DOZOR_LOKI_URL", lokiSrv.URL)
	t.Setenv("DOZOR_ALERTMANAGER_URL", amSrv.URL)

	out, err := handleMetrics(t.Context(), MetricsInput{
		Service:  "oxpulse-chat",
		Category: "failures",
		Range:    "1h",
	})
	if err != nil {
		t.Fatalf("expected graceful degradation, got error: %v", err)
	}
	if out.Failures == nil {
		t.Fatal("out.Failures must not be nil even when alertmanager is down")
	}
	// Must emit a warning mentioning alertmanager.
	foundWarn := false
	for _, w := range out.Warnings {
		if strings.Contains(strings.ToLower(w), "alertmanager") {
			foundWarn = true
		}
	}
	if !foundWarn {
		t.Errorf("expected alertmanager warning, got: %v", out.Warnings)
	}
}

// TestFailuresSweep_CountersSortedDesc verifies that fail_counters are returned
// sorted descending by delta so the highest-volume failure class appears first.
// RED test: remove sortFailCounters call and order will be non-deterministic.
func TestFailuresSweep_CountersSortedDesc(t *testing.T) {
	// Serve different delta values for different counter expressions.
	// oxpulse-chat has 7 failure_counters; we'll return different values
	// based on query content.
	counterValues := map[string]string{
		"client_ice_failed_total":              "100",
		"client_cold_hangup_total":             "50",
		"call_ice_to_connected_failures_total": "200",
		"ws_idle_timeout_total":                "10",
		"signaling_join_rejected_total":        "75",
		"analytics_events_total":               "30",
		"http_requests_total":                  "5",
	}

	promSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/api/v1/query") {
			q, _ := url.QueryUnescape(r.URL.Query().Get("query"))
			if strings.Contains(q, "== 0") {
				fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
				return
			}
			for name, val := range counterValues {
				if strings.Contains(q, name) {
					fmt.Fprint(w, promIncreaseResponse(val))
					return
				}
			}
		}
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
	}))
	defer promSrv.Close()

	amSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "[]")
	}))
	defer amSrv.Close()

	lokiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
	}))
	defer lokiSrv.Close()

	t.Setenv("DOZOR_PROMETHEUS_URL", promSrv.URL)
	t.Setenv("DOZOR_LOKI_URL", lokiSrv.URL)
	t.Setenv("DOZOR_ALERTMANAGER_URL", amSrv.URL)

	out, err := handleMetrics(t.Context(), MetricsInput{
		Service:  "oxpulse-chat",
		Category: "failures",
		Range:    "24h",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Failures == nil {
		t.Fatal("out.Failures must not be nil")
	}
	counters := out.Failures.FailCounters
	if len(counters) < 2 {
		t.Fatalf("expected multiple counters, got %d", len(counters))
	}
	// Verify descending order.
	isSorted := sort.SliceIsSorted(counters, func(i, j int) bool {
		return counters[i].Delta >= counters[j].Delta
	})
	if !isSorted {
		t.Errorf("fail_counters must be sorted desc by delta; got: %v", counters)
	}
	// Highest delta (200 for call_ice_to_connected_failures_total) must be first.
	if counters[0].Delta != 200 {
		t.Errorf("first counter delta: want 200, got %d (%s)", counters[0].Delta, counters[0].Name)
	}
}

// TestFailuresSweep_NonRegistryService_ReturnsEmptySections verifies that a
// service not in the registry returns an empty failures sweep without error.
// The counters/gauges sections are empty because there are no registry entries.
func TestFailuresSweep_NonRegistryService_ReturnsEmptySections(t *testing.T) {
	promSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
	}))
	defer promSrv.Close()

	amSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "[]")
	}))
	defer amSrv.Close()

	lokiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
	}))
	defer lokiSrv.Close()

	t.Setenv("DOZOR_PROMETHEUS_URL", promSrv.URL)
	t.Setenv("DOZOR_LOKI_URL", lokiSrv.URL)
	t.Setenv("DOZOR_ALERTMANAGER_URL", amSrv.URL)

	out, err := handleMetrics(t.Context(), MetricsInput{
		Service:  "unknown-svc",
		Category: "failures",
		Range:    "24h",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Failures == nil {
		t.Fatal("out.Failures must not be nil for unknown service")
	}
	// No counter or gauge data for unknown service -- but no error either.
	if len(out.Failures.FailCounters) != 0 {
		t.Errorf("unknown service: want 0 fail counters, got %d", len(out.Failures.FailCounters))
	}
}

// TestParseIntValue verifies the parseIntValue helper handles the common cases.
func TestParseIntValue(t *testing.T) {
	cases := []struct {
		input string
		want  int64
		ok    bool
	}{
		{"42", 42, true},
		{"42.7", 43, true}, // round-half-up
		{"0", 0, true},
		{"", 0, false},
		{"NaN", 0, false},
		{"+Inf", 0, false},
		{"-Inf", 0, false},
		{"abc", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			got, ok := parseIntValue(tc.input)
			if ok != tc.ok {
				t.Errorf("parseIntValue(%q): ok: want %v, got %v", tc.input, tc.ok, ok)
			}
			if ok && got != tc.want {
				t.Errorf("parseIntValue(%q): want %d, got %d", tc.input, tc.want, got)
			}
		})
	}
}

// TestParseLokiLogLine_LogfmtLevelKey verifies that logfmt lines from slog services
// (go-code, etc.) are classified by their explicit level= key, NOT by substring
// match on field values like error=false or error=true.
func TestParseLokiLogLine_LogfmtLevelKey(t *testing.T) {
	cases := []struct {
		name      string
		raw       string
		wantLevel string
	}{
		{
			name:      "logfmt_INFO_with_error=false",
			raw:       `time=2026-06-12T19:54:53.768Z level=INFO msg=tool_result tool=code_search duration=63ms error=false`,
			wantLevel: "info",
		},
		{
			name:      "logfmt_INFO_with_error=true",
			raw:       `time=2026-06-12T19:53:17.241Z level=INFO msg=tool_result tool=code_search duration=45ms error=true`,
			wantLevel: "info",
		},
		{
			name:      "logfmt_WARN",
			raw:       `time=2026-06-12T19:54:46.953Z level=WARN msg="go/packages load failed" err="exit status 1"`,
			wantLevel: "warn",
		},
		{
			name:      "no_level_key_with_error_word_falls_back_to_substring",
			raw:       `something went terribly wrong: error in processing`,
			wantLevel: "error",
		},
		{
			name:      "logfmt_DEBUG_no_false_positive",
			raw:       `time=2026-06-12T20:00:00Z level=DEBUG msg=heartbeat error=false`,
			wantLevel: "debug",
		},
		{
			name:      "logfmt_ERROR_explicit",
			raw:       `time=2026-06-12T20:01:00Z level=ERROR msg="connection refused" target=redis`,
			wantLevel: "error",
		},
		{
			name:      "logfmt_WARNING_alias",
			raw:       `time=2026-06-12T20:02:00Z level=WARNING msg="slow query" duration=5s`,
			wantLevel: "warning",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotLevel, _ := parseLokiLogLine(tc.raw)
			if gotLevel != tc.wantLevel {
				t.Errorf("parseLokiLogLine(%q): level = %q, want %q", tc.raw, gotLevel, tc.wantLevel)
			}
		})
	}
}
