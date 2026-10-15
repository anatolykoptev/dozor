package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/anatolykoptev/dozor/internal/agentproto"
	"github.com/anatolykoptev/dozor/internal/agentsurface"
	"github.com/anatolykoptev/dozor/internal/identity"
	"github.com/anatolykoptev/go-mcpserver/keyfile"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

const masterToken = "master-secret"

// metricValue reads one counter or gauge sample by its exact label set.
func metricValue(t *testing.T, name string, want map[string]string) float64 {
	t.Helper()
	fams, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fams {
		if f.GetName() != name {
			continue
		}
	next:
		for _, m := range f.GetMetric() {
			got := map[string]string{}
			for _, l := range m.GetLabel() {
				got[l.GetName()] = l.GetValue()
			}
			for k, v := range want {
				if got[k] != v {
					continue next
				}
			}
			switch {
			case m.GetCounter() != nil:
				return m.GetCounter().GetValue()
			case m.GetGauge() != nil:
				return m.GetGauge().GetValue()
			}
		}
	}
	return -1
}

func gaugeNow(t *testing.T, g prometheus.Gauge) float64 {
	t.Helper()
	var m dto.Metric
	if err := g.Write(&m); err != nil {
		t.Fatal(err)
	}
	return m.GetGauge().GetValue()
}

// captureLogs routes slog.Default into a buffer for the test.
func captureLogs(t *testing.T) *syncBuf {
	t.Helper()
	b := &syncBuf{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(b, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return b
}

type testKey struct {
	name, runAs string
	tools       []string
	token       string
	hash        string
}

func newTestKey(t *testing.T, name, runAs string, tools ...string) testKey {
	t.Helper()
	token, hash, err := keyfile.Mint(identity.TokenPrefix)
	if err != nil {
		t.Fatal(err)
	}
	return testKey{name: name, runAs: runAs, tools: tools, token: token, hash: hash}
}

func (k testKey) entry() string {
	return renderEntry(k.name, k.runAs, k.hash, time.Now().UTC().Add(30*24*time.Hour), k.tools)
}

// writeKeys atomically replaces the key file (tmp 0600 + rename), as the
// runbook tells the operator to.
func writeKeys(t *testing.T, path string, keys ...testKey) {
	t.Helper()
	var b strings.Builder
	b.WriteString("version: 1\nkeys:\n")
	for _, k := range keys {
		b.WriteString(k.entry())
	}
	b.WriteString("end: true\n")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "id-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// fakeWorker serves dir/<id>.sock; serve gets the connection after the
// request has been read.
func fakeWorker(t *testing.T, dir, id string, serve func(c net.Conn)) {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", filepath.Join(dir, id+".sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
				_, _ = io.ReadAll(c)
				serve(c)
			}()
		}
	}()
}

func statusReply(build string) func(net.Conn) {
	return func(c net.Conn) {
		_, _ = fmt.Fprintf(c, `{"v":%d,"build":%q,"status":{}}`, agentproto.ProtoVersion, build)
	}
}

type idRig struct {
	h       http.Handler
	kf      *keyfile.Store
	keyPath string
	sockDir string
}

func newIDRig(t *testing.T, keys ...testKey) *idRig {
	t.Helper()
	dir := shortDir(t)
	keyPath := filepath.Join(dir, "mcp-keys.yaml")
	writeKeys(t, keyPath, keys...)
	kf, err := keyfile.Open(identity.KeyfileOptions(keyPath, slog.Default()))
	if err != nil {
		t.Fatal(err)
	}
	sockDir := shortDir(t)
	h, err := buildIdentityHandler(kf, agentsurface.Config{SocketDir: sockDir, ExpectedPeerUID: uint32(os.Getuid()), DialTimeout: time.Second}) //nolint:gosec // test
	if err != nil {
		t.Fatal(err)
	}
	return &idRig{h: h, kf: kf, keyPath: keyPath, sockDir: sockDir}
}

