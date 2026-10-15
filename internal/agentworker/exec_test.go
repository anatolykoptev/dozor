package agentworker

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/anatolykoptev/dozor/internal/agentproto"
)

func newCfg(t *testing.T) *Config {
	t.Helper()
	home, err := os.MkdirTemp("", "agentworker-home-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	if home, err = filepath.EvalSymlinks(home); err != nil { // macOS /var -> /private/var
		t.Fatal(err)
	}
	return &Config{Home: home, User: "tester"}
}

func run(t *testing.T, c *Config, req agentproto.ExecRequest) *agentproto.ExecResult {
	t.Helper()
	res, perr := c.Exec(&req)
	if perr != nil {
		t.Fatalf("exec: %+v", perr)
	}
	return res
}

func TestExec_Basics(t *testing.T) {
	c := newCfg(t)
	res := run(t, c, agentproto.ExecRequest{Command: "echo out; echo err >&2; exit 7"})
	if res.ExitCode != 7 || res.Stdout != "out\n" || res.Stderr != "err\n" || res.TimedOut {
		t.Fatalf("got %+v", res)
	}
	res = run(t, c, agentproto.ExecRequest{Command: "cat", Stdin: "hello"})
	if res.Stdout != "hello" {
		t.Fatalf("stdin not delivered: %+v", res)
	}
	res = run(t, c, agentproto.ExecRequest{Command: "pwd -P"})
	if got := strings.TrimSpace(res.Stdout); got != c.Home {
		t.Fatalf("default cwd = %q, want home %q", got, c.Home)
	}
	res = run(t, c, agentproto.ExecRequest{Command: "kill -9 $$"})
	if res.Signal != "killed" || res.ExitCode != -1 {
		t.Fatalf("signal not reported: %+v", res)
	}
}

func TestExec_StdinDefaultsToDevNull(t *testing.T) {
	c := newCfg(t)
	res := run(t, c, agentproto.ExecRequest{Command: "cat; echo eof"})
	if res.Stdout != "eof\n" {
		t.Fatalf("cat on default stdin did not see EOF immediately: %+v", res)
	}
}

func TestExec_CleanEnv(t *testing.T) {
	t.Setenv("DOZOR_TEST_SECRET", "leak")
	t.Setenv("PATH", "/nonexistent")
	c := newCfg(t)
	res := run(t, c, agentproto.ExecRequest{Command: "env"})
	if strings.Contains(res.Stdout, "DOZOR_TEST_SECRET") {
		t.Fatalf("worker environment leaked into child: %s", res.Stdout)
	}
	allowed := map[string]bool{"HOME": true, "LANG": true, "TMPDIR": true, "PATH": true, "USER": true,
		"LOGNAME": true, "PWD": true, "SHLVL": true, "_": true, "OLDPWD": true}
	for _, line := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
		k, _, _ := strings.Cut(line, "=")
		if !allowed[k] {
			t.Errorf("unexpected env var %q", k)
		}
	}
	if !strings.Contains(res.Stdout, "HOME="+c.Home+"\n") || !strings.Contains(res.Stdout, ".local/bin:") {
		t.Fatalf("HOME/PATH not set: %s", res.Stdout)
	}
}

func TestExec_BadRequests(t *testing.T) {
	c := newCfg(t)
	cases := map[string]agentproto.ExecRequest{
		"empty":        {},
		"huge command": {Command: strings.Repeat("a", agentproto.MaxCommandBytes+1)},
		"timeout high": {Command: "true", TimeoutS: agentproto.MaxTimeoutS + 1},
		"timeout neg":  {Command: "true", TimeoutS: -1},
		"huge stdin":   {Command: "true", Stdin: strings.Repeat("a", agentproto.MaxStdinBytes+1)},
	}
	for name, req := range cases {
		if _, perr := c.Exec(&req); perr == nil || perr.Code != agentproto.CodeBadRequest {
			t.Errorf("%s: got %+v, want bad_request", name, perr)
		}
	}
	if _, perr := c.Exec(&agentproto.ExecRequest{Command: "true", Cwd: "/nonexistent/dir"}); perr == nil || perr.Code != agentproto.CodeOp {
		t.Errorf("bad cwd: got %+v, want op error", perr)
	}
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func TestExec_TimeoutKillsProcessGroup(t *testing.T) {
	c := newCfg(t)
	start := time.Now()
	res := run(t, c, agentproto.ExecRequest{Command: "sleep 60 & echo $!; wait", TimeoutS: 1})
	if !res.TimedOut {
		t.Fatalf("not timed out: %+v", res)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("took %s", d)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(res.Stdout))
	if err != nil {
		t.Fatalf("no child pid in %q", res.Stdout)
	}
	deadline := time.Now().Add(3 * time.Second)
	for alive(pid) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if alive(pid) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Fatal("grandchild survived the group kill")
	}
}

