//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Landlock and seccomp are irreversible, so the real restriction is exercised
// in a subprocess: the test binary re-runs itself as a helper, which restricts
// itself and then probes, from several OS threads (some created BEFORE the
// restriction, which only a thread-synchronised install reaches) and from a
// child process.

const (
	helperEnv   = "DOZOR_WORKER_LANDLOCK_HELPER"
	helperPort  = "DOZOR_WORKER_LANDLOCK_CONNECT_PORT"
	helperRange = "DOZOR_WORKER_LANDLOCK_RANGE"
	ipprotoTCP  = 0
	tcpSockType = syscall.SOCK_STREAM
	probeThread = 4
)

func errName(err error) string {
	if err == nil {
		return ""
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno.Error()
	}
	return err.Error()
}

func loopback(port int) *syscall.SockaddrInet4 {
	return &syscall.SockaddrInet4{Port: port, Addr: [4]byte{127, 0, 0, 1}}
}

// bindTCP binds a plain (classic) TCP socket. It uses the raw syscall on
// purpose: Go's net.Listen opens Multipath TCP, which Landlock does not cover.
func bindTCP(proto, port int) (string, int) {
	fd, err := syscall.Socket(syscall.AF_INET, tcpSockType, proto)
	if err != nil {
		return "socket:" + errName(err), 0
	}
	defer syscall.Close(fd)
	if err := syscall.Bind(fd, loopback(port)); err != nil {
		return errName(err), 0
	}
	sa, _ := syscall.Getsockname(fd)
	if in, ok := sa.(*syscall.SockaddrInet4); ok {
		return "", in.Port
	}
	return "", 0
}

// listenAutobind listens on an unbound socket: the kernel binds an ephemeral
// port. It returns the error name and the port it got.
func listenAutobind() (string, int) {
	fd, err := syscall.Socket(syscall.AF_INET, tcpSockType, ipprotoTCP)
	if err != nil {
		return "socket:" + errName(err), 0
	}
	defer syscall.Close(fd)
	if err := syscall.Listen(fd, 1); err != nil {
		return errName(err), 0
	}
	sa, _ := syscall.Getsockname(fd)
	if in, ok := sa.(*syscall.SockaddrInet4); ok {
		return "", in.Port
	}
	return "", 0
}

// ioUringSetup calls io_uring_setup(1, &params).
func ioUringSetup() string {
	var params [120]byte // sizeof(struct io_uring_params)
	fd, _, e := syscall.Syscall(unix.SYS_IO_URING_SETUP, 1, uintptr(unsafe.Pointer(&params[0])), 0)
	runtime.KeepAlive(params)
	if e != 0 {
		return e.Error()
	}
	_ = syscall.Close(int(fd))
	return ""
}

// probeAll is what one thread or process reports.
func probeAll() map[string]string {
	res := map[string]string{}
	for _, p := range []int{2019, 8765, 5432, 6379} {
		res["bind:"+strconv.Itoa(p)], _ = bindTCP(ipprotoTCP, p)
	}
	res["mptcp:2019"], _ = bindTCP(262, 2019)
	res["mptcp:0"], _ = bindTCP(262, 0)
	res["uring"] = ioUringSetup()
	return res
}

func TestLandlockHelper(t *testing.T) {
	switch os.Getenv(helperEnv) {
	case "":
		t.Skip("subprocess helper; run by TestLandlock_*")
	case "probe":
		_ = json.NewEncoder(os.Stdout).Encode(probeAll())
		os.Exit(0)
	case "restrict":
		helperRestrict(t)
	}
}

// helperRestrict runs inside the subprocess.
func helperRestrict(t *testing.T) {
	lo, hi, err := parsePortRange(os.Getenv(helperRange))
	if err != nil {
		t.Fatal(err)
	}

	// Threads that exist before the restriction: each goroutine pins its own
	// OS thread and waits.
	var ready, done sync.WaitGroup
	gate := make(chan struct{})
	results := make([]map[string]string, probeThread)
	for i := range results {
		ready.Add(1)
		done.Add(1)
		go func() {
			defer done.Done()
			runtime.LockOSThread()
			ready.Done()
			<-gate
			results[i] = probeAll()
		}()
	}
	ready.Wait()

	if err := restrictBindTCP(lo, hi); err != nil {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"restrict": err.Error()})
		os.Exit(0)
	}
	close(gate)
	done.Wait()

	main := probeAll()
	main["bind:below"], _ = bindTCP(ipprotoTCP, int(lo)-1)
	main["bind:above"], _ = bindTCP(ipprotoTCP, int(hi)+1)
	main["bind:in-range"] = "no free port in range"
	for p := int(lo); p <= int(hi); p++ {
		if r, _ := bindTCP(ipprotoTCP, p); r == "" {
			main["bind:in-range"] = ""
			break
		}
	}
	var port int
	main["bind:0"], port = bindTCP(ipprotoTCP, 0)
	main["bind:0-port"] = strconv.Itoa(port)
	main["listen:autobind"], port = listenAutobind()
	main["listen:autobind-port"] = strconv.Itoa(port)

	// A descendant inherits both filters.
	child := exec.CommandContext(context.Background(), os.Args[0], "-test.run=^TestLandlockHelper$")
	child.Env = append(os.Environ(), helperEnv+"=probe")
	if b, cerr := child.Output(); cerr != nil {
		main["child"] = "child failed: " + cerr.Error()
	} else {
		var cres map[string]string
		if err := json.Unmarshal(b, &cres); err != nil {
			main["child"] = "child output: " + string(b)
		} else {
			for k, v := range cres {
				main["child:"+k] = v
			}
		}
	}
	for i, r := range results {
		for k, v := range r {
			main["thread"+strconv.Itoa(i)+":"+k] = v
		}
	}

	// Outgoing connect, including the kernel's implicit bind for the source
	// port, is not restricted.
	conn, derr := (&net.Dialer{}).DialContext(context.Background(), "tcp", "127.0.0.1:"+os.Getenv(helperPort))
	if derr != nil {
		main["connect"] = derr.Error()
	} else {
		main["connect"] = ""
		_ = conn.Close()
	}
	main["restrict"] = ""
	_ = json.NewEncoder(os.Stdout).Encode(main)
	os.Exit(0)
}