func (r *idRig) call(t *testing.T, token, body string) (int, string) {
	t.Helper()
	auth := ""
	if token != "" {
		auth = "Bearer " + token
	}
	return rpc(t, r.h, auth, body)
}

const (
	listBodyJSON = `{"jsonrpc":"2.0","id":3,"method":"tools/list"}`
	statusCall   = `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"agent_status","arguments":{}}}`
)

func toolNamesFrom(t *testing.T, body string) []string {
	t.Helper()
	var out struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("body: %v\n%s", err, body)
	}
	var names []string
	for _, tl := range out.Result.Tools {
		names = append(names, tl.Name)
	}
	slices.Sort(names)
	return names
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

// A key lists exactly the agent tools it was granted; the operator tools do
// not exist on this server, so calling one is an error, not a denial.
func TestIdentityListener_ToolSurface(t *testing.T) {
	k := newTestKey(t, "probe", "probe-agent", "agent_status", "agent_exec")
	r := newIDRig(t, k)

	code, body := r.call(t, k.token, listBodyJSON)
	if code != http.StatusOK {
		t.Fatalf("tools/list: %d %s", code, body)
	}
	if got, want := toolNamesFrom(t, body), []string{"agent_exec", "agent_status"}; !slices.Equal(got, want) {
		t.Errorf("tools = %v, want %v", got, want)
	}
	for _, tool := range []string{"server_exec", "server_deploy", "agent_read_file"} {
		_, body := r.call(t, k.token, fmt.Sprintf(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":%q,"arguments":{}}}`, tool))
		if callSucceeded(t, body) {
			t.Errorf("%s succeeded on the identity listener: %s", tool, body)
		}
	}
}

// A key entry naming an operator tool is dropped by the key file itself.
func TestIdentityListener_OperatorToolInEntryDropsKey(t *testing.T) {
	k := newTestKey(t, "greedy", "greedy-agent", "agent_status", "server_exec")
	r := newIDRig(t, k)
	if code, _ := r.call(t, k.token, listBodyJSON); code != http.StatusUnauthorized {
		t.Errorf("key naming server_exec: status %d, want 401", code)
	}
}

// The identity flows from the verified key to the worker, the call is
// audited under the key's name, and a worker built differently raises the
// version-mismatch gauge.
func TestIdentityListener_CallReachesWorkerAndIsAudited(t *testing.T) {
	k := newTestKey(t, "probe", "probe-agent", "agent_status")
	r := newIDRig(t, k)
	fakeWorker(t, r.sockDir, "probe-agent", statusReply("some-other-build"))

	before := callCount(t, "probe", "agent_status", "ok")
	code, body := r.call(t, k.token, statusCall)
	if code != http.StatusOK || !callSucceeded(t, body) {
		t.Fatalf("agent_status: %d %s", code, body)
	}
	if got := callCount(t, "probe", "agent_status", "ok") - before; got != 1 {
		t.Errorf("audit delta = %v, want 1", got)
	}
	if got := metricValue(t, "dozor_agent_worker_version_mismatch", map[string]string{"identity": "probe-agent"}); got != 1 {
		t.Errorf("version_mismatch = %v, want 1", got)
	}
}

// A worker that closes without a reply reads as busy; it is counted by
// reason, logged as no-reply, and audited as busy (issue #281 item 6).
func TestIdentityListener_NoReplyIsCountedAndWarned(t *testing.T) {
	logs := captureLogs(t)
	k := newTestKey(t, "crashy", "crashy-agent", "agent_status")
	r := newIDRig(t, k)
	fakeWorker(t, r.sockDir, "crashy-agent", func(net.Conn) {})

	_, body := r.call(t, k.token, statusCall)
	if callSucceeded(t, body) {
		t.Fatalf("a worker with no reply produced a result: %s", body)
	}
	if got := metricValue(t, "dozor_agent_rejects_total", map[string]string{"identity": "crashy-agent", "reason": "backlog"}); got != 1 {
		t.Errorf("rejects_total{backlog} = %v, want 1", got)
	}
	if got := callCount(t, "crashy", "agent_status", "busy"); got != 1 {
		t.Errorf("audit result busy = %v, want 1", got)
	}
	if out := logs.String(); !strings.Contains(out, "reason=no-reply") || !strings.Contains(out, "level=WARN") {
		t.Errorf("no no-reply warning in the log:\n%s", out)
	}
}

// RED-on-revert: compose the master verifier into buildIdentityHandler and
// the master token (or MCP_TOKEN_DOZOR) is accepted here.
func TestIdentityListener_MasterTokenRejected(t *testing.T) {
	t.Setenv(mcpTokenEnv, masterToken)
	t.Setenv("MCP_TOKEN_DOZOR", "gateway-injected-token")
	k := newTestKey(t, "probe", "probe-agent", "agent_status")
	r := newIDRig(t, k)
	for _, tok := range []string{masterToken, "gateway-injected-token", "dzk1_malformed", ""} {
		if code, _ := r.call(t, tok, listBodyJSON); code != http.StatusUnauthorized {
			t.Errorf("token %q: status %d, want 401", tok[:min(len(tok), 6)], code)
		}
	}
	if code, _ := r.call(t, k.token, listBodyJSON); code != http.StatusOK {
		t.Errorf("the key itself: status %d, want 200", code)
	}
}

// RED-on-revert: compose the key-file verifier into the operator verifier
// (mcp_auth.go) and a key is accepted on the operator listener. The key file
// env var is set so such a composition has a store to read.
func TestMCP_KeyfileKeyRejectedOn8765(t *testing.T) {
	k := newTestKey(t, "probe", "probe-agent", "agent_status")
	r := newIDRig(t, k)
	t.Setenv(keysFileEnv, r.keyPath)
	t.Setenv(identityPortEnv, "8766")
	t.Setenv(mcpTokenEnv, masterToken)
	t.Setenv("DOZOR_MCP_ALLOW_INSECURE", "")

	op := operatorHandler(t, mcp.NewServer(&mcp.Implementation{Name: "op-test", Version: "0"}, nil))
	if code := postInit(op, "Bearer "+k.token); code != http.StatusUnauthorized {
		t.Errorf("key-file key on the operator listener: status %d, want 401", code)
	}
	if code := postInit(op, "Bearer "+masterToken); code != http.StatusOK {
		t.Errorf("master token on the operator listener: status %d, want 200", code)
	}
}

// Revoking a key by replacing the file takes effect without a restart, in
// time. The same handler (the same process) serves both sides of the change.
func TestIdentityListener_RevokeWithinTwoSeconds(t *testing.T) {
	keep := newTestKey(t, "keeper", "keeper-agent", "agent_status")
	gone := newTestKey(t, "goner", "goner-agent", "agent_status")
	r := newIDRig(t, keep, gone)

	for _, k := range []testKey{keep, gone} {
		if code, _ := r.call(t, k.token, listBodyJSON); code != http.StatusOK {
			t.Fatalf("%s before revoke: %d", k.name, code)
		}
	}
	start := time.Now()
	writeKeys(t, r.keyPath, keep) // the mv
	for {
		code, _ := r.call(t, gone.token, listBodyJSON)
		if code == http.StatusUnauthorized {
			break
		}
		if time.Since(start) > 2*time.Second {
			t.Fatalf("revoked key still accepted %s after the file changed", time.Since(start))
		}
		time.Sleep(50 * time.Millisecond)
	}
	if code, _ := r.call(t, keep.token, listBodyJSON); code != http.StatusOK {
		t.Errorf("the surviving key stopped working: %d", code)
	}
}

// No token, token fragment or token hash reaches a log line, whatever the
// credential: valid key, revoked key, malformed, unknown, or the master token.
func TestIdentityListener_NoTokenInLogs(t *testing.T) {
	logs := captureLogs(t)
	t.Setenv(mcpTokenEnv, masterToken)
	k := newTestKey(t, "probe", "probe-agent", "agent_status")
	other := newTestKey(t, "other", "other-agent", "agent_status") // not in the file
	r := newIDRig(t, k)
	fakeWorker(t, r.sockDir, "probe-agent", statusReply(agentproto.BuildID))

	r.call(t, k.token, statusCall)
	r.call(t, k.token, `{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"agent_status","arguments":{"secret":"`+k.token+`"}}}`)
	r.call(t, other.token, statusCall)
	r.call(t, masterToken, statusCall)
	r.call(t, "dzk1_not-a-real-token", statusCall)
	writeKeys(t, r.keyPath) // revoke everything
	time.Sleep(1100 * time.Millisecond)
	r.call(t, k.token, statusCall)

	out := logs.String()
	if out == "" {
		t.Fatal("captured no logs; the check proves nothing")
	}
	for _, secret := range []string{"dzk1_", k.token, k.hash, other.token, masterToken} {
		if strings.Contains(out, secret) {
			t.Errorf("log contains %q:\n%s", secret[:min(len(secret), 8)], out)
		}
	}
}

func opServer(t *testing.T, names ...string) *mcp.Server {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "op-test", Version: "0"}, nil)
	type in struct{}
	type out struct{}
	for _, n := range names {
		mcp.AddTool(srv, &mcp.Tool{Name: n}, func(context.Context, *mcp.CallToolRequest, in) (*mcp.CallToolResult, out, error) {
			return nil, out{}, nil
		})
	}
	return srv
}

