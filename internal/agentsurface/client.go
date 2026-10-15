// Package agentsurface is the MCP side of the agent tools: four tool
// handlers and the client that forwards each call to the caller's own worker
// over a unix socket. It deliberately imports nothing that can execute code
// as dozor (fitness function F1): there is no fallback path, so a missing or
// misbehaving worker means "unavailable", never a local execution.
package agentsurface

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"github.com/anatolykoptev/dozor/internal/agentproto"
)

// Tool-visible failures. Their text is the whole message the agent sees, and
// the audit layer classifies results by it.
var (
	ErrUnavailable = errors.New("unavailable")
	ErrBusy        = errors.New("busy")
	ErrDenied      = errors.New("denied")
)

// Config tunes the worker client. The zero value is production-correct:
// workers live in agentproto.SocketDir and the socket's creator is root.
type Config struct {
	// SocketDir holds "<identity>.sock". Default agentproto.SocketDir.
	SocketDir string
	// ExpectedPeerUID is the uid that must own the listening socket. For a
	// socket-activated worker that is the activator, uid 0, so a socket
	// squatted by any other process is refused. Tests override it.
	ExpectedPeerUID uint32
	// DialTimeout bounds connecting. Default agentproto.DialTimeout.
	DialTimeout time.Duration
	// MaxInFlight caps concurrent calls per identity. Default agentproto.MaxInFlight.
	MaxInFlight int
	// ReplyBytesPerMinute caps reply bytes per identity per minute.
	// Default agentproto.ReplyBytesPerMinute.
	ReplyBytesPerMinute int64
	// Logger receives one line per failed call, never arguments or output.
	Logger *slog.Logger
	// OnReply is called for every well-formed reply with the worker's
	// protocol version and build, for skew metrics.
	OnReply func(identity string, version int, build string)

	now func() time.Time
}

// Client forwards requests to per-identity workers.
type Client struct {
	cfg     Config
	mu      sync.Mutex
	slots   map[string]chan struct{}
	budgets map[string]*budget
}

type budget struct {
	windowStart time.Time
	used        int64
}

// NewClient applies defaults to cfg.
func NewClient(cfg Config) *Client {
	if cfg.SocketDir == "" {
		cfg.SocketDir = agentproto.SocketDir
	}
	if cfg.DialTimeout <= 0 {
		cfg.DialTimeout = agentproto.DialTimeout
	}
	if cfg.MaxInFlight <= 0 {
		cfg.MaxInFlight = agentproto.MaxInFlight
	}
	if cfg.ReplyBytesPerMinute <= 0 {
		cfg.ReplyBytesPerMinute = agentproto.ReplyBytesPerMinute
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.now == nil {
		cfg.now = time.Now
	}
	return &Client{cfg: cfg, slots: map[string]chan struct{}{}, budgets: map[string]*budget{}}
}

func (c *Client) acquire(identity string) (release func(), ok bool) {
	c.mu.Lock()
	ch, found := c.slots[identity]
	if !found {
		ch = make(chan struct{}, c.cfg.MaxInFlight)
		c.slots[identity] = ch
	}
	c.mu.Unlock()
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, true
	default:
		return nil, false
	}
}

// spend reports whether identity still has reply budget in the current
// minute; charge adds consumed bytes.
func (c *Client) overBudget(identity string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	b := c.budgets[identity]
	return b != nil && c.cfg.now().Sub(b.windowStart) < time.Minute && b.used >= c.cfg.ReplyBytesPerMinute
}

func (c *Client) charge(identity string, n int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.cfg.now()
	b := c.budgets[identity]
	if b == nil || now.Sub(b.windowStart) >= time.Minute {
		b = &budget{windowStart: now}
		c.budgets[identity] = b
	}
	b.used += int64(n)
}

func (c *Client) fail(identity, op, reason string, err error) error {
	attrs := []any{slog.String("identity", clamp(identity)), slog.String("op", op), slog.String("reason", reason)}
	if err != nil {
		attrs = append(attrs, slog.String("error", clamp(err.Error())))
	}
	c.cfg.Logger.Warn("agent worker call failed", attrs...)
	return ErrUnavailable
}

