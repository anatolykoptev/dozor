// Package identity is the dozor-side identity layer: the built-in operator
// name, the per-call audit of MCP tool calls, and (for key-file keys) the
// attribute validation and role ceiling.
package identity

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sync/atomic"
	"time"

	"github.com/anatolykoptev/dozor/internal/agentsurface"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Operator is the identity of the master token on the operator listener.
const Operator = "operator"

// Anonymous labels a call that carries no verified identity (stdio, or the
// explicit DOZOR_MCP_ALLOW_INSECURE dev override).
const Anonymous = "anonymous"

// Unknown labels a tool name that is not a registered tool, and any
// identity or tool string that is not a plain token. Client-chosen names
// never become metric labels.
const Unknown = "unknown"

// ResultOK is the audit result of a call that succeeded; failures use the
// agentsurface.Result* kinds.
const ResultOK = "ok"

const (
	maxLabelLen   = 64
	labelIdentity = "identity"
)

var labelRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]*$`)

var (
	callsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "dozor_mcp_calls_total",
		Help: "MCP tool calls by verified identity, tool and result (ok, error, unavailable, busy, denied).",
	}, []string{labelIdentity, "tool", "result"})

	callDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "dozor_mcp_call_duration_seconds",
		Help:    "MCP tool call duration by verified identity and tool.",
		Buckets: []float64{0.01, 0.05, 0.25, 1, 5, 15, 60, 300, 900},
	}, []string{labelIdentity, "tool"})
)

// label returns s when it is a short plain token, otherwise Unknown. It keeps
// client-supplied strings out of metric labels and journal lines.
func label(s string) string {
	if len(s) == 0 || len(s) > maxLabelLen || !labelRE.MatchString(s) {
		return Unknown
	}
	return s
}

// Audit returns receiving middleware that records one journal line
// (identity, tool, result, dur) and one counter and histogram sample per
// tools/call. It sits at the tool-call boundary, not the transport, so a
// tool handler's Go error is still the typed error when the result is
// classified (the SDK keeps it on CallToolResult for exactly this use).
//
// Register it AFTER mcpserver.Build: receiving middleware wraps what is
// already there, so the one added last is outermost. Added before Build it
// would sit inside the tool-timeout layer, which runs the tool in its own
// goroutine and answers the client at the deadline: the call would be
// audited only when the tool finally returned, with the tool's duration,
// and never if it hung. Outermost, it records what the caller saw, once,
// at the moment the caller saw it, including calls a later layer refuses.
//
// It never reads Arguments, never logs an error string, and never touches
// token material: the only inputs are the verified UserID, the tool name,
// the error kind and the clock. A panic that reaches it is counted as an
// error and returned as an error result (the panic value is not logged: it
// may carry caller data). Panics inside the tool-timeout goroutine are
// recovered by [Recover], which must be registered before Build.
func Audit(log *slog.Logger, opts ...AuditOption) mcp.Middleware {
	if log == nil {
		log = slog.Default()
	}
	var o auditOpts
	for _, opt := range opts {
		opt(&o)
	}
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (res mcp.Result, err error) {
			isCall := method == methodToolsCall
			if !isCall && method == methodToolsList {
				return next(ctx, method, req)
			}
			note := &denyNote{}
			ctx = context.WithValue(ctx, denyKey{}, note)
			start := time.Now()
			id, tool := callerAndTool(req, o.known)
			c := &callAudit{log: log, id: id, tool: tool, isCall: isCall, start: start}
			defer func() {
				r := recover()
				if r != nil {
					res, err = panicResult(), nil
				}
				c.finish(ctx, outcome(r != nil, note.denied.Load(), res, err), err)
			}()
			return next(ctx, method, req)
		}
	}
}

// callAudit is what one audited call knows when it ends.
type callAudit struct {
	log      *slog.Logger
	id, tool string
	isCall   bool
	start    time.Time
}

// finish records the call: a counter and a histogram sample and one journal line.
func (c *callAudit) finish(ctx context.Context, result string, err error) {
	if result == agentsurface.ResultError && isUnknownTool(err) {
		c.tool = Unknown
	}
	if !c.isCall {
		// Only refusals of non-tool methods are audited; pings, initialize and
		// notifications are not tool calls.
		if result != agentsurface.ResultDenied {
			return
		}
		c.tool = Unknown
	}
	dur := time.Since(c.start)
	callsTotal.WithLabelValues(c.id, c.tool, result).Inc()
	callDuration.WithLabelValues(c.id, c.tool).Observe(dur.Seconds())
	c.log.LogAttrs(ctx, slog.LevelInfo, "mcp call",
		slog.String("identity", c.id),
		slog.String("tool", c.tool),
		slog.String("result", result),
		slog.Duration("dur", dur.Round(time.Millisecond)),
	)
}

// outcome picks the audit result: a panic is an error, a refusal by a layer
// below is denied, otherwise the result decides.
func outcome(panicked, denied bool, res mcp.Result, err error) string {
	switch {
	case panicked:
		return agentsurface.ResultError
	case denied:
		return agentsurface.ResultDenied
	default:
		return resultOf(res, err)
	}
}

// callerAndTool returns the audit labels of a request: the verified identity
// and the tool name, each a plain token or Unknown.
func callerAndTool(req mcp.Request, known func(string) bool) (id, tool string) {
	id, tool = Anonymous, Unknown
	if extra := req.GetExtra(); extra != nil && extra.TokenInfo != nil {
		id = label(extra.TokenInfo.UserID)
	}
	if p, ok := req.GetParams().(*mcp.CallToolParamsRaw); ok && (known == nil || known(p.Name)) {
		tool = label(p.Name)
	}
	return id, tool
}

const (
	methodToolsCall = "tools/call"
	methodToolsList = "tools/list"
)

type auditOpts struct{ known func(string) bool }

// AuditOption configures [Audit].
type AuditOption func(*auditOpts)

// WithKnownTools clamps the tool label to the given registered names: any
// other name, including one a client made up, is labelled Unknown. Without it
// any plain token is accepted as a label.
func WithKnownTools(sets ...[]string) AuditOption {
	known := map[string]struct{}{}
	for _, set := range sets {
		for _, n := range set {
			known[n] = struct{}{}
		}
	}
	return func(o *auditOpts) {
		o.known = func(n string) bool { _, ok := known[n]; return ok }
	}
}

type denyKey struct{}

// denyNote lets a layer below [Audit] say "I refused this call", which a
// refusal returns as an ordinary error result with no typed error to classify.
type denyNote struct{ denied atomic.Bool }

func markDenied(ctx context.Context) {
	if n, ok := ctx.Value(denyKey{}).(*denyNote); ok {
		n.denied.Store(true)
	}
}

// MarkRefusals wraps a receiving middleware so that when it fails a request
// without passing it on (a method-level refusal, e.g. keyfile's default-deny
// of non-tool methods), the audit layer records the request as denied.
func MarkRefusals(mw mcp.Middleware) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			reached := false
			inner := mw(func(c context.Context, m string, r mcp.Request) (mcp.Result, error) {
				reached = true
				return next(c, m, r)
			})
			res, err := inner(ctx, method, req)
			if err != nil && !reached {
				markDenied(ctx)
			}
			return res, err
		}
	}
}

var errToolPanic = errors.New("internal error: the tool panicked")

func panicResult() *mcp.CallToolResult {
	var r mcp.CallToolResult
	r.SetError(errToolPanic)
	return &r
}

// Recover returns receiving middleware that turns a panic below it into an
// error result. Register it BEFORE mcpserver.Build: the tool-timeout layer
// runs the tool in its own goroutine, where only a recover installed inside
// that goroutine (that is, below it) can stop the panic from ending the
// process. It logs the identity, the tool and the panic's type, never its
// value or the arguments.
func Recover(log *slog.Logger) mcp.Middleware {
	if log == nil {
		log = slog.Default()
	}
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (res mcp.Result, err error) {
			defer func() {
				if r := recover(); r != nil {
					id, tool := callerAndTool(req, nil)
					log.LogAttrs(ctx, slog.LevelError, "mcp handler panicked",
						slog.String("identity", id), slog.String("tool", tool),
						slog.String("method", label(method)), slog.String("panic_type", fmt.Sprintf("%T", r)))
					if method == "tools/call" {
						res, err = panicResult(), nil
						return
					}
					res, err = nil, errToolPanic
				}
			}()
			return next(ctx, method, req)
		}
	}
}

// resultOf maps a tool-call outcome to its audit kind.
func resultOf(res mcp.Result, err error) string {
	if err != nil {
		return agentsurface.ResultError
	}
	r, ok := res.(*mcp.CallToolResult)
	if !ok || r == nil || !r.IsError {
		return ResultOK
	}
	if goErr := r.GetError(); goErr != nil {
		return agentsurface.Classify(goErr)
	}
	return agentsurface.ResultError
}

// isUnknownTool reports the SDK's reply to a tool that is not registered
// (a wire InvalidParams error), so a made-up name cannot mint a label.
func isUnknownTool(err error) bool {
	we, ok := err.(*jsonrpc.Error) //nolint:errorlint // the SDK returns it unwrapped
	return ok && we.Code == jsonrpc.CodeInvalidParams
}
