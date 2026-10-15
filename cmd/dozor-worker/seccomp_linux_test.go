//go:build linux

package main

import (
	"encoding/binary"
	"testing"

	"golang.org/x/sys/unix"
)

// runFilter interprets the classic-BPF subset the filter uses against a
// synthetic seccomp_data and returns the verdict (SECCOMP_RET_*).
func runFilter(t *testing.T, prog []unix.SockFilter, arch uint32, nr int32, args [6]uint64) uint32 {
	t.Helper()
	data := make([]byte, 64)
	binary.LittleEndian.PutUint32(data[0:], uint32(nr)) //nolint:gosec // synthetic data
	binary.LittleEndian.PutUint32(data[4:], arch)
	for i, a := range args {
		binary.LittleEndian.PutUint64(data[16+8*i:], a)
	}
	var acc uint32
	for pc := 0; pc < len(prog); pc++ {
		in := prog[pc]
		switch in.Code {
		case unix.BPF_LD | unix.BPF_W | unix.BPF_ABS:
			acc = binary.LittleEndian.Uint32(data[in.K:])
		case unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K:
			if acc == in.K {
				pc += int(in.Jt)
			} else {
				pc += int(in.Jf)
			}
		case unix.BPF_JMP | unix.BPF_JSET | unix.BPF_K:
			if acc&in.K != 0 {
				pc += int(in.Jt)
			} else {
				pc += int(in.Jf)
			}
		case unix.BPF_RET | unix.BPF_K:
			return in.K
		default:
			t.Fatalf("instruction %d: unsupported opcode %#x", pc, in.Code)
		}
	}
	t.Fatal("filter fell off the end")
	return 0
}

// The filter's decisions, checked by interpreting the real program on any
// architecture (the x32 case cannot be exercised on a live arm64 host).
// RED-on-revert: drop the JSET x32 test, an io_uring comparison, or the MPTCP
// comparison from buildFilter.
func TestFilter_Decisions(t *testing.T) {
	prog, err := buildFilter()
	if err != nil {
		t.Fatal(err)
	}
	allow := uint32(unix.SECCOMP_RET_ALLOW)
	eperm := uint32(unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM))
	noproto := uint32(unix.SECCOMP_RET_ERRNO | uint32(unix.EPROTONOSUPPORT))
	sock := func(proto uint64) [6]uint64 { return [6]uint64{2, 1, proto} }

	for name, c := range map[string]struct {
		arch uint32
		nr   int32
		args [6]uint64
		want uint32
	}{
		"tcp socket":            {auditArch, unix.SYS_SOCKET, sock(0), allow},
		"tcp socket explicit":   {auditArch, unix.SYS_SOCKET, sock(6), allow},
		"mptcp socket":          {auditArch, unix.SYS_SOCKET, sock(262), noproto},
		"mptcp, junk high bits": {auditArch, unix.SYS_SOCKET, sock(262 | 1<<40), noproto},
		"io_uring_setup":        {auditArch, unix.SYS_IO_URING_SETUP, [6]uint64{}, eperm},
		"io_uring_enter":        {auditArch, unix.SYS_IO_URING_ENTER, [6]uint64{}, eperm},
		"io_uring_register":     {auditArch, unix.SYS_IO_URING_REGISTER, [6]uint64{}, eperm},
		"x32 socket":            {auditArch, unix.SYS_SOCKET | x32Bit, sock(262), eperm},
		"x32 getpid":            {auditArch, unix.SYS_GETPID | x32Bit, [6]uint64{}, eperm},
		"foreign arch":          {unix.AUDIT_ARCH_I386, unix.SYS_GETPID, [6]uint64{}, eperm},
		"getpid":                {auditArch, unix.SYS_GETPID, [6]uint64{}, allow},
		"bind":                  {auditArch, unix.SYS_BIND, [6]uint64{}, allow},
		"connect":               {auditArch, unix.SYS_CONNECT, [6]uint64{}, allow},
	} {
		if got := runFilter(t, prog, c.arch, c.nr, c.args); got != c.want {
			t.Errorf("%s: verdict %#x, want %#x", name, got, c.want)
		}
	}
}
