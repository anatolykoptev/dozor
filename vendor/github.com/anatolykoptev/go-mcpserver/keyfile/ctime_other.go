//go:build unix && !linux && !darwin && !freebsd && !netbsd

package keyfile

import "syscall"

// ctime is not exposed uniformly on this platform; inode, mtime and size still
// drive reloads.
func ctimeNanos(*syscall.Stat_t) int64 { return 0 }