// Do sends req to identity's worker and returns its decoded reply. Every
// transport, framing, size or version problem returns ErrUnavailable; there
// is no fallback. The identity is validated by SocketPath, so only an agent
// socket can ever be dialled.
func (c *Client) Do(ctx context.Context, identity string, req *agentproto.Request, timeout time.Duration) (*agentproto.Response, error) {
	sock, err := agentproto.SocketPath(c.cfg.SocketDir, identity)
	if err != nil {
		return nil, ErrDenied
	}
	release, ok := c.acquire(identity)
	if !ok || c.overBudget(identity) {
		if ok {
			release()
		}
		return nil, ErrBusy
	}
	defer release()

	conn, err := c.dial(ctx, identity, req.Op, sock)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	_ = conn.SetDeadline(time.Now().Add(timeout))
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Unix(1, 0)) })
	defer stop()

	data, err := c.exchange(conn, identity, req)
	if err != nil {
		return nil, err
	}
	c.charge(identity, len(data))
	var resp agentproto.Response
	if err := agentproto.DecodeStrict(data, &resp); err != nil {
		return nil, c.fail(identity, req.Op, "decode", err)
	}
	if !agentproto.VersionOK(resp.V) {
		return nil, c.fail(identity, req.Op, fmt.Sprintf("unsupported worker protocol version %d", resp.V), nil)
	}
	if c.cfg.OnReply != nil {
		c.cfg.OnReply(identity, resp.V, clamp(resp.Build))
	}
	return &resp, nil
}

// dial connects and proves we are talking to the socket's activator.
func (c *Client) dial(ctx context.Context, identity, op, sock string) (*net.UnixConn, error) {
	dctx, cancel := context.WithTimeout(ctx, c.cfg.DialTimeout)
	defer cancel()
	raw, err := (&net.Dialer{}).DialContext(dctx, "unix", sock)
	if err != nil {
		if errors.Is(err, syscall.EAGAIN) {
			return nil, ErrBusy
		}
		return nil, c.fail(identity, op, "dial", err)
	}
	conn, ok := raw.(*net.UnixConn)
	if !ok {
		_ = raw.Close()
		return nil, c.fail(identity, op, "not a unix connection", nil)
	}
	// The worker is the untrusted end, so first make sure we are talking to
	// the activator that owns the socket and not a squatter.
	peer, err := agentproto.PeerUID(conn)
	if err != nil {
		_ = conn.Close()
		return nil, c.fail(identity, op, "peer credentials", err)
	}
	if peer != c.cfg.ExpectedPeerUID {
		_ = conn.Close()
		return nil, c.fail(identity, op, fmt.Sprintf("socket owned by uid %d", peer), nil)
	}
	return conn, nil
}

// exchange sends the request, half-closes, and reads at most MaxReplyBytes.
func (c *Client) exchange(conn *net.UnixConn, identity string, req *agentproto.Request) ([]byte, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, c.fail(identity, req.Op, "encode", err)
	}
	if _, err := conn.Write(payload); err != nil {
		return nil, c.fail(identity, req.Op, "write", err)
	}
	if err := conn.CloseWrite(); err != nil {
		return nil, c.fail(identity, req.Op, "half-close", err)
	}
	data, err := io.ReadAll(io.LimitReader(conn, agentproto.MaxReplyBytes+1))
	if err != nil {
		return nil, c.fail(identity, req.Op, "read", err)
	}
	if len(data) > agentproto.MaxReplyBytes {
		return nil, c.fail(identity, req.Op, "reply over the size cap", nil)
	}
	return data, nil
}

// workerError turns a worker-reported error into a tool error. A refusal is
// "unavailable" (the worker declined to serve); other messages are shown to
// the agent after clamping and sanitising, since the worker is untrusted.
func (c *Client) workerError(identity, op string, e *agentproto.Error) error {
	if e.Code == agentproto.CodeRefused {
		return c.fail(identity, op, "worker refused: "+clamp(e.Message), nil)
	}
	return fmt.Errorf("%s: %s", op, clamp(e.Message))
}

// clamp makes untrusted text safe for a log line or an error message: it
// cuts to MaxErrorBytes and replaces every non-printable rune.
func clamp(s string) string {
	if len(s) > agentproto.MaxErrorBytes {
		s = s[:agentproto.MaxErrorBytes]
	}
	return strings.Map(func(r rune) rune {
		if r == unicode.ReplacementChar || !unicode.IsPrint(r) {
			return '?'
		}
		return r
	}, s)
}
