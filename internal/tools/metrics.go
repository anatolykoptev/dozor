package tools

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gopkg.in/yaml.v3"
)

//go:embed metrics_registry.yaml
var embeddedRegistryYAML []byte

// MetricsInput is the input schema for the `metrics` MCP tool.
type MetricsInput struct {
	Service       string `json:"service"`
	Category      string `json:"category,omitempty"`
	Filter        string `json:"filter,omitempty"`
	Range         string `json:"range,omitempty"`
	Format        string `json:"format,omitempty"`
	IncludeLogs   bool   `json:"include_logs,omitempty"`
	MaxResults    int    `json:"max_results,omitempty"`
	IncludeTraces bool   `json:"include_traces,omitempty"` // pull recent failed Jaeger traces for the service
	LogQuery      string `json:"log_query,omitempty"`      // regex override for Loki filter; default keeps "error|warn|panic|fail" behaviour
	TraceQuery    string `json:"trace_query,omitempty"`    // Jaeger tag filter, e.g. 'error=true' or 'http.status_code=500'
	TraceLimit    int    `json:"trace_limit,omitempty"`    // max traces returned, default 20, hard cap 100
}

// MetricsOutput is the output schema for the `metrics` MCP tool.
type MetricsOutput struct {
	Service    string         `json:"service"`
	Source     string         `json:"source"`
	BasisURL   string         `json:"basis_url"`
	Categories []string       `json:"categories"`
	Metrics    []MetricSample `json:"metrics"`
	Logs       []LogLine      `json:"logs,omitempty"`
	Traces     []TraceSummary `json:"traces,omitempty"` // only when include_traces=true
	Warnings   []string       `json:"warnings,omitempty"`
}

// TraceSummary holds a single Jaeger trace summary.
type TraceSummary struct {
	TraceID   string            `json:"trace_id"`
	Operation string            `json:"operation"`
	Duration  string            `json:"duration"`   // human-readable, e.g. "1.2s", "340ms"
	StartTime string            `json:"start_time"` // ISO 8601
	Spans     int               `json:"spans"`
	Tags      map[string]string `json:"tags,omitempty"` // top-level interesting tags (error, http.status_code, etc.)
	HasError  bool              `json:"has_error"`
}

// MetricSample holds a single Prometheus metric series.
type MetricSample struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels"`
	Value  string            `json:"value"`
	Type   string            `json:"type,omitempty"`
}

// LogLine holds one Loki log entry.
type LogLine struct {
	Timestamp string `json:"ts"`
	Level     string `json:"level"`
	Snippet   string `json:"msg"`
}

// metricsRegistry is the top-level structure of metrics_registry.yaml.
type metricsRegistry struct {
	Services map[string]serviceEntry `yaml:"services"`
}

type serviceEntry struct {
	PromJob       string              `yaml:"prom_job"`
	Container     string              `yaml:"container"`
	JaegerService string              `yaml:"jaeger_service"` // optional — defaults to service key
	Categories    map[string][]string `yaml:"categories"`
}

// loadMetricsRegistry loads the registry from a file path (if non-empty and
// the env DOZOR_METRICS_REGISTRY_PATH is set), falling back to the embedded
// YAML. The path parameter takes precedence over the env var.
func loadMetricsRegistry(overridePath string) (*metricsRegistry, error) {
	var data []byte
	path := overridePath
	if path == "" {
		path = os.Getenv("DOZOR_METRICS_REGISTRY_PATH")
	}
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read registry override %q: %w", path, err)
		}
		data = b
	} else {
		data = embeddedRegistryYAML
	}

	var reg metricsRegistry
	if err := yaml.Unmarshal(data, &reg); err != nil {
		return nil, fmt.Errorf("parse metrics registry: %w", err)
	}
	if reg.Services == nil {
		reg.Services = make(map[string]serviceEntry)
	}
	return &reg, nil
}

// registerMetrics wires the `metrics` MCP tool into the server.
func registerMetrics(server *mcp.Server, _ *engine.ServerAgent) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "metrics",
		Description: `Service-aware Prometheus metric snapshot with optional Loki log tail.

Registry-driven: name a service and optional category instead of writing raw PromQL.
Supported services: oxpulse-chat, go-code, memdb, dozor, go-search.
Categories per service — oxpulse-chat: calls, turn, sdk, build, sfu;
go-code: tools, embed, index, postgres; memdb: storage, api, embed;
dozor: deploy, queue, alerts, gateway, quotas; go-search: api, cache, llm.

Examples:
  {"service":"oxpulse-chat","category":"turn"}
  {"service":"dozor","category":"deploy","range":"5m"}
  {"service":"go-code","include_logs":true}
  {"service":"memdb","filter":"^memdb_storage_","format":"full"}`,
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input MetricsInput) (*mcp.CallToolResult, engine.TextOutput, error) {
		if strings.TrimSpace(input.Service) == "" {
			return nil, engine.TextOutput{}, errors.New("service is required")
		}
		out, err := handleMetrics(ctx, input)
		if err != nil {
			return nil, engine.TextOutput{}, err
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		return nil, engine.TextOutput{Text: string(b)}, nil
	})
}

