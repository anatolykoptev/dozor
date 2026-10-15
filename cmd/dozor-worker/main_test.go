package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/anatolykoptev/dozor/internal/agentproto"
)

// okDeps passes every check except what a test overrides. euid is an agent
// uid different from the test runner's uid.
func okDeps(t *testing.T) deps {
	t.Helper()
	euid := 2500
	if os.Getuid() == euid {
		euid++
	}
	return deps{
		geteuid:     func() int { return euid },
		procStatus:  func() (string, int, error) { return "0000000000000000", 1, nil },
		mountCheck:  func(string) error { return nil },
		home:        t.TempDir(),
		user:        "tester",
		findCommand: func(string) (string, error) { return "", errors.New("absent") },
	}
}

// serveOnce runs the worker against one end of a socketpair that inherits
// no close-on-exec flag (like systemd's fd 0) and returns exit code + reply.
func serveOnce(t *testing.T, args []string, d deps, request string) (int, agentproto.Response, string) {
	t.Helper()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	stdin := os.NewFile(uintptr(fds[0]), "stdin")
	cf := os.NewFile(uintptr(fds[1]), "client")
	client, err := net.FileConn(cf)
	if err != nil {
		t.Fatal(err)
	}
	_ = cf.Close()
	defer client.Close()

	code := make(chan int, 1)
	var stderr bytes.Buffer
	go func() { code <- run(args, stdin, io.Discard, &stderr, d); _ = stdin.Close() }()
	_, _ = io.WriteString(client, request)
	_ = client.(*net.UnixConn).CloseWrite()
	b, _ := io.ReadAll(client)
	var resp agentproto.Response
	if len(b) > 0 {
		if err := json.Unmarshal(b, &resp); err != nil {
			t.Fatalf("reply %q: %v", b, err)
		}
	}
	return <-code, resp, stderr.String()
}

const helloReq = `{"v":2,"op":"hello"}`

func callerArg(uid int) []string { return []string{"--caller-uid", strconv.Itoa(uid)} }

func TestWorker_ServesTheExpectedCaller(t *testing.T) {
	code, resp, stderr := serveOnce(t, callerArg(os.Getuid()), okDeps(t), helloReq)
	if code != 0 || resp.Error != nil || resp.V != agentproto.ProtoVersion {
		t.Fatalf("code=%d resp=%+v stderr=%s", code, resp, stderr)
	}
}

func TestWorker_RejectsWrongPeerUID(t *testing.T) {
	// The test process is the peer, so any other expected uid is a stranger.
	code, resp, _ := serveOnce(t, callerArg(os.Getuid()+1), okDeps(t), helloReq)
	if code == 0 || resp.Error == nil || resp.Error.Code != agentproto.CodeRefused {
		t.Fatalf("code=%d resp=%+v, want refused", code, resp)
	}
	if !strings.Contains(resp.Error.Message, "peer uid") {
		t.Fatalf("refused for the wrong reason: %q", resp.Error.Message)
	}
}

func TestWorker_RefusesRootAndCaller(t *testing.T) {
	caller := os.Getuid()
	cases := []struct {
		name string
		euid int
		ok   bool
	}{
		{"root", 0, false},
		{"the caller's own uid", caller, false},
		{"an ordinary account", 1002, false},
		{"just below the range", agentproto.UIDMin - 1, false},
		{"just above the range", agentproto.UIDMax + 1, false},
		{"range start", agentproto.UIDMin, caller != agentproto.UIDMin},
		{"range end", agentproto.UIDMax, caller != agentproto.UIDMax},
		{"mid range", 2001, caller != 2001},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := okDeps(t)
			d.geteuid = func() int { return tc.euid }
			code, resp, _ := serveOnce(t, callerArg(caller), d, helloReq)
			if tc.ok && (code != 0 || resp.Error != nil) {
				t.Fatalf("euid %d refused: code=%d %+v", tc.euid, code, resp.Error)
			}
			if !tc.ok && (code == 0 || resp.Error == nil || resp.Error.Code != agentproto.CodeRefused) {
				t.Fatalf("euid %d served: code=%d %+v", tc.euid, code, resp)
			}
		})
	}
}

func TestWorker_RefusesPrivilegedProcess(t *testing.T) {
	cases := map[string]func() (string, int, error){
		"capabilities held":  func() (string, int, error) { return "0000000000000400", 1, nil },
		"no_new_privs unset": func() (string, int, error) { return "0000000000000000", 0, nil },
		"status unreadable":  func() (string, int, error) { return "", 0, errors.New("boom") },
		"all caps":           func() (string, int, error) { return "000001ffffffffff", 1, nil },
	}
	for name, read := range cases {
		d := okDeps(t)
		d.procStatus = read
		code, resp, _ := serveOnce(t, callerArg(os.Getuid()), d, helloReq)
		if code == 0 || resp.Error == nil || resp.Error.Code != agentproto.CodeRefused {
			t.Errorf("%s: served: code=%d %+v", name, code, resp)
		}
	}
}

func TestWorker_RefusesWhenMountCheckFails(t *testing.T) {
	d := okDeps(t)
	d.mountCheck = func(dir string) error { return fmt.Errorf("%s is the root fs", dir) }
	args := append(callerArg(os.Getuid()), "--require-mount", "/srv/x")
	code, resp, _ := serveOnce(t, args, d, helloReq)
	if code == 0 || resp.Error == nil || resp.Error.Code != agentproto.CodeRefused {
		t.Fatalf("code=%d %+v", code, resp)
	}
	// Without the flag the check is not consulted.
	if code, resp, _ = serveOnce(t, callerArg(os.Getuid()), d, helloReq); code != 0 || resp.Error != nil {
		t.Fatalf("code=%d %+v", code, resp)
	}
}

func TestMountOK(t *testing.T) {
	if mountOK(7, 7, ext4Magic) == nil {
		t.Error("same device as / accepted")
	}
	if mountOK(7, 8, 0x01021994) == nil { // tmpfs
		t.Error("non-ext4 accepted")
	}
	if err := mountOK(7, 8, ext4Magic); err != nil {
		t.Errorf("separate ext4 refused: %v", err)
	}
}

func TestParseProcStatus(t *testing.T) {
	const status = "Name:\tx\nCapInh:\t0000000000000000\nCapEff:\t0000000000000000\nNoNewPrivs:\t1\nSeccomp:\t2\n"
	capEff, nnp, err := parseProcStatus(status)
	if err != nil || capEff != "0000000000000000" || nnp != 1 {
		t.Fatalf("%q %d %v", capEff, nnp, err)
	}
	for _, bad := range []string{"", "CapEff:\t0\n", "NoNewPrivs:\t1\n", "CapEff:\t0\nNoNewPrivs:\tx\n"} {
		if _, _, err := parseProcStatus(bad); err == nil {
			t.Errorf("parseProcStatus(%q) accepted", bad)
		}
	}
}

func TestWorker_ProtocolFlag(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"--protocol"}, nil, &out, io.Discard, okDeps(t)); code != 0 ||
		strings.TrimSpace(out.String()) != strconv.Itoa(agentproto.ProtoVersion) {
		t.Fatalf("code=%d out=%q", code, out.String())
	}
}

func TestWorker_RequiresCallerUID(t *testing.T) {
	if code := run(nil, nil, io.Discard, io.Discard, okDeps(t)); code == 0 {
		t.Fatal("started without --caller-uid")
	}
}
