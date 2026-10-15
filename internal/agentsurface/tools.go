package agentsurface

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/anatolykoptev/dozor/internal/agentproto"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Tool names. The set is closed: ToolNames is what the key file may grant
// and what Register adds, and nothing else.
const (
	ToolExec      = "agent_exec"
	ToolReadFile  = "agent_read_file"
	ToolWriteFile = "agent_write_file"
	ToolStatus    = "agent_status"
)

// ToolNames lists the agent tools in registration order.
var ToolNames = []string{ToolExec, ToolReadFile, ToolWriteFile, ToolStatus}

// Resolver maps a tool call to the identity (the key's run_as) whose worker
// serves it. It reads the verified token info of the request, never tool
// input, and must return an error for an unauthenticated call.
type Resolver func(ctx context.Context, req *mcp.CallToolRequest) (identity string, err error)

// Register adds the four agent tools to server. Every call is forwarded to
// the worker of the identity resolve returns; nothing runs in this process.
func Register(server *mcp.Server, resolve Resolver, cfg Config) {
	t := &tools{client: NewClient(cfg), resolve: resolve}
	yes := true
	mcp.AddTool(server, &mcp.Tool{
		Name: ToolExec,
		Description: "Run a shell command (bash -c) as your own unprivileged user on the host. " +
			"Clean environment; default timeout 60 s, max 900 s; stdout and stderr keep their last 128 KiB. " +
			"Use job-style detaching (setsid/nohup) for anything longer.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &yes},
	}, t.exec)
	mcp.AddTool(server, &mcp.Tool{
		Name: ToolReadFile,
		Description: "Read a byte range of a file you can read. Text is returned as utf8, other bytes as base64. " +
			"A reply that would be too large is shortened: continue from offset+length.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, t.readFile)
	mcp.AddTool(server, &mcp.Tool{
		Name: ToolWriteFile,
		Description: "Atomically create a file (or replace it with overwrite=true). Content up to 4 MiB as text or base64; " +
			"default mode 0600.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &yes},
	}, t.writeFile)
	mcp.AddTool(server, &mcp.Tool{
		Name:        ToolStatus,
		Description: "Report your uid, home, disk and inode headroom, memory/pids/cpu limits and usage, and the loopback range reserved for your own servers.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, t.status)
}

type tools struct {
	client  *Client
	resolve Resolver
}

// ExecInput is the agent_exec argument set. No field names a user, uid or
// host: the target worker comes only from the verified identity.
type ExecInput struct {
	Command  string `json:"command" jsonschema:"Shell command, at most 64 KiB"`
	Cwd      string `json:"cwd,omitempty" jsonschema:"Working directory (default: your home)"`
	TimeoutS int    `json:"timeout_s,omitempty" jsonschema:"Timeout in seconds (default 60, max 900)"`
	Stdin    string `json:"stdin,omitempty" jsonschema:"Text fed to the command's stdin (at most 1 MiB)"`
}

// ReadFileInput is the agent_read_file argument set.
type ReadFileInput struct {
	Path   string `json:"path" jsonschema:"File path; relative paths resolve against your home"`
	Offset int64  `json:"offset,omitempty" jsonschema:"Byte offset to start at"`
	Length int64  `json:"length,omitempty" jsonschema:"Bytes to read (default 1 MiB, max 4 MiB; shortened to fit the reply cap)"`
}

// WriteFileInput is the agent_write_file argument set.
type WriteFileInput struct {
	Path          string  `json:"path" jsonschema:"Destination path; relative paths resolve against your home"`
	Text          *string `json:"text,omitempty" jsonschema:"UTF-8 content (exactly one of text and base64)"`
	Base64        *string `json:"base64,omitempty" jsonschema:"Binary content, base64 (exactly one of text and base64)"`
	Mode          string  `json:"mode,omitempty" jsonschema:"Octal permission bits (default 0600)"`
	CreateParents bool    `json:"create_parents,omitempty" jsonschema:"Create missing parent directories"`
	Overwrite     bool    `json:"overwrite,omitempty" jsonschema:"Replace an existing file (default false)"`
}

// StatusInput has no fields.
type StatusInput struct{}

const rpcTimeout = 30 * time.Second

func invalid(msg string) error { return fmt.Errorf("%w: %s", ErrInvalid, msg) }

func (t *tools) identity(ctx context.Context, req *mcp.CallToolRequest) (string, error) {
	id, err := t.resolve(ctx, req)
	if err != nil || !agentproto.ValidRunAs(id) {
		slog.Warn("agent tool call has no valid identity")
		return "", ErrDenied
	}
	return id, nil
}

func (t *tools) exec(ctx context.Context, req *mcp.CallToolRequest, in ExecInput) (*mcp.CallToolResult, agentproto.ExecResult, error) {
	var out agentproto.ExecResult
	if in.Command == "" {
		return nil, out, invalid("command is required")
	}
	if len(in.Cwd) > agentproto.MaxPathBytes {
		return nil, out, invalid("cwd too long")
	}
	if len(in.Command) > agentproto.MaxCommandBytes {
		return nil, out, invalid("command exceeds 64 KiB")
	}
	if len(in.Stdin) > agentproto.MaxStdinBytes {
		return nil, out, invalid("stdin exceeds 1 MiB")
	}
	if in.TimeoutS < 0 || in.TimeoutS > agentproto.MaxTimeoutS {
		return nil, out, invalid("timeout_s must be between 1 and 900")
	}
	id, err := t.identity(ctx, req)
	if err != nil {
		return nil, out, err
	}
	timeout := in.TimeoutS
	if timeout == 0 {
		timeout = agentproto.DefaultTimeoutS
	}
	resp, err := t.client.Do(ctx, id, &agentproto.Request{
		V: agentproto.ProtoVersion, Op: agentproto.OpExec,
		Exec: &agentproto.ExecRequest{Command: in.Command, Cwd: in.Cwd, TimeoutS: in.TimeoutS, Stdin: in.Stdin},
	}, time.Duration(timeout)*time.Second+agentproto.ExecGrace)
	if err != nil {
		return nil, out, err
	}
	if resp.Error != nil {
		return nil, out, t.client.workerError(id, ToolExec, resp.Error)
	}
	r := resp.Exec
	if r == nil || len(r.Stdout) > agentproto.OutputTailBytes || len(r.Stderr) > agentproto.OutputTailBytes {
		return nil, out, t.client.fail(id, ToolExec, "malformed exec reply", nil)
	}
	return nil, *r, nil
}

func (t *tools) readFile(ctx context.Context, req *mcp.CallToolRequest, in ReadFileInput) (*mcp.CallToolResult, agentproto.ReadFileResult, error) {
	var out agentproto.ReadFileResult
	if in.Path == "" {
		return nil, out, invalid("path is required")
	}
	if len(in.Path) > agentproto.MaxPathBytes {
		return nil, out, invalid("path too long")
	}
	if in.Offset < 0 || in.Length < 0 || in.Length > agentproto.MaxReadBytes {
		return nil, out, invalid("offset/length out of range (length max 4 MiB)")
	}
	id, err := t.identity(ctx, req)
	if err != nil {
		return nil, out, err
	}
	resp, err := t.client.Do(ctx, id, &agentproto.Request{
		V: agentproto.ProtoVersion, Op: agentproto.OpReadFile,
		ReadFile: &agentproto.ReadFileRequest{Path: in.Path, Offset: in.Offset, Length: in.Length},
	}, rpcTimeout)
	if err != nil {
		return nil, out, err
	}
	if resp.Error != nil {
		return nil, out, t.client.workerError(id, ToolReadFile, resp.Error)
	}
	r := resp.ReadFile
	if r == nil || r.Length < 0 || r.Length > agentproto.MaxReadBytes ||
		(r.Encoding != agentproto.EncodingUTF8 && r.Encoding != agentproto.EncodingBase64) {
		return nil, out, t.client.fail(id, ToolReadFile, "malformed read reply", nil)
	}
	return nil, *r, nil
}

func (t *tools) writeFile(ctx context.Context, req *mcp.CallToolRequest, in WriteFileInput) (*mcp.CallToolResult, agentproto.WriteFileResult, error) {
	var out agentproto.WriteFileResult
	if in.Path == "" {
		return nil, out, invalid("path is required")
	}
	if len(in.Path) > agentproto.MaxPathBytes {
		return nil, out, invalid("path too long")
	}
	if (in.Text == nil) == (in.Base64 == nil) {
		return nil, out, invalid("exactly one of text and base64 is required")
	}
	if in.Text != nil && len(*in.Text) > agentproto.MaxWriteBytes {
		return nil, out, invalid("content exceeds 4 MiB")
	}
	if in.Base64 != nil {
		if base64.StdEncoding.DecodedLen(len(*in.Base64)) > agentproto.MaxWriteBytes+2 { //nolint:mnd // padding slack
			return nil, out, invalid("content exceeds 4 MiB")
		}
		if strings.ContainsAny(*in.Base64, "\r\n ") {
			return nil, out, invalid("base64 must be a single unbroken string")
		}
	}
	id, err := t.identity(ctx, req)
	if err != nil {
		return nil, out, err
	}
	resp, err := t.client.Do(ctx, id, &agentproto.Request{
		V: agentproto.ProtoVersion, Op: agentproto.OpWriteFile,
		WriteFile: &agentproto.WriteFileRequest{
			Path: in.Path, Text: in.Text, Base64: in.Base64, Mode: in.Mode,
			CreateParents: in.CreateParents, Overwrite: in.Overwrite,
		},
	}, rpcTimeout)
	if err != nil {
		return nil, out, err
	}
	if resp.Error != nil {
		return nil, out, t.client.workerError(id, ToolWriteFile, resp.Error)
	}
	if resp.WriteFile == nil {
		return nil, out, t.client.fail(id, ToolWriteFile, "malformed write reply", nil)
	}
	return nil, *resp.WriteFile, nil
}

func (t *tools) status(ctx context.Context, req *mcp.CallToolRequest, _ StatusInput) (*mcp.CallToolResult, agentproto.StatusResult, error) {
	var out agentproto.StatusResult
	id, err := t.identity(ctx, req)
	if err != nil {
		return nil, out, err
	}
	resp, err := t.client.Do(ctx, id, &agentproto.Request{V: agentproto.ProtoVersion, Op: agentproto.OpStatus}, rpcTimeout)
	if err != nil {
		return nil, out, err
	}
	if resp.Error != nil {
		return nil, out, t.client.workerError(id, ToolStatus, resp.Error)
	}
	if resp.Status == nil {
		return nil, out, t.client.fail(id, ToolStatus, "malformed status reply", nil)
	}
	return nil, *resp.Status, nil
}