func TestExec_BackgroundHolderDoesNotHang(t *testing.T) {
	c := newCfg(t)
	start := time.Now()
	res := run(t, c, agentproto.ExecRequest{Command: "sleep 20 & echo $!"})
	pid, _ := strconv.Atoi(strings.TrimSpace(res.Stdout))
	if pid > 0 {
		t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	}
	if d := time.Since(start); d > 8*time.Second {
		t.Fatalf("exec hung on a backgrounded pipe holder: %s", d)
	}
	if res.ExitCode != 0 || pid == 0 {
		t.Fatalf("unexpected result %+v", res)
	}
}

func TestExec_OutputTailAndFit(t *testing.T) {
	c := newCfg(t)
	res := run(t, c, agentproto.ExecRequest{Command: "yes abcdefgh | head -c 400000"})
	if !res.Truncated || len(res.Stdout) > agentproto.OutputTailBytes {
		t.Fatalf("tail not applied: truncated=%v len=%d", res.Truncated, len(res.Stdout))
	}
	// NUL bytes inflate six-fold under JSON escaping: the reply must still fit.
	res = run(t, c, agentproto.ExecRequest{Command: "head -c 200000 /dev/zero; head -c 200000 /dev/zero >&2"})
	b, err := json.Marshal(&agentproto.Response{Exec: res})
	if err != nil || len(b) >= agentproto.MaxReplyBytes {
		t.Fatalf("reply %d bytes (err %v) exceeds cap", len(b), err)
	}
	if !res.Truncated {
		t.Fatal("shrunk output must be flagged truncated")
	}
}

func socketPair(t *testing.T) (server *os.File, client net.Conn) {
	t.Helper()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Both ends are deliberately NOT close-on-exec, like fd 0/1 under socket
	// activation.
	server = os.NewFile(uintptr(fds[0]), "stdin")
	cf := os.NewFile(uintptr(fds[1]), "client")
	client, err = net.FileConn(cf)
	if err != nil {
		t.Fatal(err)
	}
	_ = cf.Close()
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
	return server, client
}

const listSockets = `for i in $(seq 0 63); do [ -S /dev/fd/$i ] && echo leak$i; done; echo done`

func TestExec_ChildDoesNotInheritConn(t *testing.T) {
	c := newCfg(t)
	server, client := socketPair(t)

	// Control: unsealed, the inherited descriptor is visible to a child, so
	// the probe below can fail.
	res := run(t, c, agentproto.ExecRequest{Command: listSockets})
	if !strings.Contains(res.Stdout, "leak") {
		t.Fatalf("control: probe cannot see an inherited socket: %q", res.Stdout)
	}

	srvConn, err := net.FileConn(server)
	if err != nil {
		t.Fatal(err)
	}
	defer srvConn.Close()
	done := make(chan error, 1)
	go func() { done <- c.Serve(context.Background(), srvConn, server) }()

	req, _ := json.Marshal(agentproto.Request{V: agentproto.ProtoVersion, Op: agentproto.OpExec,
		Exec: &agentproto.ExecRequest{Command: listSockets}})
	if _, err := client.Write(req); err != nil {
		t.Fatal(err)
	}
	_ = client.(*net.UnixConn).CloseWrite()
	var resp agentproto.Response
	if err := json.NewDecoder(client).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if resp.Exec == nil || resp.Exec.Stdout != "done\n" {
		t.Fatalf("child saw a socket descriptor: %+v / %+v", resp.Exec, resp.Error)
	}
}