func idConfig(t *testing.T, port string, keys ...testKey) identityConfig {
	t.Helper()
	path := filepath.Join(shortDir(t), "mcp-keys.yaml")
	writeKeys(t, path, keys...)
	return identityConfig{port: port, keysFile: path}
}

func testClientCfg(t *testing.T) agentsurface.Config {
	return agentsurface.Config{SocketDir: shortDir(t), ExpectedPeerUID: uint32(os.Getuid())} //nolint:gosec // test
}

// The listener binds 127.0.0.1 and nothing else.
// RED-on-revert: identityBindHost "127.0.0.1" -> "0.0.0.0".
func TestIdentityListener_LoopbackOnly(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	k := newTestKey(t, "probe", "probe-agent", "agent_status")
	addr, err := startIdentityListener(ctx, idConfig(t, "0", k), opServer(t, "server_probe"), "8765", testClientCfg(t))
	if err != nil {
		t.Fatal(err)
	}
	tcp, ok := addr.(*net.TCPAddr)
	if !ok || !tcp.IP.Equal(net.IPv4(127, 0, 0, 1)) {
		t.Fatalf("bound %v, want exactly 127.0.0.1", addr)
	}
	if got := identityListenAddr("8766"); got != "127.0.0.1:8766" {
		t.Errorf("identityListenAddr = %q", got)
	}
	if gaugeNow(t, identity.ListenerUp) != 1 {
		t.Error("dozor_identity_listener_up != 1 while serving")
	}
	// And it really serves: a key lists its tool over a real socket.
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+addr.String()+"/mcp", strings.NewReader(listBodyJSON))
	req.Header.Set("Authorization", "Bearer "+k.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !slices.Equal(toolNamesFrom(t, string(body)), []string{"agent_status"}) {
		t.Errorf("over the socket: %d %s", resp.StatusCode, body)
	}
	cancel()
	deadline := time.Now().Add(3 * time.Second)
	for gaugeNow(t, identity.ListenerUp) != 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if gaugeNow(t, identity.ListenerUp) != 0 {
		t.Error("listener_up stayed 1 after shutdown")
	}
}

