package agentsurface

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/anatolykoptev/dozor/internal/agentproto"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeWorker listens on dir/<id>.sock and calls serve for every connection.
type fakeWorker struct {
	ln    *net.UnixListener
	reads chan []byte
}

func newDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "as-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func startFake(t *testing.T, dir, id string, serve func(c *net.UnixConn, request []byte)) *fakeWorker {
	t.Helper()
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(dir, id+".sock"), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	fw := &fakeWorker{ln: ln, reads: make(chan []byte, 16)}
	go func() {
		for {
			c, err := ln.AcceptUnix()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
				b, _ := io.ReadAll(c)
				fw.reads <- b
				serve(c, b)
			}()
		}
	}()
	return fw
}

func reply(c *net.UnixConn, body string) { _, _ = io.WriteString(c, body) }

func okReply(c *net.UnixConn, _ []byte) {
	reply(c, fmt.Sprintf(`{"v":%d,"build":"b"}`, agentproto.ProtoVersion))
}

func testClient(dir string) *Client {
	return NewClient(Config{SocketDir: dir, ExpectedPeerUID: uint32(os.Getuid()), //nolint:gosec // test
		DialTimeout: time.Second})
}

func hello() *agentproto.Request {
	return &agentproto.Request{V: agentproto.ProtoVersion, Op: agentproto.OpHello}
}

