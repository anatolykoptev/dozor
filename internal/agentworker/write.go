package agentworker

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/anatolykoptev/dozor/internal/agentproto"
)

const (
	defaultWriteMode = 0o600
	parentDirMode    = 0o700
)

// WriteFile creates or replaces a file atomically: the bytes go to a temp
// file in the destination directory, are fsynced, and only then renamed (or,
// for overwrite=false, hard-linked, which fails if the destination exists)
// into place. A reader never observes a partial file, and a failure leaves
// the previous content untouched.
func (c *Config) WriteFile(req *agentproto.WriteFileRequest) (*agentproto.WriteFileResult, *agentproto.Error) {
	if req == nil {
		return nil, badReq("write_file is required")
	}
	path, err := c.resolve(req.Path)
	if err != nil {
		return nil, badReq(err.Error())
	}
	data, perr := payload(req)
	if perr != nil {
		return nil, perr
	}
	mode, err := parseMode(req.Mode)
	if err != nil {
		return nil, badReq(err.Error())
	}
	dir := filepath.Dir(path)
	if req.CreateParents {
		if err := os.MkdirAll(dir, parentDirMode); err != nil {
			return nil, opErr(err)
		}
	}
	if err := c.commit(dir, path, data, mode, req.Overwrite); err != nil {
		return nil, opErr(err)
	}
	return &agentproto.WriteFileResult{Path: path, Bytes: int64(len(data)), Mode: fmt.Sprintf("%04o", mode)}, nil
}

func payload(req *agentproto.WriteFileRequest) ([]byte, *agentproto.Error) {
	if (req.Text == nil) == (req.Base64 == nil) {
		return nil, badReq("exactly one of text and base64 is required")
	}
	if req.Text != nil {
		if len(*req.Text) > agentproto.MaxWriteBytes {
			return nil, badReq("content too large")
		}
		return []byte(*req.Text), nil
	}
	if base64.StdEncoding.DecodedLen(len(*req.Base64)) > agentproto.MaxWriteBytes+2 {
		return nil, badReq("content too large")
	}
	data, err := base64.StdEncoding.DecodeString(*req.Base64)
	if err != nil {
		return nil, badReq("invalid base64")
	}
	if len(data) > agentproto.MaxWriteBytes {
		return nil, badReq("content too large")
	}
	return data, nil
}

func parseMode(s string) (os.FileMode, error) {
	if s == "" {
		return defaultWriteMode, nil
	}
	v, err := strconv.ParseUint(s, 8, 32)
	if err != nil || v > 0o777 {
		return 0, errors.New("mode must be an octal permission, at most 0777")
	}
	return os.FileMode(v), nil
}

func (c *Config) commit(dir, path string, data []byte, mode os.FileMode, overwrite bool) (err error) {
	tmp, err := os.CreateTemp(dir, ".agent-write-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()
	if _, err = tmp.Write(data); err != nil {
		return err
	}
	if err = tmp.Chmod(mode); err != nil {
		return err
	}
	if c.beforeCommit != nil {
		if err = c.beforeCommit(); err != nil {
			return err
		}
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if overwrite {
		if err = os.Rename(tmpName, path); err != nil {
			return err
		}
	} else {
		if err = os.Link(tmpName, path); err != nil {
			return err
		}
		_ = os.Remove(tmpName)
	}
	syncDir(dir)
	return nil
}

// syncDir makes the rename durable; failure is not actionable.
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}
