//go:build unix

package keyfile

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// openNoFollow opens path read-only without following a final symlink and
// without blocking on a FIFO.
func openNoFollow(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, syscall.ELOOP) {
		return nil, &loadError{"key file is a symlink"}
	}
	return f, err
}

// sigMeta extracts the inode and ctime that identify a file version.
func sigMeta(fi fs.FileInfo) (ino uint64, ctimeNS int64) {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Ino), ctimeNanos(st) //nolint:unconvert // width differs per platform
	}
	return 0, 0
}

// checkFile refuses a key file that is not a plain, privately writable file
// owned by the running user or root. It runs on the already-open descriptor.
func (s *Store) checkFile(fi fs.FileInfo) string {
	if !fi.Mode().IsRegular() {
		return "key file is not a regular file"
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return "key file is group- or world-writable"
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	if uid := int(st.Uid); uid != 0 && uid != s.euid() {
		return "key file has an untrusted owner"
	}
	return ""
}

// checkParent refuses a parent directory that other users can write to, unless
// it has the sticky bit (as /tmp does).
func checkParent(path string) (string, error) {
	fi, err := os.Stat(filepath.Dir(path))
	if err != nil {
		return "", err
	}
	if fi.Mode().Perm()&0o022 != 0 && fi.Mode()&os.ModeSticky == 0 {
		return kindParentWritable, nil
	}
	return "", nil
}
