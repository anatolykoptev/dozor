package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/anatolykoptev/dozor/internal/agentproto"
	"golang.org/x/sys/unix"
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
		getegid:     func() int { return euid },
		getgroups:   func() ([]int, error) { return nil, nil },
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

// devNull is a stdin that is not a socket: a run that gets past argument
// validation fails cleanly (exit 1) instead of dereferencing a nil file.
func devNull(t *testing.T) *os.File {
	t.Helper()
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func callerArg(uid int) []string {
	return []string{"--caller-uid", strconv.Itoa(uid), "--no-mount-check"}
}

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
}

func TestCheckPeer_TypedReason(t *testing.T) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	f := os.NewFile(uintptr(fds[0]), "a")
	defer f.Close()
	other := os.NewFile(uintptr(fds[1]), "b")
	defer other.Close()
	c, err := net.FileConn(f)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := checkPeer(c, uint32(os.Getuid())); err != nil { //nolint:gosec // test
		t.Fatalf("own uid refused: %v", err)
	}
	if err := checkPeer(c, uint32(os.Getuid())+1); !errors.Is(err, errPeerUID) { //nolint:gosec // test
		t.Fatalf("err = %v, want errPeerUID", err)
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

func TestWorker_MountFlagsAreMandatory(t *testing.T) {
	d := okDeps(t)
	for name, args := range map[string][]string{
		"neither flag": {"--caller-uid", strconv.Itoa(os.Getuid())},
		"both flags":   {"--caller-uid", strconv.Itoa(os.Getuid()), "--require-mount", "/srv/x", "--no-mount-check"},
	} {
		if code := run(args, devNull(t), io.Discard, io.Discard, d); code != 2 {
			t.Errorf("%s: exit %d, want 2 (usage)", name, code)
		}
	}
}

func TestWorker_RefusesWhenMountCheckFails(t *testing.T) {
	d := okDeps(t)
	d.mountCheck = func(dir string) error { return fmt.Errorf("%s is the root fs", dir) }
	args := []string{"--caller-uid", strconv.Itoa(os.Getuid()), "--require-mount", filepath.Dir(d.home)}
	code, resp, _ := serveOnce(t, args, d, helloReq)
	if code == 0 || resp.Error == nil || resp.Error.Code != agentproto.CodeRefused {
		t.Fatalf("code=%d %+v", code, resp)
	}
	d.mountCheck = func(string) error { return nil }
	if code, resp, _ = serveOnce(t, args, d, helloReq); code != 0 || resp.Error != nil {
		t.Fatalf("passing mount refused: code=%d %+v", code, resp)
	}
}

func TestWorker_RefusesHomeOutsideTheMount(t *testing.T) {
	d := okDeps(t)
	other := t.TempDir()
	args := []string{"--caller-uid", strconv.Itoa(os.Getuid()), "--require-mount", other}
	code, resp, _ := serveOnce(t, args, d, helloReq)
	if code == 0 || resp.Error == nil || resp.Error.Code != agentproto.CodeRefused {
		t.Fatalf("home outside the mount served: code=%d %+v", code, resp)
	}
	if err := checkHomeUnder(d.home, other); !errors.Is(err, errHome) {
		t.Fatalf("err = %v, want errHome", err)
	}
	if err := os.MkdirAll(filepath.Join(other, "a", "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, home := range []string{other, filepath.Join(other, "a", "b")} {
		if err := checkHomeUnder(home, other); err != nil {
			t.Errorf("home %s under %s refused: %v", home, other, err)
		}
	}
	if err := checkHomeUnder(other+"-sibling", other); !errors.Is(err, errHome) {
		t.Errorf("sibling prefix accepted: %v", err)
	}
}

func TestWorker_CallerUIDMustFitAUint32(t *testing.T) {
	d := okDeps(t)
	for _, v := range []string{"4294967296", "99999999999", "-2"} {
		if code := run([]string{"--caller-uid", v, "--no-mount-check"}, devNull(t), io.Discard, io.Discard, d); code != 2 {
			t.Errorf("--caller-uid %s: exit %d, want 2", v, code)
		}
	}
}

func TestWorker_RefusesForeignGroups(t *testing.T) {
	cases := map[string]struct {
		egid   int
		groups []int
		ok     bool
	}{
		"agent gid":             {2001, nil, true},
		"agent gid and group":   {2001, []int{2001, 2002}, true},
		"root gid":              {0, nil, false},
		"ordinary gid":          {1002, nil, false},
		"foreign supplementary": {2001, []int{2001, 4}, false},
		"docker-like group":     {2001, []int{999}, false},
	}
	for name, tc := range cases {
		err := checkGIDs(tc.egid, tc.groups)
		if tc.ok != (err == nil) || (!tc.ok && !errors.Is(err, errGIDRange)) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	d := okDeps(t)
	d.getgroups = func() ([]int, error) { return []int{4}, nil }
	code, resp, _ := serveOnce(t, callerArg(os.Getuid()), d, helloReq)
	if code == 0 || resp.Error == nil || resp.Error.Code != agentproto.CodeRefused {
		t.Fatalf("foreign supplementary group served: code=%d %+v", code, resp)
	}
	d = okDeps(t)
	d.getegid = func() int { return 0 }
	if code, resp, _ = serveOnce(t, callerArg(os.Getuid()), d, helloReq); code == 0 || resp.Error == nil {
		t.Fatalf("root egid served: code=%d %+v", code, resp)
	}
}

// fakeChrt installs a chrt on PATH that records its argv, one per line, and
// then runs the rest of the command line like the real one.
func fakeChrt(t *testing.T) (rec string) {
	t.Helper()
	dir := t.TempDir()
	rec = filepath.Join(dir, "argv")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + rec + "'\nshift 2\nexec \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "chrt"), []byte(script), 0o755); err != nil { //nolint:gosec // test script
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return rec
}

const execReq = `{"v":2,"op":"exec","exec":{"command":"echo hi"}}`

func TestWorker_AgentCommandsRunUnderChrtIdle(t *testing.T) {
	rec := fakeChrt(t)
	d := okDeps(t)
	d.findCommand = exec.LookPath
	code, resp, _ := serveOnce(t, callerArg(os.Getuid()), d, execReq)
	if code != 0 || resp.Exec == nil || resp.Exec.Stdout != "hi\n" {
		t.Fatalf("code=%d resp=%+v exec=%+v", code, resp, resp.Exec)
	}
	b, err := os.ReadFile(rec)
	if err != nil {
		t.Fatalf("the command did not go through chrt: %v", err)
	}
	if got, want := string(b), "--idle\n0\n/bin/bash\n-c\necho hi\n"; got != want {
		t.Fatalf("chrt argv = %q, want %q", got, want)
	}
	_, resp, _ = serveOnce(t, callerArg(os.Getuid()), d, `{"v":2,"op":"status"}`)
	if resp.Status == nil || !strings.Contains(strings.Join(resp.Status.Notes, "|"), "idle CPU priority") {
		t.Fatalf("status does not report idle scheduling: %+v", resp.Status)
	}
}

func TestWorker_MissingChrtFailsExecUnlessNoIdle(t *testing.T) {
	d := okDeps(t) // findCommand fails
	code, resp, _ := serveOnce(t, callerArg(os.Getuid()), d, execReq)
	if code != 0 || resp.Exec != nil || resp.Error == nil || resp.Error.Code != agentproto.CodeOp {
		t.Fatalf("exec without chrt: code=%d resp=%+v", code, resp)
	}
	_, resp, _ = serveOnce(t, callerArg(os.Getuid()), d, `{"v":2,"op":"status"}`)
	if resp.Status == nil || !strings.Contains(strings.Join(resp.Status.Notes, "|"), "DISABLED") {
		t.Fatalf("status does not report disabled exec: %+v", resp.Status)
	}
	// Other operations keep working.
	if _, resp, _ = serveOnce(t, callerArg(os.Getuid()), d, `{"v":2,"op":"write_file","write_file":{"path":"f","text":"x"}}`); resp.Error != nil {
		t.Fatalf("write without chrt: %+v", resp.Error)
	}

	args := append(callerArg(os.Getuid()), "--no-idle")
	code, resp, _ = serveOnce(t, args, d, execReq)
	if code != 0 || resp.Exec == nil || resp.Exec.Stdout != "hi\n" {
		t.Fatalf("--no-idle: code=%d resp=%+v", code, resp)
	}
	_, resp, _ = serveOnce(t, args, d, `{"v":2,"op":"status"}`)
	if resp.Status == nil || !strings.Contains(strings.Join(resp.Status.Notes, "|"), "NORMAL") {
		t.Fatalf("status does not report normal priority: %+v", resp.Status)
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

func TestWorker_SealsInheritedDescriptors(t *testing.T) {
	fd, err := syscall.Open("/dev/null", syscall.O_RDONLY, 0) // inherited-style: no O_CLOEXEC
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(fd)
	if flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); err != nil || flags&unix.FD_CLOEXEC != 0 {
		t.Fatalf("setup: flags=%d err=%v", flags, err)
	}
	if code, resp, _ := serveOnce(t, callerArg(os.Getuid()), okDeps(t), helloReq); code != 0 || resp.Error != nil {
		t.Fatalf("code=%d %+v", code, resp)
	}
	if flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); err != nil || flags&unix.FD_CLOEXEC == 0 {
		t.Fatalf("the worker left descriptor %d inheritable: flags=%d err=%v", fd, flags, err)
	}
}
