package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"
	dockerclient "github.com/docker/docker/client"
)

// fakeContainerLogger implements containerLogger for testing.
type fakeContainerLogger struct {
	containers []container.Summary
	logsBody   string // raw (non-multiplexed) log content
	listErr    error
	logsErr    error
}

func (f *fakeContainerLogger) ContainerList(_ context.Context, _ container.ListOptions) ([]container.Summary, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.containers, nil
}

func (f *fakeContainerLogger) ContainerLogs(_ context.Context, _ string, _ container.LogsOptions) (io.ReadCloser, error) {
	if f.logsErr != nil {
		return nil, f.logsErr
	}
	// Build a minimal stdcopy-compatible stream (stdout frames) from logsBody.
	// Each line is wrapped in an 8-byte header: [1, 0,0,0, size(4 bytes BE)]
	var buf bytes.Buffer
	for _, line := range strings.Split(f.logsBody, "\n") {
		if line == "" {
			continue
		}
		payload := []byte(line + "\n")
		header := []byte{
			1, 0, 0, 0,
			byte(len(payload) >> 24),
			byte(len(payload) >> 16),
			byte(len(payload) >> 8),
			byte(len(payload)),
		}
		buf.Write(header)
		buf.Write(payload)
	}
	return io.NopCloser(bytes.NewReader(buf.Bytes())), nil
}

// Compile-time check: *dockerclient.Client satisfies containerLogger.
var _ containerLogger = (*dockerclient.Client)(nil)

// makeContainer builds a minimal container.Summary for tests.
func makeContainer(id, name, composeService string) container.Summary {
	return container.Summary{
		ID:    id,
		Names: []string{"/" + name},
		Labels: map[string]string{
			"com.docker.compose.service": composeService,
		},
	}
}

// logsBody helpers: Docker --timestamps format is "<rfc3339> <content>".
func tsLine(ts, content string) string {
	return ts + " " + content
}

const (
	fakeTS1 = "2026-05-08T10:00:01Z"
	fakeTS2 = "2026-05-08T10:00:02Z"
	fakeTS3 = "2026-05-08T10:00:03Z"
)

func TestLogsHandler_HappyPathJSONLines(t *testing.T) {
	jsonLine := `{"level":"error","msg":"connection refused","target":"db"}`
	fake := &fakeContainerLogger{
		containers: []container.Summary{makeContainer("abc123full", "oxpulse-chat", "oxpulse-chat")},
		logsBody:   tsLine(fakeTS1, jsonLine),
	}

	mx := http.NewServeMux()
	registerLogsHandler(mx, fake)
	srv := httptest.NewServer(mx)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/logs?service=oxpulse-chat")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var body logsResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Service != "oxpulse-chat" {
		t.Errorf("service: want oxpulse-chat, got %q", body.Service)
	}
	if body.ContainerID != "abc123full" {
		t.Errorf("container_id: want abc123full, got %q", body.ContainerID)
	}
	if len(body.Lines) != 1 {
		t.Fatalf("want 1 line, got %d", len(body.Lines))
	}
	ll := body.Lines[0]
	if ll.Level != "ERROR" {
		t.Errorf("level: want ERROR, got %q", ll.Level)
	}
	if ll.Msg != "connection refused" {
		t.Errorf("msg: want 'connection refused', got %q", ll.Msg)
	}
	if ll.Ts != fakeTS1 {
		t.Errorf("ts: want %q, got %q", fakeTS1, ll.Ts)
	}
}

func TestLogsHandler_FallbackRawLine(t *testing.T) {
	fake := &fakeContainerLogger{
		containers: []container.Summary{makeContainer("id1", "myservice", "myservice")},
		logsBody:   tsLine(fakeTS1, "not json at all ERROR happened"),
	}

	mx := http.NewServeMux()
	registerLogsHandler(mx, fake)
	srv := httptest.NewServer(mx)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/logs?service=myservice")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var body logsResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Lines) != 1 {
		t.Fatalf("want 1 raw line (matches default heuristic), got %d: %v", len(body.Lines), body.Lines)
	}
	if body.Lines[0].Level != "" {
		t.Errorf("raw line should have empty level, got %q", body.Lines[0].Level)
	}
	if !strings.Contains(body.Lines[0].Raw, "ERROR happened") {
		t.Errorf("raw should contain original text, got %q", body.Lines[0].Raw)
	}
}

