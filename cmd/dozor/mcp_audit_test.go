package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/anatolykoptev/dozor/internal/identity"
	"github.com/anatolykoptev/dozor/internal/toolreg"
	"github.com/anatolykoptev/dozor/internal/tools"
	"github.com/anatolykoptev/go-mcpserver"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/prometheus/client_golang/prometheus"
)

const operatorToolCount = 27

func rpc(t *testing.T, h http.Handler, auth, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w.Code, w.Body.String()
}

func callCount(t *testing.T, id, tool, result string) float64 {
	t.Helper()
	fams, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fams {
		if f.GetName() != "dozor_mcp_calls_total" {
			continue
		}
		for _, m := range f.GetMetric() {
			got := map[string]string{}
			for _, l := range m.GetLabel() {
				got[l.GetName()] = l.GetValue()
			}
			if got["identity"] == id && got["tool"] == tool && got["result"] == result {
				return m.GetCounter().GetValue()
			}
		}
	}
	return 0
}

func operatorHandler(t *testing.T, server *mcp.Server) http.Handler {
	t.Helper()
	return operatorHandlerCfg(t, server, baseMCPConfig("", ""))
}

func operatorHandlerCfg(t *testing.T, server *mcp.Server, cfg mcpserver.Config) http.Handler {
	t.Helper()
	h, err := buildOperatorHandler(server, cfg)
	if err != nil {
		t.Fatalf("buildOperatorHandler: %v", err)
	}
	return h
}

// The master token on the operator listener is the identity "operator", and
// a call through the real server and real auth config is audited under it.
// RED-on-revert: drop the UserID stamp in operatorVerifier (mcp_auth.go) and
// the call is counted as "anonymous"; drop identity.Audit from buildMCPServer
// (setup.go) and nothing is counted.
func TestMCP_OperatorStampedAndAudited(t *testing.T) {
	t.Setenv(mcpTokenEnv, "master-secret")
	t.Setenv("DOZOR_MCP_ALLOW_INSECURE", "")
	eng := engine.NewAgent(engine.Config{Host: "audit-test.invalid", DuckDuckGoEnabled: true})
	defer eng.Close()
	h := operatorHandler(t, buildMCPServer(eng, tools.ExecOptions{}))

	before := callCount(t, identity.Operator, "server_exec_security", "ok")
	code, body := rpc(t, h, "Bearer master-secret",
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"server_exec_security","arguments":{"action":"get"}}}`)
	if code != http.StatusOK || !strings.Contains(body, "Current exec security mode") {
		t.Fatalf("tool call: status %d body %s", code, body)
	}
	if got := callCount(t, identity.Operator, "server_exec_security", "ok") - before; got != 1 {
		t.Errorf("dozor_mcp_calls_total{identity=operator} delta = %v, want 1", got)
	}
	if got := callCount(t, identity.Anonymous, "server_exec_security", "ok"); got != 0 {
		t.Errorf("call was counted as anonymous (%v): the operator stamp is missing", got)
	}
}

// The stamp must not change who is let in.
func TestOperatorVerifier_AcceptsExactlyTheMasterToken(t *testing.T) {
	v := operatorVerifier(mcpserver.StaticTokenVerifier("master-secret"))
	info, err := v(t.Context(), "master-secret", nil)
	if err != nil || info.UserID != identity.Operator {
		t.Fatalf("master token: info %+v err %v", info, err)
	}
	if _, err := v(t.Context(), "other", nil); err == nil {
		t.Error("wrong token accepted")
	}
	if _, err := operatorVerifier(mcpserver.StaticTokenVerifier(""))(t.Context(), "", nil); err == nil {
		t.Error("empty token accepted when none is configured")
	}
}

func listBody(t *testing.T, h http.Handler) (names []string, raw string) {
	t.Helper()
	code, body := rpc(t, h, "Bearer master-secret", `{"jsonrpc":"2.0","id":3,"method":"tools/list"}`)
	if code != http.StatusOK {
		t.Fatalf("tools/list: status %d body %s", code, body)
	}
	var out struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("tools/list body: %v\n%s", err, body)
	}
	for _, tl := range out.Result.Tools {
		names = append(names, tl.Name)
	}
	return names, body
}

// The audit layer must not change what the operator listener serves: the
// same 27 tools, byte for byte, as a server built without it.
func TestOperatorToolSurface_UnchangedByAudit(t *testing.T) {
	t.Setenv(mcpTokenEnv, "master-secret")
	t.Setenv("DOZOR_MCP_ALLOW_INSECURE", "")
	eng := engine.NewAgent(engine.Config{Host: "audit-test.invalid", DuckDuckGoEnabled: true})
	defer eng.Close()

	audited := buildMCPServer(eng, tools.ExecOptions{})
	buildExtensionRegistry(eng, toolreg.NewRegistry(), audited, false, nil)

	bare := mcpserver.NewServer(&mcp.Implementation{Name: "dozor", Version: version}, mcpserver.Config{SchemaCache: mcp.NewSchemaCache()})
	tools.RegisterAllWithOpts(bare, eng, tools.ExecOptions{})
	buildExtensionRegistry(eng, toolreg.NewRegistry(), bare, false, nil)

	names, got := listBody(t, operatorHandler(t, audited))
	if len(names) != operatorToolCount {
		t.Errorf("operator tools = %d, want %d: %v", len(names), operatorToolCount, names)
	}
	_, want := listBody(t, operatorHandler(t, bare))
	if got != want {
		t.Errorf("tools/list differs with the audit layer:\n got %s\nwant %s", got, want)
	}
}

func sampleSum(t *testing.T, name, id, tool string) (count uint64, sum float64) {
	t.Helper()
	fams, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fams {
		if f.GetName() != name {
			continue
		}
		for _, m := range f.GetMetric() {
			got := map[string]string{}
			for _, l := range m.GetLabel() {
				got[l.GetName()] = l.GetValue()
			}
			if got["identity"] == id && got["tool"] == tool {
				return m.GetHistogram().GetSampleCount(), m.GetHistogram().GetSampleSum()
			}
		}
	}
	return 0, 0
}

type slowIn struct{}
type slowOut struct{}

// A tool that outlives its timeout is audited once, when the caller got the
// timeout answer, with the duration the caller saw, not the tool's.
// RED-on-revert: register Audit before Build (inside the tool-timeout
// goroutine): nothing is recorded when the response arrives.
func TestAudit_ToolPastItsTimeoutIsAuditedWhenTheCallerSeesIt(t *testing.T) {
	t.Setenv(mcpTokenEnv, masterToken)
	t.Setenv("DOZOR_MCP_ALLOW_INSECURE", "")
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	srv := mcp.NewServer(&mcp.Implementation{Name: "slow", Version: "0"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "audit_slow_tool"}, func(context.Context, *mcp.CallToolRequest, slowIn) (*mcp.CallToolResult, slowOut, error) {
		<-release // ignores ctx on purpose
		return nil, slowOut{}, nil
	})
	cfg := baseMCPConfig("", "")
	cfg.ToolTimeout = 100 * time.Millisecond
	h := operatorHandlerCfg(t, srv, cfg)

	code, body := rpc(t, h, "Bearer "+masterToken,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"audit_slow_tool","arguments":{}}}`)
	if code != http.StatusOK || callSucceeded(t, body) {
		t.Fatalf("expected the timeout answer, got %d %s", code, body)
	}
	if got := callCount(t, identity.Operator, "audit_slow_tool", "error"); got != 1 {
		t.Fatalf("calls_total after the response = %v, want 1 (audited when the caller saw the answer)", got)
	}
	n, sum := sampleSum(t, "dozor_mcp_call_duration_seconds", identity.Operator, "audit_slow_tool")
	if n != 1 || sum < 0.09 || sum > 1.5 {
		t.Errorf("duration samples=%d sum=%vs, want one sample near the 100ms timeout", n, sum)
	}
}

