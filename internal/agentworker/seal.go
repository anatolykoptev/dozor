package agentworker

import (
	"os"
	"syscall"
)

// seal marks the descriptors backing the connection close-on-exec so no
// child process can inherit it. Under socket activation the connection is
// fd 0/1; os/exec replaces 0-2 in the child, and this closes the remaining
// route (any other duplicate). Failure to seal is fatal to the call.
func seal(files ...*os.File) error {
	for _, f := range files {
		if f == nil {
			continue
		}
		rc, err := f.SyscallConn()
		if err != nil {
			return err
		}
		if err := rc.Control(func(fd uintptr) { syscall.CloseOnExec(int(fd)) }); err != nil {
			return err
		}
	}
	return nil
}

// sealLoop is the portable fallback of SealInherited.
func sealLoop() {
	const maxFD = 4096
	for fd := 3; fd < maxFD; fd++ {
		syscall.CloseOnExec(fd)
	}
}
