package main

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/pkg/stdcopy"
)

const (
	logsDefaultLimit = 100
	logsMaxLimit     = 1000
)

// serviceNameRe validates that a service name contains only safe characters.
// Dots are intentionally excluded — compose service names use [a-zA-Z0-9_-].
var serviceNameRe = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// containerLogger is the subset of docker client used by the logs handler.
// *client.Client (from github.com/docker/docker/client) satisfies this interface. Extracted for testability.
type containerLogger interface {
	ContainerList(ctx context.Context, opts container.ListOptions) ([]container.Summary, error)
	ContainerLogs(ctx context.Context, containerID string, opts container.LogsOptions) (io.ReadCloser, error)
}

// LogLine is one parsed (or raw) log entry returned by /api/logs.
type LogLine struct {
	// Ts is the RFC3339 timestamp parsed from the line (empty if not found).
	Ts string `json:"ts,omitempty"`
	// Level is the log level parsed from structured JSON (empty for raw lines).
	Level string `json:"level,omitempty"`
	// Msg is the message field parsed from structured JSON (empty for raw lines).
	Msg string `json:"msg,omitempty"`
	// Raw is the full original line, always present.
	Raw string `json:"raw"`
}

// logsResponse is the JSON body returned by GET /api/logs.
type logsResponse struct {
	Service     string    `json:"service"`
	ContainerID string    `json:"container_id"`
	Lines       []LogLine `json:"lines"`
	Truncated   bool      `json:"truncated"`
}

// structuredLine matches the JSON shape emitted by slog / tracing-subscriber.
type structuredLine struct {
	// slog JSON handler emits "time", tracing-subscriber emits "timestamp".
	Time      string `json:"time"`
	Timestamp string `json:"timestamp"`
	Level     string `json:"level"`
	// slog emits "msg", tracing-subscriber emits "message".
	Msg     string `json:"msg"`
	Message string `json:"message"`
}

// defaultGrepRe matches lines that should be kept when no grep param is given.
// Anchors on level field in structured JSON, or falls back to substring on raw.
var defaultGrepRe = regexp.MustCompile(`(?i)(panic|fatal|error)`)

