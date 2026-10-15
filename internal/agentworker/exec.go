package agentworker

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/anatolykoptev/dozor/internal/agentproto"
)

// killGrace is both the SIGTERM-to-SIGKILL window on timeout and the time a
// backgrounded process holding our output pipe can delay the reply.
const killGrace = 3 * time.Second

// tail keeps the last max bytes written to it.
type tail struct {
	max       int
	buf       []byte
	truncated bool
}

func (t *tail) Write(p []byte) (int, error) {
	n := len(p)
	t.buf = append(t.buf, p...)
	if len(t.buf) > 2*t.max {
		t.buf = append(t.buf[:0], t.buf[len(t.buf)-t.max:]...)
		t.truncated = true
	}
	return n, nil
}

// last returns the final n bytes, trimmed so it starts on a rune boundary.
func (t *tail) last(n int) ([]byte, bool) {
	b := t.buf
	cut := t.truncated
	if len(b) > n {
		b = b[len(b)-n:]
		cut = true
	}
	for cut && len(b) > 0 && !utf8.RuneStart(b[0]) {
		b = b[1:]
	}
	return b, cut
}

// Exec runs req.Command under bash as the worker's uid. The child gets a
// clean environment, /dev/null (or the supplied stdin) on fd 0, pipes on
// fd 1/2 and no other descriptor: the connection never reaches it.
func (c *Config) Exec(req *agentproto.ExecRequest) (*agentproto.ExecResult, *agentproto.Error) {
	timeout, cwd, perr := c.checkExec(req)
	if perr != nil {
		return nil, perr
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	argv := append(append([]string{}, c.ExecPrefix...), "/bin/bash", "-c", req.Command)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // running caller commands is the tool's purpose; the uid bounds it
	cmd.Dir = cwd
	cmd.Env = c.cleanEnv()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// On timeout: SIGTERM the whole group; a leader that ignores it is killed
	// after WaitDelay, which also bounds how long a backgrounded pipe holder
	// can delay the reply.
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = killGrace
	cmd.Stdin = nil // /dev/null
	if req.Stdin != "" {
		cmd.Stdin = strings.NewReader(req.Stdin)
	}
	out := &tail{max: agentproto.OutputTailBytes}
	errb := &tail{max: agentproto.OutputTailBytes}
	cmd.Stdout, cmd.Stderr = out, errb

	start := time.Now()
	if err := cmd.Start(); err != nil {
		return nil, opErr(err)
	}
	pgid := cmd.Process.Pid
	werr := cmd.Wait()
	timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded)
	if timedOut {
		_ = syscall.Kill(-pgid, syscall.SIGKILL) // stragglers that ignored SIGTERM
	}
	if cmd.ProcessState == nil {
		return nil, opErr(werr)
	}

	res := &agentproto.ExecResult{TimedOut: timedOut, DurationMs: time.Since(start).Milliseconds()}
	if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		res.Signal, res.ExitCode = ws.Signal().String(), -1
	} else {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}
	fillOutput(res, out, errb)
	return res, nil
}

// checkExec validates the request and resolves timeout and working directory.
func (c *Config) checkExec(req *agentproto.ExecRequest) (time.Duration, string, *agentproto.Error) {
	switch {
	case req == nil || req.Command == "":
		return 0, "", badReq("command is required")
	case len(req.Command) > agentproto.MaxCommandBytes:
		return 0, "", badReq("command too large")
	case len(req.Stdin) > agentproto.MaxStdinBytes:
		return 0, "", badReq("stdin too large")
	}
	timeout := req.TimeoutS
	if timeout == 0 {
		timeout = agentproto.DefaultTimeoutS
	}
	if timeout < 0 || timeout > agentproto.MaxTimeoutS {
		return 0, "", badReq("timeout_s out of range")
	}
	cwd := c.Home
	if req.Cwd != "" {
		var err error
		if cwd, err = c.resolve(req.Cwd); err != nil {
			return 0, "", badReq(err.Error())
		}
	}
	return time.Duration(timeout) * time.Second, cwd, nil
}

// fillOutput stores the output tails, shrinking them until the encoded reply
// fits the reply cap (JSON escaping can inflate a byte up to six times).
func fillOutput(res *agentproto.ExecResult, out, errb *tail) {
	n := agentproto.OutputTailBytes
	for {
		so, c1 := out.last(n)
		se, c2 := errb.last(n)
		res.Stdout, res.Stderr, res.Truncated = string(so), string(se), c1 || c2
		if n <= 1 || fits(&agentproto.Response{Build: agentproto.BuildID, Exec: res}) {
			return
		}
		n /= 2
	}
}
