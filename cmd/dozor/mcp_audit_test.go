package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
	h, err := mcpserver.Build(server, baseMCPConfig("", ""))
	if err != nil {
		t.Fatalf("Build: %v", err)
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
