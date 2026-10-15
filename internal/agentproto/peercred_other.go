//go:build !linux && !darwin

package agentproto

import (
	"errors"
	"syscall"
)

var errUnsupported = errors.New("agentproto: peer credentials unsupported on this platform")

// PeerUID is unsupported off Linux/Darwin; callers fail closed.
func PeerUID(interface {
	SyscallConn() (syscall.RawConn, error)
}) (uint32, error) {
	return 0, errUnsupported
}