// registerLogsHandler mounts GET /api/logs on the given mux.
//
// Auth: if DOZOR_API_TOKEN is set in the environment, every request must
// present `Authorization: Bearer <token>`. When the env var is unset the
// endpoint is open (dev / same-network mode — suitable when dozor listens
// only on the internal docker network). A warning is logged once at
// registration time so operators see the gap in prod startup logs.
//
// Docker unavailability: if cli is nil (Docker not reachable at boot, or
// non-local config), the handler is not registered and a warning is logged,
// mirroring registerDeployWebhook's behaviour on missing config.
func registerLogsHandler(mx *http.ServeMux, cli containerLogger) {
	if cli == nil {
		slog.Warn("logs endpoint disabled: Docker client unavailable")
		return
	}

	apiToken := os.Getenv("DOZOR_API_TOKEN")
	if apiToken == "" {
		slog.Warn("logs endpoint running without auth; set DOZOR_API_TOKEN to restrict access")
	}

	mx.HandleFunc("GET /api/logs", func(w http.ResponseWriter, r *http.Request) {
		// Auth check — use constant-time compare to prevent timing oracle.
		if apiToken != "" {
			auth := r.Header.Get("Authorization")
			const prefix = "Bearer "
			if !strings.HasPrefix(auth, prefix) {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			provided := strings.TrimPrefix(auth, prefix)
			if subtle.ConstantTimeCompare([]byte(provided), []byte(apiToken)) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}

		q := r.URL.Query()

		// Parse service (required) and validate characters.
		service := q.Get("service")
		if service == "" {
			http.Error(w, `{"error":"service param required"}`, http.StatusBadRequest)
			return
		}
		if !serviceNameRe.MatchString(service) {
			http.Error(w, `{"error":"service name contains invalid characters"}`, http.StatusBadRequest)
			return
		}

		// Parse optional since/until (unix seconds).
		var sinceTS, untilTS int64
		var since, until string
		if s := q.Get("since"); s != "" {
			ts, err := strconv.ParseInt(s, 10, 64)
			if err != nil {
				http.Error(w, `{"error":"since must be unix timestamp"}`, http.StatusBadRequest)
				return
			}
			sinceTS = ts
			since = time.Unix(ts, 0).UTC().Format(time.RFC3339)
		}
		if u := q.Get("until"); u != "" {
			ts, err := strconv.ParseInt(u, 10, 64)
			if err != nil {
				http.Error(w, `{"error":"until must be unix timestamp"}`, http.StatusBadRequest)
				return
			}
			untilTS = ts
			until = time.Unix(ts, 0).UTC().Format(time.RFC3339)
		}

		// Validate time window ordering when both are present.
		if since != "" && until != "" && untilTS <= sinceTS {
			http.Error(w, `{"error":"until must be > since"}`, http.StatusBadRequest)
			return
		}

		// Parse grep.
		grep := q.Get("grep")

		// Parse limit.
		limit := logsDefaultLimit
		if l := q.Get("limit"); l != "" {
			n, err := strconv.Atoi(l)
			if err != nil || n <= 0 {
				http.Error(w, `{"error":"limit must be a positive integer"}`, http.StatusBadRequest)
				return
			}
			if n > logsMaxLimit {
				http.Error(w, `{"error":"limit exceeds maximum of 1000"}`, http.StatusBadRequest)
				return
			}
			limit = n
		}

		// Resolve service → container ID.
		containerID, err := resolveContainer(r.Context(), cli, service)
		if err != nil {
			slog.Warn("logs: container resolve failed",
				slog.String("service", service),
				slog.Any("error", err))
			http.Error(w, `{"error":"docker unreachable"}`, http.StatusBadGateway)
			return
		}
		if containerID == "" {
			http.Error(w, `{"error":"service container not found"}`, http.StatusNotFound)
			return
		}

		// Stream logs.
		lines, truncated, err := fetchAndFilterLogs(r.Context(), cli, containerID, since, until, grep, limit)
		if err != nil {
			slog.Error("logs: fetch failed",
				slog.String("service", service),
				slog.String("container", containerID),
				slog.Any("error", err))
			http.Error(w, `{"error":"failed to fetch logs"}`, http.StatusInternalServerError)
			return
		}

		resp := logsResponse{
			Service:     service,
			ContainerID: containerID,
			Lines:       lines,
			Truncated:   truncated,
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			slog.Warn("logs: json encode failed",
				slog.String("service", service),
				slog.Any("error", err))
		}
	})

	slog.Info("logs endpoint active", slog.String("path", "/api/logs"))
}

// resolveContainer maps a service name to a full container ID using Docker SDK.
// Matching priority:
//  1. com.docker.compose.service label == service
//  2. container name == service (with or without leading /)
//  3. container name contains service as substring
//  4. dozor.alias label (CSV) contains service — checked last across all running containers
//
// Priority 1-3 operate on the Docker name-filtered set (cheap).
// Priority 4 fetches all containers only when 1-3 yield no match (rare slow path).
//
// Returns ("", nil) when not found, ("", err) on docker error.
func resolveContainer(ctx context.Context, cli containerLogger, service string) (string, error) {
	f := filters.NewArgs()
	f.Add("name", service)
	containers, err := cli.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: f,
	})
	if err != nil {
		return "", err
	}

	for _, c := range containers {
		if c.Labels["com.docker.compose.service"] == service {
			return c.ID, nil
		}
	}
	for _, c := range containers {
		for _, n := range c.Names {
			clean := strings.TrimPrefix(n, "/")
			if clean == service {
				return c.ID, nil
			}
		}
	}
	// Substring fallback (filter already narrowed the set).
	for _, c := range containers {
		for _, n := range c.Names {
			if strings.Contains(strings.TrimPrefix(n, "/"), service) {
				return c.ID, nil
			}
		}
	}

	// Alias fallback: check dozor.alias label (CSV) across all containers.
	// This requires a second Docker call because the name filter above only
	// matches containers whose name contains the service string.
	all, err := cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return "", err
	}
	for _, c := range all {
		aliases := c.Labels["dozor.alias"]
		if aliases == "" {
			continue
		}
		for _, a := range strings.Split(aliases, ",") {
			if strings.TrimSpace(a) == service {
				return c.ID, nil
			}
		}
	}
	return "", nil
}

