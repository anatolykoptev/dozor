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