func TestLogsHandler_GrepFilter(t *testing.T) {
	fake := &fakeContainerLogger{
		containers: []container.Summary{makeContainer("id1", "svc", "svc")},
		logsBody: strings.Join([]string{
			tsLine(fakeTS1, "normal info line"),
			tsLine(fakeTS2, `{"level":"info","msg":"database query ok"}`),
			tsLine(fakeTS3, `{"level":"error","msg":"timeout connecting to redis"}`),
		}, "\n"),
	}

	mx := http.NewServeMux()
	registerLogsHandler(mx, fake)
	srv := httptest.NewServer(mx)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/logs?service=svc&grep=redis")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var body logsResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Lines) != 1 {
		t.Fatalf("want 1 line matching 'redis', got %d", len(body.Lines))
	}
	if !strings.Contains(body.Lines[0].Raw, "redis") {
		t.Errorf("expected redis in raw line, got %q", body.Lines[0].Raw)
	}
}

func TestLogsHandler_DefaultGrepHeuristic(t *testing.T) {
	fake := &fakeContainerLogger{
		containers: []container.Summary{makeContainer("id1", "svc", "svc")},
		logsBody: strings.Join([]string{
			tsLine(fakeTS1, `{"level":"info","msg":"startup complete"}`),
			tsLine(fakeTS2, `{"level":"warn","msg":"retry attempt"}`),
			tsLine(fakeTS3, `{"level":"error","msg":"disk full"}`),
		}, "\n"),
	}

	mx := http.NewServeMux()
	registerLogsHandler(mx, fake)
	srv := httptest.NewServer(mx)
	defer srv.Close()

	// No grep param → default heuristic: keep ERROR and WARN.
	resp, err := http.Get(srv.URL + "/api/logs?service=svc")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var body logsResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Lines) != 2 {
		t.Fatalf("want 2 lines (WARN+ERROR), got %d: %v", len(body.Lines), body.Lines)
	}
}

func TestLogsHandler_LimitAndTruncate(t *testing.T) {
	var rawLines []string
	for i := range 10 {
		rawLines = append(rawLines, tsLine(fakeTS1, fmt.Sprintf(`{"level":"error","msg":"err %d"}`, i)))
	}
	fake := &fakeContainerLogger{
		containers: []container.Summary{makeContainer("id1", "svc", "svc")},
		logsBody:   strings.Join(rawLines, "\n"),
	}

	mx := http.NewServeMux()
	registerLogsHandler(mx, fake)
	srv := httptest.NewServer(mx)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/logs?service=svc&limit=3")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var body logsResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Lines) != 3 {
		t.Errorf("want 3 lines, got %d", len(body.Lines))
	}
	if !body.Truncated {
		t.Error("want truncated=true")
	}
}

func TestLogsHandler_ServiceNotFound_404(t *testing.T) {
	fake := &fakeContainerLogger{
		containers: []container.Summary{}, // no containers
	}

	mx := http.NewServeMux()
	registerLogsHandler(mx, fake)
	srv := httptest.NewServer(mx)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/logs?service=nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("want 404, got %d", resp.StatusCode)
	}
}

func TestLogsHandler_BadLimit_400(t *testing.T) {
	fake := &fakeContainerLogger{
		containers: []container.Summary{makeContainer("id1", "svc", "svc")},
	}

	mx := http.NewServeMux()
	registerLogsHandler(mx, fake)
	srv := httptest.NewServer(mx)
	defer srv.Close()

	cases := []struct {
		name  string
		limit string
	}{
		{"over cap", "1001"},
		{"zero", "0"},
		{"negative", "-5"},
		{"non-numeric", "abc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := http.Get(srv.URL + "/api/logs?service=svc&limit=" + tc.limit)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("limit=%q: want 400, got %d", tc.limit, resp.StatusCode)
			}
		})
	}
}

func TestLogsHandler_DockerUnavailable_502(t *testing.T) {
	fake := &fakeContainerLogger{
		listErr: fmt.Errorf("dial unix /var/run/docker.sock: connect: no such file"),
	}

	mx := http.NewServeMux()
	registerLogsHandler(mx, fake)
	srv := httptest.NewServer(mx)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/logs?service=svc")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("want 502, got %d", resp.StatusCode)
	}
}

func TestLogsHandler_MissingService_400(t *testing.T) {
	fake := &fakeContainerLogger{}

	mx := http.NewServeMux()
	registerLogsHandler(mx, fake)
	srv := httptest.NewServer(mx)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/logs")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("want 400, got %d", resp.StatusCode)
	}
}

func TestLogsHandler_NilClient_NotRegistered(t *testing.T) {
	// When cli is nil, registerLogsHandler must not panic and must not register the route.
	mx := http.NewServeMux()
	registerLogsHandler(mx, nil) // should no-op, just log warning

	srv := httptest.NewServer(mx)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/logs?service=svc")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	// Should 404 (mux has no /api/logs route).
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("want 404 (route not registered), got %d", resp.StatusCode)
	}
}