func runRestrictHelper(t *testing.T, portRange string) map[string]string {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())

	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestLandlockHelper$")
	cmd.Env = append(os.Environ(), helperEnv+"=restrict", helperPort+"="+port, helperRange+"="+portRange)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("helper: %v\n%s", err, out)
	}
	var res map[string]string
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("helper output %q: %v", out, err)
	}
	if msg := res["restrict"]; msg != "" {
		if os.Getenv("DOZOR_REQUIRE_LANDLOCK") != "" {
			t.Fatalf("Landlock ABI 4 is required here but unavailable: %s", msg)
		}
		t.Skipf("Landlock ABI 4 unavailable on this kernel: %s", msg)
	}
	return res
}

const (
	eacces      = "permission denied"
	eperm       = "operation not permitted"
	noMPTCP     = "socket:protocol not supported"
	srcMain     = ""
	srcChild    = "child:"
	srcThreadFn = "thread"
)

// sources are every place a probe ran: the main thread, the child process, and
// the OS threads created before the restriction.
func sources() []string {
	s := []string{srcMain, srcChild}
	for i := 0; i < probeThread; i++ {
		s = append(s, srcThreadFn+strconv.Itoa(i)+":")
	}
	return s
}

// RED-on-revert: remove the RestrictNet call in restrictBindTCP and every
// outside-the-range bind succeeds; widen the range and the edge ports do.
func TestLandlock_BindOutsideTheRangeIsDenied(t *testing.T) {
	res := runRestrictHelper(t, defaultBindPorts)
	for _, src := range sources() {
		for _, k := range []string{"bind:2019", "bind:8765", "bind:5432", "bind:6379"} {
			if got := res[src+k]; got != eacces {
				t.Errorf("%s%s = %q, want %q", src, k, got, eacces)
			}
		}
	}
	for _, k := range []string{"bind:below", "bind:above"} { // 29999, 31000
		if res[k] != eacces {
			t.Errorf("%s = %q, want %q", k, res[k], eacces)
		}
	}
	if res["bind:in-range"] != "" {
		t.Errorf("no port in %s could be bound: %q", defaultBindPorts, res["bind:in-range"])
	}
}

// Connecting out still works, with the kernel's implicit source-port bind.
func TestLandlock_ConnectIsNotRestricted(t *testing.T) {
	if res := runRestrictHelper(t, defaultBindPorts); res["connect"] != "" {
		t.Errorf("connect to a local listener failed: %q", res["connect"])
	}
}

// Port 0 and listen() autobind take a kernel-chosen ephemeral port; tools
// that need a free port must keep working, and no reserved port can be
// reached this way.
func TestLandlock_EphemeralBindAndAutobindWork(t *testing.T) {
	res := runRestrictHelper(t, defaultBindPorts)
	for _, k := range []string{"bind:0", "listen:autobind"} {
		if res[k] != "" {
			t.Errorf("%s = %q, want success", k, res[k])
		}
	}
	for _, k := range []string{"bind:0-port", "listen:autobind-port"} {
		p, _ := strconv.Atoi(res[k])
		if p <= 1024 {
			t.Errorf("%s = %d, want an ephemeral port", k, p)
		}
		switch p {
		case 2019, 8765, 5432, 6379:
			t.Errorf("%s = %d is a service port", k, p)
		}
	}
}

// Landlock does not cover Multipath TCP sockets (a kernel limitation), so the
// worker also refuses socket(IPPROTO_MPTCP) — in the process, in OS threads
// that existed before the filter (SECCOMP_FILTER_FLAG_TSYNC), and in
// descendants. RED-on-revert: drop denySockets() from restrictBindTCP, or
// replace the TSYNC flag with 0 in denySockets (the earlier threads keep
// their unfiltered sockets).
func TestLandlock_MultipathTCPSocketIsRefusedEverywhere(t *testing.T) {
	res := runRestrictHelper(t, defaultBindPorts)
	for _, src := range sources() {
		for _, k := range []string{"mptcp:2019", "mptcp:0"} {
			if got := res[src+k]; got != noMPTCP {
				t.Errorf("%s%s = %q, want %q", src, k, got, noMPTCP)
			}
		}
	}
}

// IORING_OP_SOCKET would create sockets without socket(2), around the MPTCP
// filter; io_uring is refused with EPERM everywhere.
func TestLandlock_IOUringIsRefusedEverywhere(t *testing.T) {
	res := runRestrictHelper(t, defaultBindPorts)
	for _, src := range sources() {
		if got := res[src+"uring"]; got != eperm {
			t.Errorf("%suring = %q, want %q", src, got, eperm)
		}
	}
}
