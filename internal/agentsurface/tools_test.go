package agentsurface_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anatolykoptev/dozor/internal/agentproto"
	"github.com/anatolykoptev/dozor/internal/agentsurface"
	"github.com/anatolykoptev/dozor/internal/agentworker"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// rig runs real workers (the Serve of internal/agentworker, one goroutine
// per connection, like an Accept=yes unit) behind real unix sockets and
// connects an MCP client to a server carrying the agent tools.
type rig struct {
	dir     string
	homes   map[string]string
	current atomic.Value // identity the resolver returns
	session *mcp.ClientSession
	server  *mcp.Server
}

func newRig(t *testing.T, identities ...string) *rig {
	t.Helper()
	dir, err := os.MkdirTemp("", "as-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	r := &rig{dir: dir, homes: map[string]string{}}
	r.current.Store(identities[0])
	for _, id := range identities {
		home, err := os.MkdirTemp("", "home-"+id+"-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(home) })
		home, _ = filepath.EvalSymlinks(home)
		r.homes[id] = home
		cfg := &agentworker.Config{Home: home, User: id}
		ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(dir, id+".sock"), Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = ln.Close() })
		go func() {
			for {
				c, err := ln.AcceptUnix()
				if err != nil {
					return
				}
				go func() { defer c.Close(); _ = cfg.Serve(context.Background(), c) }()
			}
		}()
	}

	r.server = mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	agentsurface.Register(r.server, func(context.Context, *mcp.CallToolRequest) (string, error) {
		id, _ := r.current.Load().(string)
		if id == "" {
			return "", errors.New("unauthenticated")
		}
		return id, nil
	}, agentsurface.Config{SocketDir: dir, ExpectedPeerUID: uint32(os.Getuid())}) //nolint:gosec // test

	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := r.server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	r.session, err = mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.session.Close() })
	return r
}

func (r *rig) call(t *testing.T, tool string, args map[string]any) (*mcp.CallToolResult, map[string]any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	res, err := r.session.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("%s: protocol error: %v", tool, err)
	}
	var out map[string]any
	if res.StructuredContent != nil {
		b, _ := json.Marshal(res.StructuredContent)
		_ = json.Unmarshal(b, &out)
	}
	return res, out
}

func errText(res *mcp.CallToolResult) string {
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}

func TestTools_ExactSetAndNoIdentityInSchema(t *testing.T) {
	r := newRig(t, "alice")
	lt, err := r.session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range lt.Tools {
		names = append(names, tool.Name)
		b, _ := json.Marshal(tool.InputSchema)
		for _, forbidden := range []string{"user", "uid", "host", "socket", "run_as", "identity", "env"} {
			if strings.Contains(strings.ToLower(string(b)), `"`+forbidden+`"`) {
				t.Errorf("%s input schema exposes %q: %s", tool.Name, forbidden, b)
			}
		}
	}
	slices.Sort(names)
	want := slices.Sorted(slices.Values(agentsurface.ToolNames))
	if !slices.Equal(names, want) {
		t.Fatalf("tools = %v, want %v", names, want)
	}
	if want := []string{"agent_exec", "agent_read_file", "agent_status", "agent_write_file"}; !slices.Equal(names, want) {
		t.Fatalf("the declared tool set drifted: %v", names)
	}
}

func TestTools_EndToEnd(t *testing.T) {
	r := newRig(t, "alice")
	home := r.homes["alice"]

	res, out := r.call(t, "agent_exec", map[string]any{"command": "echo hi; pwd -P; id -u"})
	if res.IsError || !strings.HasPrefix(out["stdout"].(string), "hi\n"+home+"\n") {
		t.Fatalf("exec: %v %s", res.IsError, errText(res))
	}
	if out["stdout"].(string) != "hi\n"+home+"\n"+itoa(os.Getuid())+"\n" {
		t.Fatalf("exec ran as the wrong uid or cwd: %q", out["stdout"])
	}

	bin := []byte{0, 1, 2, 0xff}
	res, _ = r.call(t, "agent_write_file", map[string]any{
		"path": "sub/blob.bin", "base64": base64.StdEncoding.EncodeToString(bin), "create_parents": true,
	})
	if res.IsError {
		t.Fatalf("write: %s", errText(res))
	}
	if got, _ := os.ReadFile(filepath.Join(home, "sub/blob.bin")); string(got) != string(bin) {
		t.Fatalf("file on disk = %v", got)
	}
	res, out = r.call(t, "agent_read_file", map[string]any{"path": "sub/blob.bin"})
	dec, _ := base64.StdEncoding.DecodeString(out["content"].(string))
	if res.IsError || out["encoding"] != "base64" || string(dec) != string(bin) {
		t.Fatalf("read: %v %v", res.IsError, out)
	}

	res, _ = r.call(t, "agent_write_file", map[string]any{"path": "sub/blob.bin", "text": "x"})
	if !res.IsError {
		t.Fatal("overwrite without overwrite=true succeeded")
	}

	res, out = r.call(t, "agent_status", nil)
	if res.IsError || int(out["uid"].(float64)) != os.Getuid() || out["home"] != home {
		t.Fatalf("status: %v %v", res.IsError, out)
	}
}

