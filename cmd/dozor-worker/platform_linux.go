//go:build linux

package main

import (
	"os"
	"syscall"
)

func readProcStatus() (string, int, error) {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return "", 0, err
	}
	return parseProcStatus(string(b))
}

// checkMount requires dir to be on a different device from "/" and to be
// ext4, so a failed mount can never let the agent write onto the root
// filesystem outside its quota.
func checkMount(dir string) error {
	var root, sub syscall.Stat_t
	if err := syscall.Stat("/", &root); err != nil {
		return err
	}
	if err := syscall.Stat(dir, &sub); err != nil {
		return err
	}
	var fs syscall.Statfs_t
	if err := syscall.Statfs(dir, &fs); err != nil {
		return err
	}
	return mountOK(uint64(root.Dev), uint64(sub.Dev), int64(fs.Type)) //nolint:unconvert // Dev/Type widths differ per arch
}
