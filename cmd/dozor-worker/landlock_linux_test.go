//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
)

// Landlock is irreversible, so the real restriction is exercised in a
// subprocess: the test binary re-runs itself as a helper, which restricts
// itself and then probes the network, and starts a grandchild for the
// "descendants are restricted too" case.

const (
	helperEnv   = "DOZOR_WORKER_LANDLOCK_HELPER"
	helperPort  = "DOZOR_WORKER_LANDLOCK_CONNECT_PORT"
	helperRange = "DOZOR_WORKER_LANDLOCK_RANGE"
)

// bindProbe returns the errno name of bind(127.0.0.1:port) on a plain TCP
// socket ("" on success). It uses the raw syscall on a classic TCP socket on
// purpose: Go's net.Listen would use Multipath TCP, which Landlock does not
// cover (a kernel limitation).
func bindProbe(port int) string {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
	if err != nil {
		return "socket:" + err.Error()
	}
	defer syscall.Close(fd)
	err = syscall.Bind(fd, &syscall.SockaddrInet4{Port: port, Addr: [4]byte{127, 0, 0, 1}})
	if err == nil {
		return ""
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno.Error()
	}
	return err.Error()
}

func TestLandlockHelper(t *testing.T) {
	switch os.Getenv(helperEnv) {
	case "":
		t.Skip("subprocess helper; run by TestLandlock_*")
	case "probe":
		port, _ := strconv.Atoi(os.Getenv("PROBE_PORT"))
		_, _ = os.Stdout.WriteString(bindProbe(port))
		os.Exit(0)
	case "restrict":
		lo, hi, err := parsePortRange(os.Getenv(helperRange))
		if err != nil {
			t.Fatal(err)
		}
		if err := restrictBindTCP(lo, hi); err != nil {
			_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"restrict": err.Error()})
			os.Exit(0)
		}
		res := map[string]string{"restrict": ""}
		for _, p := range []int{2019, 8765, 5432, 6379, int(lo) - 1, int(hi) + 1, 0} {
			res["bind:"+strconv.Itoa(p)] = bindProbe(p)
		}
		res["bind:in-range"] = "no free port in range"
		for p := int(lo); p <= int(hi); p++ {
			if r := bindProbe(p); r == "" {
				res["bind:in-range"] = ""
				break
			}
		}
		res["mptcp:2019"] = bindMPTCP(2019)
		// A descendant inherits the restriction.
		child := exec.CommandContext(context.Background(), os.Args[0], "-test.run=^TestLandlockHelper$")
		child.Env = append(os.Environ(), helperEnv+"=probe", "PROBE_PORT=2019")
		b, cerr := child.Output()
		if cerr != nil {
			res["child:2019"] = "child failed: " + cerr.Error()
		} else {
			res["child:2019"] = string(b)
		}
		// Outgoing connect, including the kernel's implicit bind for the
		// source port, is not restricted.
		conn, derr := (&net.Dialer{}).DialContext(context.Background(), "tcp", "127.0.0.1:"+os.Getenv(helperPort))
		if derr != nil {
			res["connect"] = derr.Error()
		} else {
			res["connect"] = ""
			_ = conn.Close()
		}
		_ = json.NewEncoder(os.Stdout).Encode(res)
		os.Exit(0)
	}
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

const eacces = "permission denied"

// RED-on-revert: remove the RestrictNet call in restrictBindTCP and every
// outside-the-range bind succeeds; widen the range and the edge ports do.
func TestLandlock_BindOutsideTheRangeIsDenied(t *testing.T) {
	res := runRestrictHelper(t, defaultBindPorts)
	for _, k := range []string{"bind:2019", "bind:8765", "bind:5432", "bind:6379", "bind:29999", "bind:31000", "child:2019"} {
		if res[k] != eacces {
			t.Errorf("%s = %q, want %q", k, res[k], eacces)
		}
	}
	if res["bind:in-range"] != "" {
		t.Errorf("no port in 30000-30999 could be bound: %q", res["bind:in-range"])
	}
}

// Connecting out still works, with the kernel's implicit source-port bind.
func TestLandlock_ConnectIsNotRestricted(t *testing.T) {
	if res := runRestrictHelper(t, defaultBindPorts); res["connect"] != "" {
		t.Errorf("connect to a local listener failed: %q", res["connect"])
	}
}

// bind(0) asks the kernel for an ephemeral port. It is not in the range, so
// it is denied; the agent's own servers use the reserved range.
func TestLandlock_EphemeralBindIsDenied(t *testing.T) {
	if res := runRestrictHelper(t, defaultBindPorts); res["bind:0"] != eacces {
		t.Errorf("bind(0) = %q, want %q", res["bind:0"], eacces)
	}
}

// bindMPTCP binds a Multipath TCP socket (IPPROTO_MPTCP = 262).
func bindMPTCP(port int) string {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 262)
	if err != nil {
		return "socket:" + err.Error()
	}
	defer syscall.Close(fd)
	if err := syscall.Bind(fd, &syscall.SockaddrInet4{Port: port, Addr: [4]byte{127, 0, 0, 1}}); err != nil {
		return err.Error()
	}
	return ""
}

// Landlock does not cover Multipath TCP sockets (a kernel limitation), so the
// worker also denies socket(IPPROTO_MPTCP). RED-on-revert: drop denyMPTCP()
// from restrictBindTCP and an MPTCP socket binds port 2019.
func TestLandlock_MultipathTCPSocketIsRefused(t *testing.T) {
	got := runRestrictHelper(t, defaultBindPorts)["mptcp:2019"]
	if got != "socket:protocol not supported" {
		t.Errorf("mptcp bind of 2019 = %q, want the socket refused", got)
	}
}
