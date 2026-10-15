package agentsurface

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anatolykoptev/dozor/internal/agentproto"
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

	startFake(t, dir, "mute", func(*net.UnixConn, []byte) {})
	if err := do("mute"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("EOF without reply: %v", err)
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
	if len(msg) > agentproto.MaxErrorBytes+len(ToolExec)+8 || strings.ContainsAny(msg, "\x1b\n\u202e") {
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

func TestClient_OnReplyReportsVersionAndBuild(t *testing.T) {
	dir := newDir(t)
	startFake(t, dir, "kim", okReply)
	var gotID, gotBuild string
	var gotV int
	c := NewClient(Config{SocketDir: dir, ExpectedPeerUID: uint32(os.Getuid()), //nolint:gosec // test
		OnReply: func(id string, v int, build string) { gotID, gotV, gotBuild = id, v, build }})
	if _, err := c.Do(context.Background(), "kim", hello(), time.Second); err != nil {
		t.Fatal(err)
	}
	if gotID != "kim" || gotV != agentproto.ProtoVersion || gotBuild != "b" {
		t.Fatalf("OnReply got %q %d %q", gotID, gotV, gotBuild)
	}
}