// handleMetrics is the core logic, separated for testing.
func handleMetrics(ctx context.Context, input MetricsInput) (*MetricsOutput, error) {
	reg, err := loadMetricsRegistry("")
	if err != nil {
		return nil, fmt.Errorf("load registry: %w", err)
	}

	promURL := os.Getenv("DOZOR_PROMETHEUS_URL")
	if promURL == "" {
		promURL = "http://127.0.0.1:9090"
	}
	lokiURL := os.Getenv("DOZOR_LOKI_URL")
	if lokiURL == "" {
		lokiURL = "http://127.0.0.1:3100"
	}
	jaegerURL := os.Getenv("DOZOR_JAEGER_URL")
	if jaegerURL == "" {
		jaegerURL = "http://127.0.0.1:16686"
	}

	maxResults := input.MaxResults
	if maxResults <= 0 {
		maxResults = 100
	}

	format := input.Format
	if format == "" {
		format = "summary"
	}

	source := "prometheus"
	if input.IncludeLogs && input.IncludeTraces {
		source = "prometheus+loki+jaeger"
	} else if input.IncludeLogs {
		source = "prometheus+loki"
	} else if input.IncludeTraces {
		source = "prometheus+jaeger"
	}
	out := &MetricsOutput{
		Service:  input.Service,
		BasisURL: promURL,
		Source:   source,
	}

	// Resolve service from registry
	svcEntry, inRegistry := reg.Services[input.Service]
	var jobFilter string
	if inRegistry {
		jobFilter = svcEntry.PromJob
	} else {
		jobFilter = input.Service
		out.Warnings = append(out.Warnings,
			fmt.Sprintf("service %q not in registry — used generic {job=%q} filter", input.Service, input.Service))
	}

	// Resolve categories
	categoryPatterns := []string{}
	if inRegistry {
		cat := input.Category
		if cat == "" || cat == "all" {
			// union of all categories
			for catName, patterns := range svcEntry.Categories {
				categoryPatterns = append(categoryPatterns, patterns...)
				out.Categories = append(out.Categories, catName)
			}
		} else {
			patterns, ok := svcEntry.Categories[cat]
			if ok {
				categoryPatterns = append(categoryPatterns, patterns...)
				out.Categories = append(out.Categories, cat)
			} else {
				out.Warnings = append(out.Warnings,
					fmt.Sprintf("category %q not found for service %q", cat, input.Service))
			}
		}
	}

	// Build combined filter regex: category patterns OR explicit Filter arg
	filterParts := make([]string, 0, len(categoryPatterns)+1)
	filterParts = append(filterParts, categoryPatterns...)
	if input.Filter != "" {
		filterParts = append(filterParts, input.Filter)
	}

	var combinedRE *regexp.Regexp
	if len(filterParts) > 0 {
		combined := strings.Join(filterParts, "|")
		var compileErr error
		combinedRE, compileErr = regexp.Compile(combined)
		if compileErr != nil {
			return nil, fmt.Errorf("invalid filter regex: %w", compileErr)
		}
	}

	// Validate range
	if input.Range != "" {
		if _, parseErr := time.ParseDuration(input.Range); parseErr != nil {
			// Prometheus range format like "5m", "1h" — also try basic validation
			if !isValidPromRange(input.Range) {
				return nil, fmt.Errorf("invalid range %q: must be a Prometheus duration like 5m, 1h, 30s", input.Range)
			}
		}
	}

	// 1. Discover all metric names via /api/v1/label/__name__/values
	allNames, err := fetchMetricNames(ctx, promURL, jobFilter)
	if err != nil {
		return nil, err
	}

	// 2. Filter names
	filteredNames := filterNames(allNames, combinedRE, maxResults)

	// 3. Query each metric
	samples, queryWarnings := queryMetrics(ctx, promURL, filteredNames, jobFilter, input.Range, format)
	out.Warnings = append(out.Warnings, queryWarnings...)
	out.Metrics = samples

	if len(filteredNames) == 0 && combinedRE != nil {
		out.Warnings = append(out.Warnings, "no metrics matched")
	} else if len(filteredNames) == 0 && combinedRE == nil {
		out.Warnings = append(out.Warnings, "no metrics matched")
	}

	// 4. Optionally fetch Loki logs
	if input.IncludeLogs {
		container := input.Service
		if inRegistry && svcEntry.Container != "" {
			container = svcEntry.Container
		}
		logs, lokiWarn := fetchLokiLogs(ctx, lokiURL, container, input.LogQuery)
		if lokiWarn != "" {
			out.Warnings = append(out.Warnings, lokiWarn)
		}
		out.Logs = logs
	}

	// 5. Optionally fetch Jaeger traces
	if input.IncludeTraces {
		jaegerSvc := input.Service
		if inRegistry && svcEntry.JaegerService != "" {
			jaegerSvc = svcEntry.JaegerService
		}

		traceLimit := input.TraceLimit
		if traceLimit <= 0 {
			traceLimit = 20
		}
		if traceLimit > 100 {
			traceLimit = 100
		}

		traces, jaegerWarn := fetchJaegerTraces(ctx, jaegerURL, jaegerSvc, input.TraceQuery, traceLimit)
		if jaegerWarn != "" {
			out.Warnings = append(out.Warnings, jaegerWarn)
		}
		out.Traces = traces
	}

	return out, nil
}

