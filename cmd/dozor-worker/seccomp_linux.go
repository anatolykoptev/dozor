//go:build linux

package main

import (
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// ipprotoMPTCP is IPPROTO_MPTCP. Landlock's TCP rules do not cover Multipath
// TCP sockets (a kernel limitation), so a program that opens its socket with
// this protocol can bind any port. Go's net.Listen does so by default since
// Go 1.24. The filter below closes that door for the worker and everything it
// starts.
const ipprotoMPTCP = 262

// BPF seccomp_data offsets (little-endian, both supported architectures).
const (
	offNr      = 0
	offArch    = 4
	offArg2Low = 16 + 8*2 // socket(domain, type, protocol): protocol is an int

	// skipToAllow jumps over the three instructions between the socket check and the final ALLOW.
	skipToAllow = 3
)

// denyMPTCP installs a seccomp filter on every thread of the process that
// makes socket(_, _, IPPROTO_MPTCP) fail with EPROTONOSUPPORT, exactly what a
// kernel without MPTCP answers, so programs fall back to plain TCP. Every
// other call, and every other socket, is untouched. Syscalls of a foreign
// architecture are refused.
func denyMPTCP() error {
	if auditArch == 0 {
		return fmt.Errorf("seccomp filter: unsupported architecture %s", runtime.GOARCH)
	}
	ld := func(off uint32) unix.SockFilter {
		return unix.SockFilter{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: off}
	}
	jeq := func(k uint32, jt, jf uint8) unix.SockFilter {
		return unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jt: jt, Jf: jf, K: k}
	}
	ret := func(k uint32) unix.SockFilter { return unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: k} }
	prog := []unix.SockFilter{
		ld(offArch),
		jeq(auditArch, 1, 0),
		ret(unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)), //nolint:gosec // errno constant
		ld(offNr),
		jeq(uint32(unix.SYS_SOCKET), 0, skipToAllow), //nolint:gosec // syscall numbers are small positive constants
		ld(offArg2Low),
		jeq(ipprotoMPTCP, 0, 1),
		ret(unix.SECCOMP_RET_ERRNO | uint32(unix.EPROTONOSUPPORT)), //nolint:gosec // errno constant
		ret(unix.SECCOMP_RET_ALLOW),
	}
	fprog := unix.SockFprog{Len: uint16(len(prog)), Filter: &prog[0]} //nolint:gosec // nine instructions

	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("no_new_privs: %w", err)
	}
	// TSYNC applies the filter to every thread of the process.
	_, _, errno := unix.Syscall(unix.SYS_SECCOMP, unix.SECCOMP_SET_MODE_FILTER,
		unix.SECCOMP_FILTER_FLAG_TSYNC, uintptr(unsafe.Pointer(&fprog))) //nolint:gosec // the seccomp(2) ABI takes a pointer to the filter program
	runtime.KeepAlive(prog)
	if errno != 0 {
		return fmt.Errorf("seccomp: %w", errno)
	}
	return nil
}
