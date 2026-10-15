//go:build !unix

package keyfile

import (
	"io/fs"
	"os"
)

// Off unix there is no portable owner or permission-bit model; only the
// regular-file check applies.
func openNoFollow(path string) (*os.File, error) { return os.Open(path) }

func sigMeta(fs.FileInfo) (uint64, int64) { return 0, 0 }

func (s *Store) checkFile(fi fs.FileInfo) string {
	if !fi.Mode().IsRegular() {
		return "key file is not a regular file"
	}
	return ""
}

func checkParent(string) (string, error) { return "", nil }
