//go:build !linux

package agentworker

// SealInherited marks every descriptor above stderr close-on-exec.
func SealInherited() { sealLoop() }
