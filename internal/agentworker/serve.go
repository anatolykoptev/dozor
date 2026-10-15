package agentworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"github.com/anatolykoptev/dozor/internal/agentproto"
)

const ioTimeout = 30 * time.Second

// Serve handles exactly one request on conn and writes exactly one reply:
// the inetd-style contract of an Accept=yes socket unit. The request must be
// a single JSON value followed by EOF (the client half-closes). inherited are
// the process's own descriptors for the same connection (stdin/stdout); they
// are sealed close-on-exec before anything is run.
func (c *Config) Serve(ctx context.Context, conn net.Conn, inherited ...*os.File) error {
	if err := c.validate(); err != nil {
		return err
	}
	if err := seal(inherited...); err != nil {
		return fmt.Errorf("seal connection: %w", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(ioTimeout))
	data, err := io.ReadAll(io.LimitReader(conn, agentproto.MaxRequestBytes+1))
	if err != nil {
		return c.reply(conn, errResp(agentproto.CodeBadRequest, "read request: "+err.Error()))
	}
	if len(data) > agentproto.MaxRequestBytes {
		return c.reply(conn, errResp(agentproto.CodeBadRequest, "request too large"))
	}
	var req agentproto.Request
	if err := agentproto.DecodeStrict(data, &req); err != nil {
		return c.reply(conn, errResp(agentproto.CodeBadRequest, err.Error()))
	}
	return c.reply(conn, c.Handle(ctx, &req))
}

// Handle dispatches one decoded request.
func (c *Config) Handle(_ context.Context, req *agentproto.Request) *agentproto.Response {
	if !agentproto.VersionOK(req.V) {
		return errResp(agentproto.CodeBadRequest, fmt.Sprintf("unsupported protocol version %d", req.V))
	}
	resp := &agentproto.Response{}
	var perr *agentproto.Error
	switch req.Op {
	case agentproto.OpHello:
	case agentproto.OpExec:
		resp.Exec, perr = c.Exec(req.Exec)
	case agentproto.OpReadFile:
		resp.ReadFile, perr = c.ReadFile(req.ReadFile)
	case agentproto.OpWriteFile:
		resp.WriteFile, perr = c.WriteFile(req.WriteFile)
	case agentproto.OpStatus:
		resp.Status = c.Status()
	default:
		perr = badReq("unknown op")
	}
	resp.Error = perr
	return resp
}

func errResp(code, msg string) *agentproto.Response {
	return &agentproto.Response{Error: &agentproto.Error{Code: code, Message: msg}}
}

// Refuse writes a refusal reply (self-check failure) on conn.
func Refuse(conn net.Conn, reason string) error {
	return (&Config{}).reply(conn, errResp(agentproto.CodeRefused, reason))
}

func (c *Config) reply(conn net.Conn, resp *agentproto.Response) error {
	resp.V, resp.Build = agentproto.ProtoVersion, agentproto.BuildID
	b, err := json.Marshal(resp)
	if err != nil || len(b)+1 > agentproto.MaxReplyBytes {
		resp = errResp(agentproto.CodeOp, "reply too large")
		resp.V, resp.Build = agentproto.ProtoVersion, agentproto.BuildID
		if b, err = json.Marshal(resp); err != nil {
			return err
		}
	}
	_ = conn.SetWriteDeadline(time.Now().Add(ioTimeout))
	if _, err := conn.Write(append(b, '\n')); err != nil {
		return err
	}
	if cw, ok := conn.(interface{ CloseWrite() error }); ok {
		if err := cw.CloseWrite(); err != nil && !errors.Is(err, net.ErrClosed) {
			return err
		}
	}
	return nil
}
