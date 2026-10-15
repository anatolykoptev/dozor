// Package agentworker implements the operations of the per-identity agent
// worker: run a shell command, read a file, write a file atomically, report
// status. It runs as the agent's own uid inside the worker process, so every
// operation is bounded by that uid; nothing here is a security boundary on
// its own. It depends only on agentproto and the standard library.
package agentworker

import (
	"errors"
	"path/filepath"
	"strings"

	"github.com/anatolykoptev/dozor/internal/agentproto"
)

// Config carries the worker's environment. The zero value is not usable;
// Home is required.
type Config struct {
	// Home is the agent's home directory; relative paths resolve against it.
	Home string
	// User is exported as USER to children (optional).
	User string
	// ExecPrefix is prepended to every command's argv, e.g. {"chrt","--idle","0"}
	// so children run at idle CPU priority while the worker keeps normal
	// priority.
	ExecPrefix []string
	// ExecUnavailable, when set, makes every exec fail with this message. The
	// worker sets it when idle scheduling is required but cannot be provided,
	// so a missing chrt is an error, never a silent change of priority.
	ExecUnavailable string
	// Notes are appended to the status reply (e.g. the exec CPU priority).
	Notes []string
	// CgroupRoot and ProcCgroup locate the cgroup v2 files for agent_status.
	// Defaults: /sys/fs/cgroup and /proc/self/cgroup.
	CgroupRoot string
	ProcCgroup string

	// beforeCommit is a test hook run after the temp file is fully written
	// and before it replaces the destination.
	beforeCommit func() error
}

func (c *Config) validate() error {
	if c.Home == "" || !filepath.IsAbs(c.Home) {
		return errors.New("agentworker: Home must be an absolute path")
	}
	return nil
}

// cleanEnv is the whole environment of a child: the caller cannot inject any.
func (c *Config) cleanEnv() []string {
	path := strings.Join([]string{
		filepath.Join(c.Home, ".local/bin"),
		filepath.Join(c.Home, "go/bin"),
		filepath.Join(c.Home, ".cargo/bin"),
		filepath.Join(c.Home, ".npm-global/bin"),
		"/usr/local/bin", "/usr/bin", "/bin",
	}, ":")
	env := []string{
		"HOME=" + c.Home,
		"LANG=C.UTF-8",
		"TMPDIR=/tmp",
		"PATH=" + path,
	}
	if c.User != "" {
		env = append(env, "USER="+c.User, "LOGNAME="+c.User)
	}
	return env
}

// resolve turns a caller path into an absolute, cleaned path.
func (c *Config) resolve(p string) (string, error) {
	if p == "" {
		return "", errors.New("path is required")
	}
	if len(p) > agentproto.MaxPathBytes {
		return "", errors.New("path too long")
	}
	if strings.ContainsRune(p, 0) {
		return "", errors.New("path contains NUL")
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(c.Home, p)
	}
	return filepath.Clean(p), nil
}

func opErr(err error) *agentproto.Error {
	return &agentproto.Error{Code: agentproto.CodeOp, Message: err.Error()}
}

func badReq(msg string) *agentproto.Error {
	return &agentproto.Error{Code: agentproto.CodeBadRequest, Message: msg}
}
