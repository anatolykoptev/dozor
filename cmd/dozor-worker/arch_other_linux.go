//go:build linux && !amd64 && !arm64

package main

// auditArch is 0 where no seccomp filter is provided; denyMPTCP then fails
// and the worker fails closed.
const auditArch = 0
