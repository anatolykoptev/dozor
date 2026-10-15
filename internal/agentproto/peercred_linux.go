//go:build linux

package agentproto

import (
	"syscall"
)

// PeerUID returns the uid of the process that created the peer end of a
// connected unix socket (SO_PEERCRED). For a socket-activated listener the
// peer credentials are those of the activator, not of whoever connects.
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
		cred, e := syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
		if e != nil {
			gerr = e
			return
		}
		uid = cred.Uid
	}); err != nil {
		return 0, err
	}
	if gerr != nil {
		return 0, gerr
	}
	return uid, nil
}
