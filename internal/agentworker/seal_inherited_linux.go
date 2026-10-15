//go:build linux

package agentworker

import "syscall"

const (
	sysCloseRange     = 436 // same number on every architecture Go supports on Linux
	closeRangeCloexec = 4   // CLOSE_RANGE_CLOEXEC
	closeRangeFirstFD = 3
	closeRangeLastFD  = ^uintptr(0) >> 32 // ~0U
)

// SealInherited marks every descriptor above stderr close-on-exec so that no
// child process can inherit one by accident, whatever started the worker.
func SealInherited() {
	if _, _, e := syscall.Syscall(sysCloseRange, closeRangeFirstFD, closeRangeLastFD, closeRangeCloexec); e == 0 {
		return
	}
	sealLoop()
}