// fetchAndFilterLogs streams container logs, demuxes via io.Pipe+stdcopy,
// parses line-by-line with bufio.Scanner, filters, and limits. Memory usage
// is bounded to O(limit) regardless of total log volume.
func fetchAndFilterLogs(
	ctx context.Context,
	cli containerLogger,
	containerID string,
	since, until string,
	grep string,
	limit int,
) ([]LogLine, bool, error) {
	opts := container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Timestamps: true,
		// Soft pre-filter: fetch up to 10× limit so post-grep we have enough lines.
		Tail: strconv.Itoa(limit * 10),
	}
	if since != "" {
		opts.Since = since
	}
	if until != "" {
		opts.Until = until
	}

	rc, err := cli.ContainerLogs(ctx, containerID, opts)
	if err != nil {
		return nil, false, err
	}
	defer rc.Close()

	// Demux multiplexed stdout/stderr into a pipe so we can scan line-by-line
	// without buffering the entire response in memory.
	pr, pw := io.Pipe()
	go func() {
		_, copyErr := stdcopy.StdCopy(pw, pw, rc)
		pw.CloseWithError(copyErr)
	}()
	// Ensure the pipe reader is closed when we return so the goroutine above
	// gets ErrClosedPipe and exits promptly.
	defer pr.Close()

	scanner := bufio.NewScanner(pr)
	// Allow up to 64 KiB per line; longer lines are truncated and kept as raw.
	scanner.Buffer(make([]byte, 64<<10), 64<<10)

	var grepRe *regexp.Regexp
	if grep != "" {
		// User-supplied grep: compile case-insensitive substring.
		grepRe = regexp.MustCompile(`(?i)` + regexp.QuoteMeta(grep))
	}

	lines := make([]LogLine, 0, limit)
	truncated := false

	for scanner.Scan() {
		rawLine := strings.TrimSpace(scanner.Text())
		if rawLine == "" {
			continue
		}

		// Docker --timestamps prepends a RFC3339Nano timestamp to each line.
		// Strip it to get the actual log content for parsing.
		ts, logContent := splitDockerTimestamp(rawLine)

		ll := parseLine(logContent, ts, rawLine)

		// Apply filter.
		if !matchesFilter(ll, grep, grepRe) {
			continue
		}

		if len(lines) >= limit {
			truncated = true
			break
		}
		lines = append(lines, ll)
	}

	if err := scanner.Err(); err != nil && err != io.EOF && err != io.ErrClosedPipe {
		slog.Warn("logs: scan error", slog.String("container", containerID), slog.Any("error", err))
	}

	return lines, truncated, nil
}

// splitDockerTimestamp strips the leading RFC3339Nano timestamp that Docker
// adds when Timestamps:true. Returns (ts, rest). If no timestamp found,
// returns ("", rawLine).
func splitDockerTimestamp(line string) (ts, rest string) {
	// Docker format: "2006-01-02T15:04:05.000000000Z <content>"
	idx := strings.IndexByte(line, ' ')
	if idx <= 0 {
		return "", line
	}
	candidate := line[:idx]
	if _, err := time.Parse(time.RFC3339Nano, candidate); err == nil {
		return candidate, strings.TrimSpace(line[idx+1:])
	}
	return "", line
}

// parseLine tries JSON structured parse first, falls back to raw.
func parseLine(content, ts, raw string) LogLine {
	ll := LogLine{Ts: ts, Raw: raw}

	var s structuredLine
	if err := json.Unmarshal([]byte(content), &s); err == nil {
		ll.Level = normalizeLevel(s.Level)
		ll.Msg = firstNonEmpty(s.Msg, s.Message)
		if ll.Ts == "" {
			ll.Ts = firstNonEmpty(s.Time, s.Timestamp)
		}
	}
	return ll
}

// matchesFilter reports whether a LogLine passes the grep filter.
// When grep is empty the default heuristic is applied: keep ERROR/WARN/FATAL
// parsed levels, or raw lines matching (?i)(panic|fatal|error).
func matchesFilter(ll LogLine, grep string, grepRe *regexp.Regexp) bool {
	if grep != "" {
		return grepRe.MatchString(ll.Raw)
	}
	// Default heuristic.
	if ll.Level != "" {
		lvl := strings.ToUpper(ll.Level)
		return lvl == "ERROR" || lvl == "WARN" || lvl == "WARNING" || lvl == "FATAL"
	}
	return defaultGrepRe.MatchString(ll.Raw)
}

// normalizeLevel maps raw level strings to canonical uppercase.
func normalizeLevel(raw string) string {
	switch strings.ToUpper(raw) {
	case "ERROR", "ERR":
		return "ERROR"
	case "WARN", "WARNING":
		return "WARN"
	case "INFO":
		return "INFO"
	case "DEBUG":
		return "DEBUG"
	case "FATAL", "CRITICAL":
		return "FATAL"
	default:
		return strings.ToUpper(raw)
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