func TestClient_HappyPath(t *testing.T) {
	dir := newDir(t)
	fw := startFake(t, dir, "alice", okReply)
	resp, err := testClient(dir).Do(context.Background(), "alice", hello(), time.Second)
	if err != nil || resp.V != agentproto.ProtoVersion || resp.Build != "b" {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
	var got agentproto.Request
	if err := json.Unmarshal(<-fw.reads, &got); err != nil || got.Op != agentproto.OpHello {
		t.Fatalf("worker saw %+v / %v", got, err)
	}
}

func TestClient_WorkerDownIsUnavailable(t *testing.T) {
	dir := newDir(t)
	c := testClient(dir)
	do := func(id string) error {
		resp, err := c.Do(context.Background(), id, hello(), 400*time.Millisecond)
		if err == nil {
			t.Errorf("%s: got reply %+v, want an error", id, resp)
		}
		return err
	}
	if err := do("absent"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("no socket: %v", err)
	}

	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(dir, "stale.sock"), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	ln.SetUnlinkOnClose(false)
	_ = ln.Close() // socket file stays, nobody listens
	if err := do("stale"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("stale socket: %v", err)
	}

	startFake(t, dir, "hung", func(c *net.UnixConn, _ []byte) { time.Sleep(3 * time.Second) })
	start := time.Now()
	if err := do("hung"); !errors.Is(err, ErrUnavailable) || time.Since(start) > 2*time.Second {
		t.Errorf("hung worker: %v after %s", err, time.Since(start))
	}

	startFake(t, dir, "garbage", func(c *net.UnixConn, _ []byte) { reply(c, "not json") })
	if err := do("garbage"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("garbage reply: %v", err)
	}
}

func TestClient_ProtoSkew(t *testing.T) {
	n := agentproto.ProtoVersion
	cases := []struct {
		name string
		body string
		ok   bool
	}{
		{"N", fmt.Sprintf(`{"v":%d,"build":"b"}`, n), true},
		{"N-1", fmt.Sprintf(`{"v":%d,"build":"b"}`, n-1), true},
		{"N-2", fmt.Sprintf(`{"v":%d,"build":"b"}`, n-2), false},
		{"N+1", fmt.Sprintf(`{"v":%d,"build":"b"}`, n+1), false},
		{"missing", `{"build":"b"}`, false},
		{"zero", `{"v":0,"build":"b"}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := newDir(t)
			startFake(t, dir, "bob", func(c *net.UnixConn, _ []byte) { reply(c, tc.body) })
			_, err := testClient(dir).Do(context.Background(), "bob", hello(), time.Second)
			if tc.ok != (err == nil) {
				t.Fatalf("err = %v, want ok=%v", err, tc.ok)
			}
			if !tc.ok && !errors.Is(err, ErrUnavailable) {
				t.Fatalf("err = %v, want unavailable", err)
			}
		})
	}
}

func TestClient_OversizedReplyRejected(t *testing.T) {
	pad := func(total int) string {
		head := fmt.Sprintf(`{"v":%d,"build":"`, agentproto.ProtoVersion)
		return head + strings.Repeat("a", total-len(head)-2) + `"}`
	}
	for name, tc := range map[string]struct {
		body string
		ok   bool
	}{
		"at the cap":   {pad(agentproto.MaxReplyBytes), true},
		"over the cap": {pad(agentproto.MaxReplyBytes + 1), false},
		"far over":     {pad(4 * agentproto.MaxReplyBytes), false},
	} {
		t.Run(name, func(t *testing.T) {
			dir := newDir(t)
			startFake(t, dir, "carol", func(c *net.UnixConn, _ []byte) { reply(c, tc.body) })
			_, err := testClient(dir).Do(context.Background(), "carol", hello(), 3*time.Second)
			if tc.ok != (err == nil) || (!tc.ok && !errors.Is(err, ErrUnavailable)) {
				t.Fatalf("err = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}

func TestClient_StrictDecoder(t *testing.T) {
	v := agentproto.ProtoVersion
	deep := strings.Repeat("[", agentproto.MaxJSONDepth+2) + strings.Repeat("]", agentproto.MaxJSONDepth+2)
	for name, body := range map[string]string{
		"unknown field": fmt.Sprintf(`{"v":%d,"build":"b","extra":1}`, v),
		"trailing":      fmt.Sprintf(`{"v":%d,"build":"b"} {}`, v),
		"too deep":      fmt.Sprintf(`{"v":%d,"build":"b","status":{"notes":%s}}`, v, deep),
		"wrong type":    fmt.Sprintf(`{"v":"%d","build":"b"}`, v),
	} {
		dir := newDir(t)
		startFake(t, dir, "dave", func(c *net.UnixConn, _ []byte) { reply(c, body) })
		if _, err := testClient(dir).Do(context.Background(), "dave", hello(), time.Second); !errors.Is(err, ErrUnavailable) {
			t.Errorf("%s: err = %v, want unavailable", name, err)
		}
	}
}

func TestClient_RejectsSquattedSocket(t *testing.T) {
	dir := newDir(t)
	fw := startFake(t, dir, "erin", okReply)

	// The squatter here is this very process, so "expected owner = a
	// different uid" is exactly the squat situation, whoever runs the test.
	c := NewClient(Config{SocketDir: dir, ExpectedPeerUID: uint32(os.Getuid()) + 1, DialTimeout: time.Second}) //nolint:gosec // test
	_, err := c.Do(context.Background(), "erin", hello(), time.Second)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want unavailable", err)
	}
	// The squatter may see the connection, but never a request byte.
	select {
	case b := <-fw.reads:
		if len(b) != 0 {
			t.Fatalf("request was sent to the squatter: %q", b)
		}
	case <-time.After(300 * time.Millisecond):
	}
	// Control: the right owner is served.
	if _, err := testClient(dir).Do(context.Background(), "erin", hello(), time.Second); err != nil {
		t.Fatalf("legitimate owner refused: %v", err)
	}
}

func TestClient_BusyWhenSlotsExhausted(t *testing.T) {
	dir := newDir(t)
	gate := make(chan struct{})
	fw := startFake(t, dir, "fay", func(c *net.UnixConn, b []byte) { <-gate; okReply(c, b) })
	c := NewClient(Config{SocketDir: dir, ExpectedPeerUID: uint32(os.Getuid()), MaxInFlight: 2}) //nolint:gosec // test
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = c.Do(context.Background(), "fay", hello(), 5*time.Second) }()
	}
	for range 2 { // both slots are now held by in-flight calls
		select {
		case <-fw.reads:
		case <-time.After(2 * time.Second):
			t.Fatal("in-flight calls never reached the worker")
		}
	}
	_, err := c.Do(context.Background(), "fay", hello(), time.Second)
	close(gate)
	wg.Wait()
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("err = %v, want busy", err)
	}
	// Another identity has its own slots.
	startFake(t, dir, "gus", okReply)
	if _, err := c.Do(context.Background(), "gus", hello(), time.Second); err != nil {
		t.Fatalf("other identity affected: %v", err)
	}
	// And the slot is released afterwards.
	if _, err := c.Do(context.Background(), "fay", hello(), time.Second); err != nil {
		t.Fatalf("slot leaked: %v", err)
	}
}

func TestClient_ReplyBudgetPerMinute(t *testing.T) {
	dir := newDir(t)
	startFake(t, dir, "hal", okReply)
	clock := time.Unix(1_000_000, 0)
	c := NewClient(Config{SocketDir: dir, ExpectedPeerUID: uint32(os.Getuid()), ReplyBytesPerMinute: 30}) //nolint:gosec // test
	c.cfg.now = func() time.Time { return clock }
	if _, err := c.Do(context.Background(), "hal", hello(), time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Do(context.Background(), "hal", hello(), time.Second); err != nil {
		t.Fatalf("second call within budget: %v", err) // 22 bytes used < 30
	}
	if _, err := c.Do(context.Background(), "hal", hello(), time.Second); !errors.Is(err, ErrBusy) {
		t.Fatalf("err = %v, want busy once the budget is spent", err)
	}
	clock = clock.Add(61 * time.Second)
	if _, err := c.Do(context.Background(), "hal", hello(), time.Second); err != nil {
		t.Fatalf("budget did not reset: %v", err)
	}
}

func TestClient_InvalidIdentityNeverDials(t *testing.T) {
	dir := newDir(t)
	c := testClient(dir)
	for _, id := range []string{"", "../etc", "a/b", "root", "krolik", "ubuntu", "A", "x", strings.Repeat("a", 40)} {
		if _, err := c.Do(context.Background(), id, hello(), time.Second); !errors.Is(err, ErrDenied) {
			t.Errorf("identity %q: err = %v, want denied", id, err)
		}
	}
}

func TestClient_WorkerErrors(t *testing.T) {
	dir := newDir(t)
	nasty := "bad\x1b[31m\nline\u202e" + strings.Repeat("x", 1000)
	startFake(t, dir, "ivy", func(c *net.UnixConn, _ []byte) {
		b, _ := json.Marshal(agentproto.Response{V: agentproto.ProtoVersion, Build: "b",
			Error: &agentproto.Error{Code: agentproto.CodeOp, Message: nasty}})
		reply(c, string(b))
	})
	startFake(t, dir, "jon", func(c *net.UnixConn, _ []byte) {
		reply(c, fmt.Sprintf(`{"v":%d,"build":"b","error":{"code":"refused","message":"euid 0"}}`, agentproto.ProtoVersion))
	})
	cl := testClient(dir)
	tl := &tools{client: cl}

	resp, err := cl.Do(context.Background(), "ivy", hello(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	werr := tl.client.workerError("ivy", ToolExec, resp.Error)
	msg := werr.Error()
	if len(msg) > agentproto.MaxErrorBytes+len(ToolExec)+len(": worker error: ") || strings.ContainsAny(msg, "\x1b\n\u202e") {
		t.Fatalf("worker text not clamped/sanitised: %q", msg)
	}

	resp, err = cl.Do(context.Background(), "jon", hello(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := tl.client.workerError("jon", ToolExec, resp.Error); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("refusal = %v, want unavailable", err)
	}
}

func TestClient_OnReplyReportsVersionAndBuildMatch(t *testing.T) {
	dir := newDir(t)
	startFake(t, dir, "kim", okReply) // replies with build "b"
	startFake(t, dir, "lee", func(c *net.UnixConn, _ []byte) {
		reply(c, fmt.Sprintf(`{"v":%d,"build":%q}`, agentproto.ProtoVersion, agentproto.BuildID))
	})
	type seen struct {
		id string
		v  int
		ok bool
	}
	var got []seen
	c := NewClient(Config{SocketDir: dir, ExpectedPeerUID: uint32(os.Getuid()), //nolint:gosec // test
		OnReply: func(id string, v int, match bool) { got = append(got, seen{id, v, match}) }})
	for _, id := range []string{"kim", "lee"} {
		if _, err := c.Do(context.Background(), id, hello(), time.Second); err != nil {
			t.Fatal(err)
		}
	}
	want := []seen{{"kim", agentproto.ProtoVersion, false}, {"lee", agentproto.ProtoVersion, true}}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("OnReply saw %+v, want %+v", got, want)
	}
}

// abort closes with SO_LINGER 0 so the peer sees a reset where the OS allows.
func abort(c *net.UnixConn) {
	if raw, err := c.SyscallConn(); err == nil {
		_ = raw.Control(func(fd uintptr) {
			_ = syscall.SetsockoptLinger(int(fd), syscall.SOL_SOCKET, syscall.SO_LINGER, &syscall.Linger{Onoff: 1})
		})
	}
	_ = c.Close()
}

type lockedBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuf) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

func logged(buf *lockedBuf) *slog.Logger { return slog.New(slog.NewTextHandler(buf, nil)) }

func TestClient_HostileWorkerTextIsClampedInLogs(t *testing.T) {
	dir := newDir(t)
	hostileKey := strings.Repeat("K", 5000)
	startFake(t, dir, "mal", func(c *net.UnixConn, _ []byte) {
		reply(c, fmt.Sprintf(`{"v":%d,"build":"b","%s":1}`, agentproto.ProtoVersion, hostileKey))
	})
	var buf lockedBuf
	c := NewClient(Config{SocketDir: dir, ExpectedPeerUID: uint32(os.Getuid()), Logger: logged(&buf)}) //nolint:gosec // test
	if _, err := c.Do(context.Background(), "mal", hello(), time.Second); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
	out := buf.String()
	if out == "" || strings.Contains(out, strings.Repeat("K", agentproto.MaxErrorBytes+50)) {
		t.Fatalf("worker-controlled text not clamped in the log (%d bytes): %.300s", len(out), out)
	}

	// The identity is clamped too, whatever reaches fail().
	buf = lockedBuf{}
	c = NewClient(Config{Logger: logged(&buf)})
	_ = c.fail("evil\n"+strings.Repeat("I", 5000), "exec", "why", nil)
	if out = buf.String(); strings.Contains(out, "\n\"") || strings.Contains(out, strings.Repeat("I", agentproto.MaxErrorBytes+50)) {
		t.Fatalf("identity not clamped in the log: %.300s", out)
	}
}

func TestClient_BuildStringNeverLeavesTheClient(t *testing.T) {
	dir := newDir(t)
	hostile := "evil\n" + strings.Repeat("B", 5000)
	startFake(t, dir, "bld", func(c *net.UnixConn, _ []byte) {
		b, _ := json.Marshal(agentproto.Response{V: agentproto.ProtoVersion, Build: hostile})
		reply(c, string(b))
	})
	var buf lockedBuf
	var calls int
	c := NewClient(Config{SocketDir: dir, ExpectedPeerUID: uint32(os.Getuid()), Logger: logged(&buf), //nolint:gosec // test
		OnReply: func(_ string, _ int, match bool) {
			calls++
			if match {
				t.Error("hostile build reported as matching")
			}
		}})
	if _, err := c.Do(context.Background(), "bld", hello(), time.Second); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || strings.Contains(buf.String(), "BBBB") {
		t.Fatalf("calls=%d log=%q", calls, buf.String())
	}
}

func TestClient_ZeroByteReplyOrResetIsBusy(t *testing.T) {
	dir := newDir(t)
	startFake(t, dir, "mute", func(*net.UnixConn, []byte) {})
	startFake(t, dir, "rst", func(c *net.UnixConn, _ []byte) { abort(c) })
	var reasons []string
	var mu sync.Mutex
	c := NewClient(Config{SocketDir: dir, ExpectedPeerUID: uint32(os.Getuid()), //nolint:gosec // test
		OnReject: func(id, reason string) { mu.Lock(); reasons = append(reasons, id+":"+reason); mu.Unlock() }})
	for _, id := range []string{"mute", "rst"} {
		if _, err := c.Do(context.Background(), id, hello(), time.Second); !errors.Is(err, ErrBusy) {
			t.Errorf("%s: err = %v, want busy", id, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(reasons) != 2 || reasons[0] != "mute:"+RejectBacklog || reasons[1] != "rst:"+RejectBacklog {
		t.Fatalf("reasons = %v", reasons)
	}
}

func TestClient_OnRejectSlotsAndBudget(t *testing.T) {
	dir := newDir(t)
	gate := make(chan struct{})
	fw := startFake(t, dir, "ned", func(c *net.UnixConn, b []byte) { <-gate; okReply(c, b) })
	var mu sync.Mutex
	got := map[string]int{}
	c := NewClient(Config{SocketDir: dir, ExpectedPeerUID: uint32(os.Getuid()), MaxInFlight: 1, //nolint:gosec // test
		ReplyBytesPerMinute: 10,
		OnReject:            func(_, reason string) { mu.Lock(); got[reason]++; mu.Unlock() }})
	done := make(chan error, 1)
	go func() { _, err := c.Do(context.Background(), "ned", hello(), 5*time.Second); done <- err }()
	<-fw.reads
	if _, err := c.Do(context.Background(), "ned", hello(), time.Second); !errors.Is(err, ErrBusy) {
		t.Fatalf("slots: %v", err)
	}
	close(gate)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := c.Do(context.Background(), "ned", hello(), time.Second); !errors.Is(err, ErrBusy) { // 19 bytes spent > 10
		t.Fatalf("budget: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if got[RejectSlots] != 1 || got[RejectBudget] != 1 {
		t.Fatalf("rejections = %v", got)
	}
}

func TestClient_RejectedRepliesStillCostBudget(t *testing.T) {
	dir := newDir(t)
	big := fmt.Sprintf(`{"v":%d,"build":"%s"}`, agentproto.ProtoVersion, strings.Repeat("a", agentproto.MaxReplyBytes))
	startFake(t, dir, "ova", func(c *net.UnixConn, _ []byte) { reply(c, big) })
	c := NewClient(Config{SocketDir: dir, ExpectedPeerUID: uint32(os.Getuid()), ReplyBytesPerMinute: 1000}) //nolint:gosec // test
	if _, err := c.Do(context.Background(), "ova", hello(), 3*time.Second); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("over-cap reply: %v", err)
	}
	if _, err := c.Do(context.Background(), "ova", hello(), time.Second); !errors.Is(err, ErrBusy) {
		t.Fatalf("an over-cap reply did not cost any budget: %v", err)
	}
}

func TestClient_OversizedRequestRejectedBeforeDial(t *testing.T) {
	dir := newDir(t)
	fw := startFake(t, dir, "pat", okReply)
	c := testClient(dir)
	// 4 MiB of '<' would be 6 bytes each if HTML-escaped; unescaped it is the
	// payload size, so use a request that is over the cap either way.
	huge := strings.Repeat("\u0001", agentproto.MaxRequestBytes/6+10) // control bytes always escape
	req := &agentproto.Request{V: agentproto.ProtoVersion, Op: agentproto.OpWriteFile,
		WriteFile: &agentproto.WriteFileRequest{Path: "x", Text: &huge}}
	if _, err := c.Do(context.Background(), "pat", req, time.Second); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
	select {
	case <-fw.reads:
		t.Fatal("an oversized request reached the worker")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestClient_RequestIsNotHTMLEscaped(t *testing.T) {
	dir := newDir(t)
	fw := startFake(t, dir, "quin", okReply)
	text := "<a href='x'>&</a>"
	req := &agentproto.Request{V: agentproto.ProtoVersion, Op: agentproto.OpWriteFile,
		WriteFile: &agentproto.WriteFileRequest{Path: "x", Text: &text}}
	if _, err := testClient(dir).Do(context.Background(), "quin", req, time.Second); err != nil {
		t.Fatal(err)
	}
	if b := string(<-fw.reads); !strings.Contains(b, text) || strings.Contains(b, `\u003c`) {
		t.Fatalf("request was HTML-escaped: %s", b)
	}
}

func TestClassify(t *testing.T) {
	for err, want := range map[error]string{
		ErrUnavailable:                           ResultUnavailable,
		ErrBusy:                                  ResultBusy,
		ErrDenied:                                ResultDenied,
		fmt.Errorf("x: %w", ErrBusy):             ResultBusy,
		fmt.Errorf("%w: unavailable", ErrWorker): ResultError, // text never classifies
		invalid("busy"):                          ResultError,
		errors.New("denied"):                     ResultError,
	} {
		if got := Classify(err); got != want {
			t.Errorf("Classify(%v) = %s, want %s", err, got, want)
		}
	}
}

func TestTools_PathCapsAreEnforcedBeforeDialling(t *testing.T) {
	dir := newDir(t)
	fw := startFake(t, dir, "ray", okReply)
	tl := &tools{client: testClient(dir), resolve: func(context.Context, *mcp.CallToolRequest) (string, error) { return "ray", nil }}
	long := strings.Repeat("a", agentproto.MaxPathBytes+1)
	ctx := context.Background()
	if _, _, err := tl.exec(ctx, nil, ExecInput{Command: "true", Cwd: long}); !errors.Is(err, ErrInvalid) {
		t.Errorf("exec cwd: %v", err)
	}
	if _, _, err := tl.readFile(ctx, nil, ReadFileInput{Path: long}); !errors.Is(err, ErrInvalid) {
		t.Errorf("read path: %v", err)
	}
	if _, _, err := tl.writeFile(ctx, nil, WriteFileInput{Path: long, Text: new(string)}); !errors.Is(err, ErrInvalid) {
		t.Errorf("write path: %v", err)
	}
	select {
	case <-fw.reads:
		t.Fatal("an over-long path reached the worker")
	case <-time.After(200 * time.Millisecond):
	}
}

// exec disabled on the worker is "unavailable" with its own reject reason, not
// a plain worker error. RED-on-revert: delete the CodeExecDisabled branch in
// workerError.
func TestClient_ExecDisabledIsDistinct(t *testing.T) {
	var got []string
	c := NewClient(Config{OnReject: func(id, reason string) { got = append(got, id+":"+reason) }})
	err := c.workerError("alice", ToolExec, &agentproto.Error{Code: agentproto.CodeExecDisabled, Message: "agent_exec is disabled: no landlock"})
	if !errors.Is(err, ErrUnavailable) || errors.Is(err, ErrWorker) || Classify(err) != ResultUnavailable {
		t.Fatalf("err = %v (classified %s)", err, Classify(err))
	}
	if !strings.Contains(err.Error(), "no landlock") {
		t.Errorf("the agent is not told why: %v", err)
	}
	if len(got) != 1 || got[0] != "alice:exec-disabled" {
		t.Errorf("OnReject calls = %v", got)
	}
	// An ordinary operation failure stays a worker error and counts nothing.
	got = nil
	err = c.workerError("alice", ToolExec, &agentproto.Error{Code: agentproto.CodeOp, Message: "no such file"})
	if !errors.Is(err, ErrWorker) || len(got) != 0 {
		t.Errorf("op error: %v, rejects %v", err, got)
	}
}
