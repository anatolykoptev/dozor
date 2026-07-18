package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCPHooks provides typed callbacks for common MCP lifecycle events.
// Use [MCPHooks.Middleware] to convert to [mcp.Middleware] for
// [Config.MCPReceivingMiddleware].
//
// Hooks are observer-only — they cannot modify requests or responses.
type MCPHooks struct {
	// OnToolCall fires before a tool executes. toolName is the requested tool.
	OnToolCall func(ctx context.Context, toolName string)
	// OnToolResult fires after a tool executes with timing and outcome.
	OnToolResult func(ctx context.Context, toolName string, duration time.Duration, isError bool)
	// OnError fires when any MCP method returns an error.
	OnError func(ctx context.Context, method string, err error)
}

// Middleware converts MCPHooks to [mcp.Middleware] for use in
// [Config.MCPReceivingMiddleware].
func (h MCPHooks) Middleware() mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method == methodToolsCall {
				return h.handleToolCall(ctx, method, req, next)
			}
			result, err := next(ctx, method, req)
			if err != nil && h.OnError != nil {
				h.OnError(ctx, method, err)
			}
			return result, err
		}
	}
}

func (h MCPHooks) handleToolCall(
	ctx context.Context, method string, req mcp.Request, next mcp.MethodHandler,
) (mcp.Result, error) {
	name := req.GetParams().(*mcp.CallToolParamsRaw).Name
	if h.OnToolCall != nil {
		h.OnToolCall(ctx, name)
	}
	start := time.Now()
	result, err := next(ctx, method, req)
	if h.OnToolResult != nil {
		isErr := err != nil
		if !isErr {
			if cr, ok := result.(*mcp.CallToolResult); ok {
				isErr = cr.IsError
			}
		}
		h.OnToolResult(ctx, name, time.Since(start), isErr)
	}
	if err != nil && h.OnError != nil {
		h.OnError(ctx, method, err)
	}
	return result, err
}

// methodToolsCall is the JSON-RPC method name for an MCP tools/call request.
const methodToolsCall = "tools/call"

// leakWarnFactor controls when the timeout watchdog logs a warning.
// If the worker goroutine eventually returns more than (factor × timeout)
// after the deadline elapsed, that suggests the tool is ignoring ctx.Done().
const leakWarnFactor = 2

// ToolTimeoutMiddleware returns MCP middleware that enforces tool execution timeouts.
//
// Timeout resolution order (first wins):
//  1. "timeout_secs" in the tool call arguments (LLM per-request override, capped at MaxToolTimeout)
//  2. cfg.ToolTimeouts[toolName] (per-tool config)
//  3. cfg.ToolTimeout (global default, 90s)
//
// On timeout the tool returns an error result instead of hanging.
//
// Concurrent tool execution is bounded by cfg.MaxConcurrentTools (default 100).
// When the limit is reached, additional calls return an error result immediately
// instead of spawning an unbounded goroutine. This prevents goroutine leaks from
// tools that ignore ctx.Done().
//
// The worker goroutine is detached on timeout: if the underlying tool does
// not honor ctx.Done(), it keeps running and may leak. To surface that, a
// best-effort watchdog logs a slog.Warn when the worker eventually returns
// more than leakWarnFactor × timeout after the deadline. This does not
// kill the goroutine — Go has no way to do that — but makes the leak
// visible in operator logs so the underlying tool can be fixed.
func ToolTimeoutMiddleware(cfg Config) mcp.Middleware {
	// MaxConcurrentTools <= 0 means unbounded (backward compat when
	// withDefaults is not called, e.g. in direct unit tests).
	var sem chan struct{}
	if cfg.MaxConcurrentTools > 0 {
		sem = make(chan struct{}, cfg.MaxConcurrentTools)
	}
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method != methodToolsCall {
				return next(ctx, method, req)
			}
			params := req.GetParams().(*mcp.CallToolParamsRaw)
			timeout := resolveTimeout(params.Name, params.Arguments, cfg)

			// Acquire semaphore (if configured) — non-blocking; if at
			// capacity, reject immediately rather than spawning an
			// unbounded goroutine.
			if sem != nil {
				select {
				case sem <- struct{}{}:
				default:
					return &mcp.CallToolResult{
						IsError: true,
						Content: []mcp.Content{&mcp.TextContent{
							Text: fmt.Sprintf("tool %q rejected: max concurrent tools (%d) reached", params.Name, cfg.MaxConcurrentTools),
						}},
					}, nil
				}
				defer func() { <-sem }()
			}

			ctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()

			type callResult struct {
				result mcp.Result
				err    error
			}
			ch := make(chan callResult, 1)
			start := time.Now()
			go func() {
				r, e := next(ctx, method, req)
				// Buffered send never blocks — keeps the goroutine itself from
				// leaking forever waiting on a receiver. Receiver consumes
				// from ch only inside the select below, before the parent
				// timeout fires.
				ch <- callResult{r, e}
				// If we returned long after the deadline, the parent gave up
				// already and the underlying tool is probably ignoring
				// ctx.Done(). Surface this so operators can fix the tool.
				if elapsed := time.Since(start); elapsed > leakWarnFactor*timeout {
					slog.Warn("tool goroutine outlived its timeout — tool likely ignores ctx.Done()",
						slog.String("tool", params.Name),
						slog.Duration("timeout", timeout),
						slog.Duration("elapsed", elapsed),
					)
				}
			}()

			select {
			case cr := <-ch:
				return cr.result, cr.err
			case <-ctx.Done():
				return &mcp.CallToolResult{
					IsError: true,
					Content: []mcp.Content{&mcp.TextContent{
						Text: fmt.Sprintf("tool %q timed out after %s", params.Name, timeout),
					}},
				}, nil
			}
		}
	}
}

func resolveTimeout(name string, args json.RawMessage, cfg Config) time.Duration {
	// 1. Check request args for timeout_secs (capped at MaxToolTimeout).
	if t := parseArgTimeout(args); t > 0 {
		if cfg.MaxToolTimeout > 0 && t > cfg.MaxToolTimeout {
			return cfg.MaxToolTimeout
		}
		return t
	}

	// 2. Check per-tool config.
	if t, ok := cfg.ToolTimeouts[name]; ok && t > 0 {
		return t
	}

	// 3. Global default.
	return cfg.ToolTimeout
}

func parseArgTimeout(args json.RawMessage) time.Duration {
	if len(args) == 0 {
		return 0
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(args, &m) != nil {
		return 0
	}
	raw, ok := m["timeout_secs"]
	if !ok {
		return 0
	}
	var secs float64
	if json.Unmarshal(raw, &secs) != nil || secs <= 0 {
		return 0
	}
	return time.Duration(secs * float64(time.Second))
}
