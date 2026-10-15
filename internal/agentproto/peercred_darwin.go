//go:build darwin

package agentproto

import (
	"syscall"

	"golang.org/x/sys/unix"
)

// PeerUID returns the uid of the peer of a connected unix socket
// (LOCAL_PEERCRED). Darwin exists only so developers can build and test
// locally; the worker is deployed on Linux.
func PeerUID(c interface {
	SyscallConn() (syscall.RawConn, error)
}) (uint32, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return 0, err
	}
	var (
		uid  uint32
		gerr error
	)
	if err := raw.Control(func(fd uintptr) {
		x, e := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if e != nil {
			gerr = e
			return
		}
		uid = x.Uid
	}); err != nil {
		return 0, err
	}
	if gerr != nil {
		return 0, gerr
	}
	return uid, nil
}
