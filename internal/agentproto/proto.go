// Package agentproto defines the wire contract between dozor (the client) and
// the per-identity agent worker: constants, request/response structs and the
// strict codec both ends share. It imports nothing from dozor, so the worker
// binary and the agent tool surface can both depend on it without pulling in
// any code that executes with dozor's own privileges.
package agentproto

import (
	"regexp"
	"time"
)

// BuildID identifies the build of the dozor/worker pair. It is a variable so
// the build can stamp it (-ldflags "-X .../agentproto.BuildID=<rev>"); the
// client compares it with the worker's reply to detect version skew.
var BuildID = "dev"

const (
	// ProtoVersion is the wire protocol version this build speaks. A peer
	// speaking ProtoVersion or ProtoVersion-1 is accepted (rolling upgrade
	// skew); anything else, including a missing (zero) version, is refused.
	ProtoVersion = 2
	// MinProtoVersion is the oldest peer version still accepted.
	MinProtoVersion = ProtoVersion - 1

	// UIDMin and UIDMax bound the euid a worker may run as.
	UIDMin = 2000
	UIDMax = 2999

	// SocketDir holds one "<identity>.sock" per agent identity. It is
	// root-owned, so only root can create or replace a socket path.
	SocketDir = "/run/dozor-workers"

	// LoopbackCIDR is the loopback range reserved for agent-owned servers.
	LoopbackCIDR = "127.30.0.0/16"
	// PortRangeLow and PortRangeHigh bound the ports advertised to the agent
	// for its own servers.
	PortRangeLow  = 30000
	PortRangeHigh = 30999
)

// Operations.
const (
	OpHello     = "hello"
	OpExec      = "exec"
	OpReadFile  = "read_file"
	OpWriteFile = "write_file"
	OpStatus    = "status"
)

// Error codes carried in Response.Error.Code.
const (
	// CodeRefused: the worker's self-check refused to serve. The client
	// reports it as "unavailable".
	CodeRefused = "refused"
	// CodeBadRequest: malformed or out-of-policy request.
	CodeBadRequest = "bad_request"
	// CodeExecDisabled: the worker serves, but agent_exec is switched off
	// (for example the kernel cannot confine TCP bind). The client reports it
	// as "unavailable" and counts it under its own reject reason.
	CodeExecDisabled = "exec_disabled"
	// CodeOp: the operation itself failed (missing file, exec start failure).
	CodeOp = "op"
)

// Size and time limits, enforced by the worker and again by the client.
const (
	MaxRequestBytes = 6 << 20 // 4 MiB payload, base64 inflated, plus envelope
	MaxReplyBytes   = 1 << 20 // the client reads at most this much
	MaxJSONDepth    = 16

	MaxCommandBytes = 64 << 10
	// MaxPathBytes caps a path or working directory argument.
	MaxPathBytes    = 4096
	MaxStdinBytes   = 1 << 20
	DefaultTimeoutS = 60
	MaxTimeoutS     = 900
	// OutputTailBytes caps each of stdout and stderr; the tail is kept.
	OutputTailBytes = 128 << 10

	DefaultReadBytes = 1 << 20
	MaxReadBytes     = 4 << 20
	MaxWriteBytes    = 4 << 20

	// MaxInFlight is the per-identity concurrency, matching the socket
	// unit's MaxConnections.
	MaxInFlight = 4
	// ReplyBytesPerMinute is the per-identity budget of reply bytes.
	ReplyBytesPerMinute = 64 << 20

	DialTimeout = 2 * time.Second
	// ExecGrace is added to an exec timeout to form the client I/O deadline.
	ExecGrace = 15 * time.Second
	// MaxErrorBytes clamps a worker-supplied error string.
	MaxErrorBytes = 256
)