// A bind error is logged and reported, and nothing else changes: the
// operator side keeps serving and the process is still here to check.
// RED-on-revert: replace the error branch of startAuxHTTPServer with
// os.Exit(1) and this test binary dies.
func TestAuxServer_ListenErrorNonFatal(t *testing.T) {
	logs := captureLogs(t)
	occupied, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	_, port, _ := net.SplitHostPort(occupied.Addr().String())

	operator := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "operator ok") }))
	defer operator.Close()

	k := newTestKey(t, "probe", "probe-agent", "agent_status")
	addr, err := startIdentityListener(t.Context(), idConfig(t, port, k), opServer(t, "server_probe"), "8765", testClientCfg(t))
	if err == nil || addr != nil {
		t.Fatalf("expected a bind error, got addr=%v err=%v", addr, err)
	}
	if gaugeNow(t, identity.ListenerUp) != 0 {
		t.Error("dozor_identity_listener_up != 0 after a failed bind")
	}
	if !strings.Contains(logs.String(), "level=ERROR") {
		t.Errorf("bind failure not logged at ERROR:\n%s", logs.String())
	}
	resp, err := http.Get(operator.URL) //nolint:noctx // test
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "operator ok" {
		t.Errorf("operator side changed: %q", b)
	}
}

// Setup problems of every kind end the same way: error, up 0, no panic.
func TestIdentityListener_SetupFailuresAreNonFatal(t *testing.T) {
	k := newTestKey(t, "probe", "probe-agent", "agent_status")
	cases := map[string]struct {
		cfg  func() identityConfig
		op   *mcp.Server
		port string
	}{
		"bad port":         {func() identityConfig { return idConfig(t, "notaport", k) }, opServer(t), "8765"},
		"operator's port":  {func() identityConfig { return idConfig(t, "8765", k) }, opServer(t), "8765"},
		"nil operator":     {func() identityConfig { return idConfig(t, "0", k) }, nil, "8765"},
		"overlapping tool": {func() identityConfig { return idConfig(t, "0", k) }, opServer(t, "agent_exec"), "8765"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			identity.ListenerUp.Set(1)
			addr, err := startIdentityListener(t.Context(), c.cfg(), c.op, c.port, testClientCfg(t))
			if err == nil || addr != nil {
				t.Fatalf("got addr=%v err=%v, want an error", addr, err)
			}
			if gaugeNow(t, identity.ListenerUp) != 0 {
				t.Error("listener_up != 0")
			}
		})
	}
}