func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }

func TestTools_RouteByResolvedIdentity(t *testing.T) {
	r := newRig(t, "alice", "bob")
	r.current.Store("alice")
	_, out := r.call(t, "agent_exec", map[string]any{"command": "pwd -P"})
	if strings.TrimSpace(out["stdout"].(string)) != r.homes["alice"] {
		t.Fatalf("alice call ran in %q", out["stdout"])
	}
	r.current.Store("bob")
	_, out = r.call(t, "agent_exec", map[string]any{"command": "pwd -P"})
	if strings.TrimSpace(out["stdout"].(string)) != r.homes["bob"] {
		t.Fatalf("bob call ran in %q", out["stdout"])
	}
	// An identity without a worker has no socket: unavailable, no fallback.
	r.current.Store("carol")
	res, _ := r.call(t, "agent_exec", map[string]any{"command": "echo SHOULD-NOT-RUN"})
	if !res.IsError || errText(res) != "unavailable" {
		t.Fatalf("no worker: IsError=%v text=%q", res.IsError, errText(res))
	}
}

func TestTools_UnresolvedOrInvalidIdentityIsDenied(t *testing.T) {
	r := newRig(t, "alice")
	for _, id := range []string{"", "root", "../alice", "krolik"} {
		r.current.Store(id)
		res, _ := r.call(t, "agent_exec", map[string]any{"command": "echo SHOULD-NOT-RUN"})
		if !res.IsError || errText(res) != "denied" {
			t.Errorf("identity %q: IsError=%v text=%q, want denied", id, res.IsError, errText(res))
		}
	}
}

func TestTools_ClientSideCaps(t *testing.T) {
	r := newRig(t, "alice")
	big := strings.Repeat("a", agentproto.MaxCommandBytes+1)
	for name, tc := range map[string]struct {
		tool string
		args map[string]any
	}{
		"command size":  {"agent_exec", map[string]any{"command": big}},
		"timeout":       {"agent_exec", map[string]any{"command": "true", "timeout_s": 901}},
		"empty command": {"agent_exec", map[string]any{"command": ""}},
		"read length":   {"agent_read_file", map[string]any{"path": "x", "length": agentproto.MaxReadBytes + 1}},
		"write neither": {"agent_write_file", map[string]any{"path": "x"}},
		"write both":    {"agent_write_file", map[string]any{"path": "x", "text": "a", "base64": "YQ=="}},
		"write size":    {"agent_write_file", map[string]any{"path": "x", "text": strings.Repeat("a", agentproto.MaxWriteBytes+1)}},
	} {
		if res, _ := r.call(t, tc.tool, tc.args); !res.IsError {
			t.Errorf("%s: accepted", name)
		}
	}
	// 4 MiB exactly is accepted.
	edge := strings.Repeat("a", agentproto.MaxWriteBytes)
	if res, _ := r.call(t, "agent_write_file", map[string]any{"path": "edge", "text": edge}); res.IsError {
		t.Fatalf("4 MiB write refused: %s", errText(res))
	}
}

func TestTools_WorkerFailureModes(t *testing.T) {
	r := newRig(t, "alice")
	res, _ := r.call(t, "agent_read_file", map[string]any{"path": "does-not-exist"})
	if !res.IsError || !strings.HasPrefix(errText(res), "agent_read_file: ") {
		t.Fatalf("read of a missing file: %q", errText(res))
	}
	start := time.Now()
	res, out := r.call(t, "agent_exec", map[string]any{"command": "sleep 30", "timeout_s": 1})
	if res.IsError || out["timed_out"] != true || time.Since(start) > 10*time.Second {
		t.Fatalf("timeout case: IsError=%v out=%v after %s", res.IsError, out, time.Since(start))
	}
}

// Binary output used to be rejected as "unavailable": the worker measured raw
// bytes while the client measures the decoded string, and encoding/json turns
// every invalid byte into a 3-byte U+FFFD.
func TestTools_BinaryExecOutputRoundTrips(t *testing.T) {
	r := newRig(t, "alice")
	res, out := r.call(t, "agent_exec", map[string]any{"command": "head -c 100000 /dev/urandom; head -c 100000 /dev/urandom >&2"})
	if res.IsError {
		t.Fatalf("binary output failed the call: %q", errText(res))
	}
	for _, k := range []string{"stdout", "stderr"} {
		s, _ := out[k].(string)
		if len(s) == 0 || len(s) > agentproto.OutputTailBytes {
			t.Errorf("%s: %d bytes, want 1..%d", k, len(s), agentproto.OutputTailBytes)
		}
	}
}

func TestTools_PathAndCwdLengthCaps(t *testing.T) {
	r := newRig(t, "alice")
	long := strings.Repeat("a", agentproto.MaxPathBytes+1)
	for tool, args := range map[string]map[string]any{
		"agent_exec":       {"command": "true", "cwd": long},
		"agent_read_file":  {"path": long},
		"agent_write_file": {"path": long, "text": "x"},
	} {
		if res, _ := r.call(t, tool, args); !res.IsError {
			t.Errorf("%s accepted an over-long path", tool)
		}
	}
}