var runAsRE = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,31}$`)

// reservedRunAs are never valid agent identities.
var reservedRunAs = map[string]bool{"root": true, "krolik": true, "ubuntu": true}

// ValidRunAs reports whether name is acceptable as an agent identity: it is
// the single syntax-and-denylist rule shared by the socket resolver and the
// key-file validator.
func ValidRunAs(name string) bool {
	return runAsRE.MatchString(name) && !reservedRunAs[name]
}

// Request is one operation. Exactly the member matching Op is set.
type Request struct {
	V         int               `json:"v"`
	Op        string            `json:"op"`
	Exec      *ExecRequest      `json:"exec,omitempty"`
	ReadFile  *ReadFileRequest  `json:"read_file,omitempty"`
	WriteFile *WriteFileRequest `json:"write_file,omitempty"`
}

// Response is the single reply. V and Build are always set.
type Response struct {
	V         int              `json:"v"`
	Build     string           `json:"build"`
	Error     *Error           `json:"error,omitempty"`
	Exec      *ExecResult      `json:"exec,omitempty"`
	ReadFile  *ReadFileResult  `json:"read_file,omitempty"`
	WriteFile *WriteFileResult `json:"write_file,omitempty"`
	Status    *StatusResult    `json:"status,omitempty"`
}

// Error is a worker-side failure. Message is untrusted text.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ExecRequest runs a shell command as the worker's own uid.
type ExecRequest struct {
	Command  string `json:"command"`
	Cwd      string `json:"cwd,omitempty"`
	TimeoutS int    `json:"timeout_s,omitempty"`
	Stdin    string `json:"stdin,omitempty"`
}

// ExecResult is the outcome of a command that was started.
type ExecResult struct {
	ExitCode   int    `json:"exit_code"`
	Signal     string `json:"signal,omitempty"`
	TimedOut   bool   `json:"timed_out,omitempty"`
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	Truncated  bool   `json:"truncated,omitempty"`
	DurationMs int64  `json:"duration_ms"`
}

// ReadFileRequest reads a byte range of a regular file.
type ReadFileRequest struct {
	Path   string `json:"path"`
	Offset int64  `json:"offset,omitempty"`
	Length int64  `json:"length,omitempty"`
}

// Encodings used by file content.
const (
	EncodingUTF8   = "utf8"
	EncodingBase64 = "base64"
)

// ReadFileResult returns the bytes actually read. Length can be shorter than
// requested when the reply would not fit MaxReplyBytes; page with Offset.
type ReadFileResult struct {
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	Offset   int64  `json:"offset"`
	Length   int64  `json:"length"`
	Encoding string `json:"encoding"`
	Content  string `json:"content"`
	EOF      bool   `json:"eof"`
}

// WriteFileRequest atomically creates or replaces a file. Exactly one of
// Text and Base64 is set.
type WriteFileRequest struct {
	Path          string  `json:"path"`
	Text          *string `json:"text,omitempty"`
	Base64        *string `json:"base64,omitempty"`
	Mode          string  `json:"mode,omitempty"` // octal, default "0600"
	CreateParents bool    `json:"create_parents,omitempty"`
	Overwrite     bool    `json:"overwrite,omitempty"`
}

// WriteFileResult confirms a committed write.
type WriteFileResult struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
	Mode  string `json:"mode"`
}

// StatusResult describes the worker's own limits and usage.
type StatusResult struct {
	UID          int         `json:"uid"`
	Home         string      `json:"home"`
	FS           *FSStat     `json:"fs,omitempty"`
	Cgroup       *CgroupStat `json:"cgroup,omitempty"`
	LoopbackCIDR string      `json:"loopback_cidr"`
	PortRange    string      `json:"port_range"`
	Notes        []string    `json:"notes,omitempty"`
}

// FSStat is the statfs view of the home directory (the quota view).
type FSStat struct {
	TotalBytes  uint64 `json:"total_bytes"`
	FreeBytes   uint64 `json:"free_bytes"`
	TotalInodes uint64 `json:"total_inodes"`
	FreeInodes  uint64 `json:"free_inodes"`
}

// CgroupStat is read from the agent slice's cgroup v2 files. A limit of -1
// means "max" (unlimited).
type CgroupStat struct {
	Slice            string `json:"slice"`
	MemoryCurrent    int64  `json:"memory_current"`
	MemoryMax        int64  `json:"memory_max"`
	PidsCurrent      int64  `json:"pids_current"`
	PidsMax          int64  `json:"pids_max"`
	CPUNrThrottled   int64  `json:"cpu_nr_throttled"`
	CPUThrottledUsec int64  `json:"cpu_throttled_usec"`
}