func TestAssertDisjoint(t *testing.T) {
	if err := assertDisjoint(agentsurface.ToolNames, []string{"server_exec", "metric-pull"}); err != nil {
		t.Errorf("disjoint sets refused: %v", err)
	}
	if err := assertDisjoint(agentsurface.ToolNames, []string{"server_exec", "agent_status"}); err == nil {
		t.Error("overlapping sets accepted")
	}
}

// The listener starts only when BOTH variables are set.
func TestIdentityConfigFromEnv(t *testing.T) {
	for _, c := range []struct {
		port, file string
		want       bool
	}{{"8766", "/x", true}, {"", "/x", false}, {"8766", "", false}, {"", "", false}} {
		t.Setenv(identityPortEnv, c.port)
		t.Setenv(keysFileEnv, c.file)
		if _, ok := identityConfigFromEnv(); ok != c.want {
			t.Errorf("port=%q file=%q: ok=%v, want %v", c.port, c.file, ok, c.want)
		}
	}
}

// dozor_mcp_keyfile_last_reload_successful follows the store: 1 while the
// file is good, 0 once an edit breaks it.
func TestKeyfileReloadGauge(t *testing.T) {
	k := newTestKey(t, "probe", "probe-agent", "agent_status")
	r := newIDRig(t, k)
	identity.ObserveKeyfile(r.kf)
	if got := metricValue(t, "dozor_mcp_keyfile_last_reload_successful", nil); got != 1 {
		t.Fatalf("gauge = %v after a good load, want 1", got)
	}
	if err := os.WriteFile(r.keyPath, []byte("version: 1\nkeys: [not valid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(4 * time.Second)
	for metricValue(t, "dozor_mcp_keyfile_last_reload_successful", nil) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("gauge stayed 1 after the key file became invalid")
		}
		r.call(t, k.token, listBodyJSON) // a verification is what triggers the reload
		time.Sleep(200 * time.Millisecond)
	}
}