// A panicking tool is audited, the caller gets an error result, and the
// process survives. RED-on-revert: remove identity.Recover from
// buildMCPServer and the panic ends this test binary (the tool runs in the
// tool-timeout goroutine, out of reach of any outer recover).
func TestAudit_PanickingToolIsAuditedAndTheProcessSurvives(t *testing.T) {
	t.Setenv(mcpTokenEnv, masterToken)
	t.Setenv("DOZOR_MCP_ALLOW_INSECURE", "")
	logs := captureLogs(t) // before the server is built: the middleware binds slog.Default() then
	eng := engine.NewAgent(engine.Config{Host: "audit-test.invalid"})
	defer eng.Close()
	srv := buildMCPServer(eng, tools.ExecOptions{})
	mcp.AddTool(srv, &mcp.Tool{Name: "audit_panic_tool"}, func(context.Context, *mcp.CallToolRequest, slowIn) (*mcp.CallToolResult, slowOut, error) {
		panic("boom with a caller secret: SECRET-ARG-123")
	})
	h := operatorHandler(t, srv)

	code, body := rpc(t, h, "Bearer "+masterToken,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"audit_panic_tool","arguments":{}}}`)
	if code != http.StatusOK || callSucceeded(t, body) {
		t.Fatalf("expected an error result, got %d %s", code, body)
	}
	if got := callCount(t, identity.Operator, "audit_panic_tool", "error"); got != 1 {
		t.Errorf("calls_total = %v, want 1", got)
	}
	out := logs.String()
	if !strings.Contains(out, "mcp handler panicked") || strings.Contains(out, "SECRET-ARG-123") || strings.Contains(body, "SECRET-ARG-123") {
		t.Errorf("panic must be logged by type only and not echoed:\nlog: %s\nbody: %s", out, body)
	}
	// And the server still serves.
	if code, _ := rpc(t, h, "Bearer "+masterToken, `{"jsonrpc":"2.0","id":3,"method":"tools/list"}`); code != http.StatusOK {
		t.Errorf("server unusable after a panic: %d", code)
	}
}

const masterToken = "master-secret"

// captureLogs routes slog.Default into a buffer for the test.
func captureLogs(t *testing.T) *syncBuf {
	t.Helper()
	b := &syncBuf{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(b, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return b
}

// callSucceeded reports whether a tools/call response is a non-error result.
func callSucceeded(t *testing.T, body string) bool {
	t.Helper()
	var out struct {
		Result *struct {
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct{} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("body: %v\n%s", err, body)
	}
	return out.Error == nil && out.Result != nil && !out.Result.IsError
}
