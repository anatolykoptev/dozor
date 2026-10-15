//go:build darwin || freebsd || netbsd

package keyfile

import "syscall"

func ctimeNanos(st *syscall.Stat_t) int64 { return st.Ctimespec.Nano() }