// fetchMetricNames queries /api/v1/label/__name__/values with a job filter.
func fetchMetricNames(ctx context.Context, promURL, jobFilter string) ([]string, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	rawURL := fmt.Sprintf("%s/api/v1/label/__name__/values", promURL)
	if jobFilter != "" {
		rawURL += "?match[]=" + url.QueryEscape(fmt.Sprintf(`{job="%s"}`, jobFilter))
	}

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build prometheus label request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("prometheus unreachable at %s: %w", promURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return nil, fmt.Errorf("prometheus label fetch failed: %s %d: %s", promURL, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var result struct {
		Status string   `json:"status"`
		Data   []string `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode prometheus label response: %w", err)
	}
	return result.Data, nil
}

// filterNames applies the combined regex and caps at maxResults.
func filterNames(names []string, re *regexp.Regexp, maxResults int) []string {
	result := make([]string, 0, maxResults)
	for _, n := range names {
		if re != nil && !re.MatchString(n) {
			continue
		}
		result = append(result, n)
		if len(result) >= maxResults {
			break
		}
	}
	return result
}

// queryMetrics issues an instant or rate() query for each metric name.
func queryMetrics(ctx context.Context, promURL string, names []string, jobFilter, rangeStr, format string) ([]MetricSample, []string) {
	samples := make([]MetricSample, 0, len(names))
	var warnings []string

	for _, name := range names {
		query := buildQuery(name, jobFilter, rangeStr)

		reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		rawURL := fmt.Sprintf("%s/api/v1/query?query=%s", promURL, url.QueryEscape(query))
		req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, rawURL, nil)
		if err != nil {
			cancel()
			warnings = append(warnings, fmt.Sprintf("build query for %q: %v", name, err))
			continue
		}
		resp, err := http.DefaultClient.Do(req)
		cancel()
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("query %q failed: %v", name, err))
			continue
		}

		parsed, parseErr := parsePromQueryResponse(resp)
		resp.Body.Close()
		if parseErr != nil {
			warnings = append(warnings, fmt.Sprintf("parse %q response: %v", name, parseErr))
			continue
		}

		for _, s := range parsed {
			if format == "summary" && s.Value == "0" {
				continue
			}
			samples = append(samples, s)
		}
	}
	return samples, warnings
}

// buildQuery constructs the PromQL query for a single metric.
// Counters (ending in _total) with a range → rate(); everything else → instant.
func buildQuery(name, jobFilter, rangeStr string) string {
	jobSel := ""
	if jobFilter != "" {
		jobSel = fmt.Sprintf(`{job="%s"}`, jobFilter)
	}

	isCounter := strings.HasSuffix(name, "_total")
	if rangeStr != "" && isCounter {
		return fmt.Sprintf("rate(%s%s[%s])", name, jobSel, rangeStr)
	}
	return name + jobSel
}

// parsePromQueryResponse parses a Prometheus /api/v1/query JSON response
// into a slice of MetricSample. Drops __name__, job, instance from labels.
func parsePromQueryResponse(resp *http.Response) ([]MetricSample, error) {
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var result struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Metric map[string]string `json:"metric"`
				Value  []interface{}     `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}

	samples := make([]MetricSample, 0, len(result.Data.Result))
	for _, r := range result.Data.Result {
		name := r.Metric["__name__"]

		labels := make(map[string]string)
		for k, v := range r.Metric {
			switch k {
			case "__name__", "job", "instance":
				// drop for readability
			default:
				labels[k] = v
			}
		}

		var value string
		if len(r.Value) == 2 {
			switch v := r.Value[1].(type) {
			case string:
				value = v
			default:
				value = fmt.Sprintf("%v", v)
			}
		}

		samples = append(samples, MetricSample{
			Name:   name,
			Labels: labels,
			Value:  value,
		})
	}
	return samples, nil
}

// fetchLokiLogs queries Loki for recent WARN/ERROR lines for a container.
// If logQuery is non-empty it replaces the default regex filter.
// On failure, it returns a warning string instead of an error (graceful degradation).
func fetchLokiLogs(ctx context.Context, lokiURL, container, logQuery string) ([]LogLine, string) {
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	now := time.Now()
	start := now.Add(-30 * time.Minute)

	regex := logQuery
	if regex == "" {
		regex = "(?i)(error|warn|panic|turn_server_down|fail)"
	}
	logQL := fmt.Sprintf(`{container="%s"} |~ "%s"`, container, regex)
	rawURL := fmt.Sprintf("%s/loki/api/v1/query_range?query=%s&start=%d&end=%d&limit=25&direction=backward",
		lokiURL,
		url.QueryEscape(logQL),
		start.UnixNano(),
		now.UnixNano(),
	)

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Sprintf("loki request build failed: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Sprintf("loki unreachable at %s: %v", lokiURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return nil, fmt.Sprintf("loki returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	lines, parseErr := parseLokiResponse(resp.Body)
	if parseErr != nil {
		return nil, fmt.Sprintf("parse loki response: %v", parseErr)
	}
	return lines, ""
}

// parseLokiResponse decodes a Loki query_range response and extracts log lines.
func parseLokiResponse(r io.Reader) ([]LogLine, error) {
	var result struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Stream map[string]string `json:"stream"`
				Values [][]string        `json:"values"` // [timestamp_ns, log_line]
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(r).Decode(&result); err != nil {
		return nil, err
	}

	var lines []LogLine
	for _, stream := range result.Data.Result {
		for _, val := range stream.Values {
			if len(val) < 2 {
				continue
			}
			ts := val[0]
			raw := val[1]

			level, msg := parseLokiLogLine(raw)

			if len(msg) > 200 {
				msg = msg[:200]
			}

			lines = append(lines, LogLine{
				Timestamp: ts,
				Level:     level,
				Snippet:   msg,
			})
		}
	}
	return lines, nil
}

// parseLokiLogLine attempts to parse a JSON log line for level and msg fields.
// Falls back to ("raw", line) if parsing fails or fields are absent.
func parseLokiLogLine(raw string) (level, msg string) {
	// The test sends JSON as a string literal encoding of JSON
	// e.g. raw = `{"level":"error","msg":"something"}`
	// but the test lokiResponse helper encodes it as a JSON string,
	// so the value arriving here is already the decoded string.
	var obj map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &obj); err == nil {
		lvl, _ := obj["level"].(string)
		m, _ := obj["msg"].(string)
		if m == "" {
			m, _ = obj["message"].(string)
		}
		if lvl == "" {
			lvl = "raw"
		}
		if m == "" {
			m = raw
		}
		return lvl, m
	}
	// plain text: try to extract level keyword
	lower := strings.ToLower(raw)
	for _, candidate := range []string{"error", "warn", "warning", "info", "debug", "panic", "fatal"} {
		if strings.Contains(lower, candidate) {
			return candidate, raw
		}
	}
	return "raw", raw
}

// parseTraceQuery converts a comma-separated "key=value" string into a JSON
// tags map for Jaeger's tags query parameter. Empty input returns the default
// failed-traces filter {"error":"true"}.
func parseTraceQuery(q string) string {
	tags := map[string]string{"error": "true"}
	if q != "" {
		tags = make(map[string]string)
		for _, pair := range strings.Split(q, ",") {
			pair = strings.TrimSpace(pair)
			idx := strings.IndexByte(pair, '=')
			if idx < 0 {
				continue
			}
			tags[strings.TrimSpace(pair[:idx])] = strings.TrimSpace(pair[idx+1:])
		}
	}
	b, _ := json.Marshal(tags)
	return string(b)
}

// formatTraceDuration converts microseconds to a human-readable string.
func formatTraceDuration(microseconds int64) string {
	d := time.Duration(microseconds) * time.Microsecond
	if d >= time.Second {
		return fmt.Sprintf("%.2gs", d.Seconds())
	}
	return fmt.Sprintf("%dms", d.Milliseconds())
}

// fetchJaegerTraces queries the Jaeger HTTP API for recent traces of a service.
// On failure, it returns a warning string instead of an error (graceful degradation).
func fetchJaegerTraces(ctx context.Context, jaegerURL, service, traceQuery string, limit int) ([]TraceSummary, string) {
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	now := time.Now()
	start := now.Add(-30 * time.Minute)

	tagsJSON := parseTraceQuery(traceQuery)

	rawURL := fmt.Sprintf("%s/api/traces?service=%s&start=%d&end=%d&limit=%d&tags=%s",
		jaegerURL,
		url.QueryEscape(service),
		start.UnixMicro(),
		now.UnixMicro(),
		limit,
		url.QueryEscape(tagsJSON),
	)

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Sprintf("jaeger request build failed: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Sprintf("jaeger unreachable at %s: %v", jaegerURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return nil, fmt.Sprintf("jaeger returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	traces, parseErr := parseJaegerResponse(resp.Body)
	if parseErr != nil {
		return nil, fmt.Sprintf("parse jaeger response: %v", parseErr)
	}
	return traces, ""
}

// jaegerTag represents a single span tag in Jaeger's JSON format.
type jaegerTag struct {
	Key   string      `json:"key"`
	Type  string      `json:"type"`
	Value interface{} `json:"value"`
}

// parseJaegerResponse decodes Jaeger /api/traces JSON and extracts TraceSummary entries.
func parseJaegerResponse(r io.Reader) ([]TraceSummary, error) {
	var result struct {
		Data []struct {
			TraceID string `json:"traceID"`
			Spans   []struct {
				OperationName string      `json:"operationName"`
				Duration      int64       `json:"duration"`  // microseconds
				StartTime     int64       `json:"startTime"` // unix microseconds
				Tags          []jaegerTag `json:"tags"`
				References    []struct {
					RefType string `json:"refType"`
				} `json:"references"`
			} `json:"spans"`
		} `json:"data"`
	}
	if err := json.NewDecoder(r).Decode(&result); err != nil {
		return nil, err
	}

	summaries := make([]TraceSummary, 0, len(result.Data))
	for _, trace := range result.Data {
		if len(trace.Spans) == 0 {
			continue
		}

		// Root span: first span with no CHILD_OF reference (or the first span if all have refs)
		rootSpan := trace.Spans[0]
		for _, span := range trace.Spans {
			isRoot := true
			for _, ref := range span.References {
				if ref.RefType == "CHILD_OF" {
					isRoot = false
					break
				}
			}
			if isRoot {
				rootSpan = span
				break
			}
		}

		// Collect interesting tags from all spans, detect errors
		hasError := false
		topTags := make(map[string]string)
		interestingKeys := map[string]bool{
			"error":            true,
			"http.status_code": true,
			"http.method":      true,
			"http.url":         true,
			"db.type":          true,
			"peer.service":     true,
		}

		for _, span := range trace.Spans {
			for _, tag := range span.Tags {
				valStr := fmt.Sprintf("%v", tag.Value)
				if tag.Key == "error" && (valStr == "true" || valStr == "True" || valStr == "1") {
					hasError = true
				}
				if interestingKeys[tag.Key] {
					topTags[tag.Key] = valStr
				}
			}
		}

		// Also treat http.status_code >= 500 as error
		if sc, ok := topTags["http.status_code"]; ok {
			if len(sc) == 3 && sc[0] == '5' {
				hasError = true
			}
		}

		startISO := time.UnixMicro(rootSpan.StartTime).UTC().Format(time.RFC3339)

		summaries = append(summaries, TraceSummary{
			TraceID:   trace.TraceID,
			Operation: rootSpan.OperationName,
			Duration:  formatTraceDuration(rootSpan.Duration),
			StartTime: startISO,
			Spans:     len(trace.Spans),
			Tags:      topTags,
			HasError:  hasError,
		})
	}
	return summaries, nil
}

// isValidPromRange checks if s looks like a valid Prometheus range duration
// such as "5m", "1h", "30s", "2d".
func isValidPromRange(s string) bool {
	if len(s) < 2 {
		return false
	}
	unit := s[len(s)-1]
	switch unit {
	case 's', 'm', 'h', 'd', 'w', 'y':
	default:
		return false
	}
	digits := s[:len(s)-1]
	for _, c := range digits {
		if c < '0' || c > '9' {
			return false
		}
	}
	return len(digits) > 0
}
