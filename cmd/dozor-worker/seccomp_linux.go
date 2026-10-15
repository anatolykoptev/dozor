//go:build linux

package main

import (
	"fmt"
	"math"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// ipprotoMPTCP is IPPROTO_MPTCP. Landlock's TCP rules do not cover Multipath
// TCP sockets (a kernel limitation), so a program that opens its socket with
// this protocol can bind any port. Go's net.Listen does so by default since
// Go 1.24.
const ipprotoMPTCP = 262

// x32Bit marks x32-ABI syscall numbers on amd64: the same calls under other
// numbers, which a filter written for the native numbers would not match.
const x32Bit = 0x40000000

// BPF seccomp_data offsets (little-endian, both supported architectures).
const (
	offNr      = 0
	offArch    = 4
	offArg2Low = 16 + 8*2 // socket(domain, type, protocol): protocol is an int
)

// ins is one filter instruction; jt and jf name labels, "" meaning "next".
type ins struct {
	code   uint16
	k      uint32
	jt, jf string
	label  string
}

const (
	lblNext    = ""
	lblEPERM   = "eperm"
	lblNoProto = "noproto"
	lblAllow   = "allow"
)

// assemble resolves labels to relative jumps.
func assemble(prog []ins) ([]unix.SockFilter, error) {
	at := map[string]int{}
	for i, p := range prog {
		if p.label != "" {
			at[p.label] = i
		}
	}
	jump := func(i int, name string) (uint8, error) {
		if name == lblNext {
			return 0, nil
		}
		to, ok := at[name]
		if !ok || to <= i {
			return 0, fmt.Errorf("seccomp: bad jump target %q at %d", name, i)
		}
		if to-i-1 > math.MaxUint8 {
			return 0, fmt.Errorf("seccomp: jump too far at %d", i)
		}
		return uint8(to - i - 1), nil //nolint:gosec // checked above
	}
	out := make([]unix.SockFilter, len(prog))
	for i, p := range prog {
		jt, err := jump(i, p.jt)
		if err != nil {
			return nil, err
		}
		jf, err := jump(i, p.jf)
		if err != nil {
			return nil, err
		}
		out[i] = unix.SockFilter{Code: p.code, Jt: jt, Jf: jf, K: p.k}
	}
	return out, nil
}

func errno(e unix.Errno) uint32 { return unix.SECCOMP_RET_ERRNO | uint32(e) } //nolint:gosec // errno constants are small

// buildFilter assembles the filter for the native architecture:
//
//   - socket(_, _, IPPROTO_MPTCP) fails with EPROTONOSUPPORT, what a kernel
//     without MPTCP answers, so programs fall back to plain TCP, which
//     Landlock does cover;
//   - io_uring_setup, io_uring_enter and io_uring_register fail with EPERM:
//     IORING_OP_SOCKET creates sockets without going through socket(2);
//   - x32 syscall numbers and syscalls of a foreign architecture fail with EPERM.
//
// Every other call is allowed.
func buildFilter() ([]unix.SockFilter, error) {
	if auditArch == 0 {
		return nil, fmt.Errorf("seccomp filter: unsupported architecture %s", runtime.GOARCH)
	}
	ld := func(off uint32) ins { return ins{code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, k: off} }
	jeq := func(k uint32, jt, jf string) ins {
		return ins{code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, k: k, jt: jt, jf: jf}
	}
	return assemble([]ins{
		ld(offArch),
		jeq(auditArch, lblNext, lblEPERM),
		ld(offNr),
		{code: unix.BPF_JMP | unix.BPF_JSET | unix.BPF_K, k: x32Bit, jt: lblEPERM},
		jeq(unix.SYS_IO_URING_SETUP, lblEPERM, lblNext),
		jeq(unix.SYS_IO_URING_ENTER, lblEPERM, lblNext),
		jeq(unix.SYS_IO_URING_REGISTER, lblEPERM, lblNext),
		jeq(unix.SYS_SOCKET, lblNext, lblAllow),
		ld(offArg2Low),
		jeq(ipprotoMPTCP, lblNoProto, lblAllow),
		{label: lblNoProto, code: unix.BPF_RET | unix.BPF_K, k: errno(unix.EPROTONOSUPPORT)},
		{label: lblEPERM, code: unix.BPF_RET | unix.BPF_K, k: errno(unix.EPERM)},
		{label: lblAllow, code: unix.BPF_RET | unix.BPF_K, k: unix.SECCOMP_RET_ALLOW},
	})
}

// denySockets installs the filter on every thread of the process (and so on
// everything it starts).
func denySockets() error {
	prog, err := buildFilter()
	if err != nil {
		return err
	}
	fprog := unix.SockFprog{Len: uint16(len(prog)), Filter: &prog[0]} //nolint:gosec // a few instructions

	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("no_new_privs: %w", err)
	}
	// TSYNC applies the filter to every thread of the process.
	_, _, e := unix.Syscall(unix.SYS_SECCOMP, unix.SECCOMP_SET_MODE_FILTER,
		unix.SECCOMP_FILTER_FLAG_TSYNC, uintptr(unsafe.Pointer(&fprog))) //nolint:gosec // the seccomp(2) ABI takes a pointer to the filter program
	runtime.KeepAlive(prog)
	if e != 0 {
		return fmt.Errorf("seccomp: %w", e)
	}
	return nil
}
