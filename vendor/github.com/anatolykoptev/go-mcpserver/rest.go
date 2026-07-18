package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	defaultRESTPrefix = "/api"
	contentTypeJSON   = "application/json"
	openAPIVersion    = "3.1.0"
	maxBodySize       = 1 << 20 // 1 MB
)

var validToolName = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// defaultToolsCacheTTL is how long the REST bridge caches the tools/list
// response before re-fetching. Short enough to pick up dynamically registered
// tools within a minute; long enough to keep tools/list calls cheap.
const defaultToolsCacheTTL = 60 * time.Second

// restBridge proxies HTTP requests to an in-process MCP client session.
type restBridge struct {
	session *mcp.ClientSession
	prefix  string
	cfg     Config
	logger  *slog.Logger

	cacheTTL time.Duration

	cacheMu      sync.Mutex
	cachedTools  []*mcp.Tool
	cachedByName map[string]*mcp.Tool
	cachedErr    error
	cachedAt     time.Time
}

// startRESTBridge creates an in-process MCP client, connects it to the server,
// and registers REST endpoints on the mux. Returns a cleanup function that
// closes both MCP sessions — the caller MUST call it after the HTTP server
// has shut down so in-flight REST requests can complete before their
// underlying session is torn down.
func startRESTBridge(ctx context.Context, server *mcp.Server, mux *http.ServeMux, cfg Config, logger *slog.Logger) (func(), error) {
	serverT, clientT := mcp.NewInMemoryTransports()

	serverSession, err := server.Connect(ctx, serverT, nil)
	if err != nil {
		return nil, err
	}

	client := mcp.NewClient(&mcp.Implementation{
		Name:    cfg.Name + "-rest-bridge",
		Version: cfg.Version,
	}, nil)

	session, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		// Best-effort cleanup: client connect failed, but the server side
		// session is already up and would otherwise leak.
		if cerr := serverSession.Close(); cerr != nil {
			logger.Warn("REST bridge server session close after client failure",
				slog.Any("error", cerr))
		}
		return nil, err
	}

	prefix := cfg.RESTPrefix
	if prefix == "" {
		prefix = defaultRESTPrefix
	}
	prefix = strings.TrimRight(prefix, "/")

	b := &restBridge{
		session:  session,
		prefix:   prefix,
		cfg:      cfg,
		logger:   logger,
		cacheTTL: defaultToolsCacheTTL,
	}

	restMux := http.NewServeMux()
	restMux.HandleFunc("GET /tools", b.handleListTools)
	restMux.HandleFunc("GET /tools/{name}", b.handleGetTool)
	restMux.HandleFunc("POST /tools/{name}", b.handleCallTool)
	restMux.HandleFunc("GET /openapi.json", b.handleOpenAPI)

	handler := http.Handler(http.StripPrefix(prefix, restMux))
	if cfg.BearerAuth != nil {
		handler = applyBearerAuth(handler, cfg.BearerAuth)
	}
	mux.Handle(prefix+"/", handler)

	// Close sessions when the context is cancelled (covers Build() testing
	// and non-Run() embedders). Run() passes a non-cancellable context so
	// this goroutine only fires on abnormal exit; the cleanup function is
	// the authoritative close path in Run() mode. sync.Once ensures both
	// paths never double-close (Close is idempotent, but this prevents
	// racing on the error log paths).
	var closeOnce sync.Once
	doClose := func() {
		closeOnce.Do(func() {
			if err := session.Close(); err != nil {
				logger.Error("REST bridge client session close error", slog.Any("error", err))
			}
			if err := serverSession.Close(); err != nil {
				logger.Error("REST bridge server session close error", slog.Any("error", err))
			}
		})
	}

	go func() {
		<-ctx.Done()
		doClose()
	}()

	cleanup := doClose

	logger.Info("REST bridge enabled",
		slog.String("prefix", prefix),
	)
	return cleanup, nil
}

// handleListTools returns all available tools as a JSON array.
// When BearerAuth.ToolFilter is configured, tools the caller cannot access
// are filtered out — matching the behaviour of the MCP tools/list path.
func (b *restBridge) handleListTools(w http.ResponseWriter, r *http.Request) {
	tools, err := b.getTools(r.Context())
	if err != nil {
		b.writeError(w, http.StatusInternalServerError, "failed to list tools", err)
		return
	}
	tools = b.applyToolFilter(r.Context(), tools)
	if tools == nil {
		tools = []*mcp.Tool{}
	}
	b.writeJSON(w, http.StatusOK, tools)
}

// applyToolFilter returns the subset of tools the caller is allowed to see
// according to the configured BearerAuth.ToolFilter. When no filter is set,
// or BearerAuth itself is not configured, the input slice is returned
// unchanged.
func (b *restBridge) applyToolFilter(ctx context.Context, tools []*mcp.Tool) []*mcp.Tool {
	if b.cfg.BearerAuth == nil || b.cfg.BearerAuth.ToolFilter == nil {
		return tools
	}
	info := auth.TokenInfoFromContext(ctx)
	out := make([]*mcp.Tool, 0, len(tools))
	for _, t := range tools {
		if b.cfg.BearerAuth.ToolFilter(ctx, t.Name, info) {
			out = append(out, t)
		}
	}
	return out
}

// toolPermitted returns true when the caller may invoke or inspect toolName.
// For requests that bypassed bearer auth (LoopbackBypass) TokenInfo is nil,
// matching the contract of the MCP-layer toolFilterMiddleware.
func (b *restBridge) toolPermitted(ctx context.Context, toolName string) bool {
	if b.cfg.BearerAuth == nil || b.cfg.BearerAuth.ToolFilter == nil {
		return true
	}
	info := auth.TokenInfoFromContext(ctx)
	return b.cfg.BearerAuth.ToolFilter(ctx, toolName, info)
}

// handleGetTool returns a single tool's schema, or 404 if not found.
func (b *restBridge) handleGetTool(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !validToolName.MatchString(name) {
		b.writeError(w, http.StatusBadRequest, "invalid tool name", nil)
		return
	}

	tool, err := b.getTool(r.Context(), name)
	if err != nil {
		b.writeError(w, http.StatusInternalServerError, "failed to list tools", err)
		return
	}
	if tool == nil {
		b.writeError(w, http.StatusNotFound, "tool not found: "+name, nil)
		return
	}
	// Hide tools the caller is not permitted to see — return 404 (not 403)
	// so probing for tool existence does not leak the per-token tool catalog.
	if !b.toolPermitted(r.Context(), name) {
		b.writeError(w, http.StatusNotFound, "tool not found: "+name, nil)
		return
	}

	b.writeJSON(w, http.StatusOK, tool)
}

// handleCallTool invokes an MCP tool and returns the result as JSON.
func (b *restBridge) handleCallTool(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !validToolName.MatchString(name) {
		b.writeError(w, http.StatusBadRequest, "invalid tool name", nil)
		return
	}

	// Enforce ToolFilter at the HTTP layer. The in-process MCP client used by
	// this bridge does not propagate HTTP request context into the server's
	// MCP middleware (RequestExtra.TokenInfo is unset on in-memory transport),
	// so the MCP-layer toolFilterMiddleware would see nil and permit
	// everything. Mirror the same filter call here to close the gap.
	if !b.toolPermitted(r.Context(), name) {
		b.writeError(w, http.StatusForbidden, "tool not permitted: "+name, nil)
		return
	}

	args, err := parseRequestBody(r)
	if err != nil {
		b.writeError(w, http.StatusBadRequest, "invalid request body", err)
		return
	}

	result, err := b.session.CallTool(r.Context(), &mcp.CallToolParams{
		Name:      name,
		Arguments: args,
	})
	if err != nil {
		b.writeError(w, http.StatusInternalServerError, "tool call failed", err)
		return
	}

	status := http.StatusOK
	if result.IsError {
		status = http.StatusUnprocessableEntity
	}

	resp := toolCallResponse{
		Content:    result.Content,
		Structured: result.StructuredContent,
		IsError:    result.IsError,
	}
	b.writeJSON(w, status, resp)
}

// handleOpenAPI generates and returns an OpenAPI 3.1 spec from tool schemas.
func (b *restBridge) handleOpenAPI(w http.ResponseWriter, r *http.Request) {
	tools, err := b.getTools(r.Context())
	if err != nil {
		b.writeError(w, http.StatusInternalServerError, "failed to list tools", err)
		return
	}

	tools = b.applyToolFilter(r.Context(), tools)
	spec := b.buildOpenAPISpec(tools)
	b.writeJSON(w, http.StatusOK, spec)
}

// toolCallResponse is the JSON envelope for tool call results.
type toolCallResponse struct {
	Content    []mcp.Content `json:"content"`
	Structured any           `json:"structured,omitempty"`
	IsError    bool          `json:"is_error"`
}

// getTools returns the cached tools list, refreshing if older than cacheTTL.
//
// A TTL cache (rather than a sync.Once) is used so dynamically registered
// tools become visible without restarting the server. Default TTL is 60s,
// so tools/list is at most as stale as the configured TTL.
func (b *restBridge) getTools(ctx context.Context) ([]*mcp.Tool, error) {
	b.cacheMu.Lock()
	defer b.cacheMu.Unlock()
	if b.cachedAt.IsZero() || time.Since(b.cachedAt) > b.cacheTTL {
		tools, err := b.listAllTools(ctx)
		b.cachedTools = tools
		b.cachedErr = err
		if err == nil {
			m := make(map[string]*mcp.Tool, len(tools))
			for _, t := range tools {
				m[t.Name] = t
			}
			b.cachedByName = m
		}
		b.cachedAt = time.Now()
	}
	return b.cachedTools, b.cachedErr
}

// getTool returns a single tool by name, or nil if not found.
func (b *restBridge) getTool(ctx context.Context, name string) (*mcp.Tool, error) {
	if _, err := b.getTools(ctx); err != nil {
		return nil, err
	}
	b.cacheMu.Lock()
	defer b.cacheMu.Unlock()
	return b.cachedByName[name], nil
}

// listAllTools fetches all tools using pagination.
func (b *restBridge) listAllTools(ctx context.Context) ([]*mcp.Tool, error) {
	var all []*mcp.Tool
	var cursor string

	for {
		params := &mcp.ListToolsParams{Cursor: cursor}
		result, err := b.session.ListTools(ctx, params)
		if err != nil {
			return nil, err
		}
		all = append(all, result.Tools...)
		if result.NextCursor == "" {
			break
		}
		cursor = result.NextCursor
	}
	return all, nil
}

// parseRequestBody reads the request body as a map of arguments.
// An empty body is treated as an empty arguments map.
// Bodies larger than 1 MB are rejected.
func parseRequestBody(r *http.Request) (map[string]any, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodySize+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxBodySize {
		return nil, errors.New("request body too large")
	}
	if len(body) == 0 {
		return map[string]any{}, nil
	}
	var args map[string]any
	if err := json.Unmarshal(body, &args); err != nil {
		return nil, err
	}
	return args, nil
}

// buildOpenAPISpec generates an OpenAPI 3.1 specification from tool definitions.
func (b *restBridge) buildOpenAPISpec(tools []*mcp.Tool) map[string]any {
	paths := make(map[string]any, len(tools))
	for _, t := range tools {
		paths[b.prefix+"/tools/"+t.Name] = buildToolPath(t)
	}

	spec := map[string]any{
		"openapi": openAPIVersion,
		"info": map[string]string{
			"title":   b.cfg.Name,
			"version": b.cfg.Version,
		},
		"paths": paths,
	}

	if b.cfg.BearerAuth != nil {
		spec["components"] = map[string]any{
			"securitySchemes": map[string]any{
				"bearerAuth": map[string]string{
					jsonKeyType: "http",
					"scheme":    "bearer",
				},
			},
		}
		spec["security"] = []map[string]any{
			{"bearerAuth": []string{}},
		}
	}

	return spec
}

// toolResponseSchema is the OpenAPI schema for tool call responses.
var toolResponseSchema = map[string]any{
	jsonKeyType: jsonTypeObject,
	"properties": map[string]any{
		jsonKeyContent: map[string]any{jsonKeyType: jsonTypeArray, "items": map[string]string{jsonKeyType: jsonTypeObject}},
		"structured":   map[string]string{jsonKeyType: jsonTypeObject},
		"is_error":     map[string]string{jsonKeyType: jsonTypeBoolean},
	},
}

// toolResponses defines the standard OpenAPI response set for tool endpoints.
var toolResponses = map[string]any{
	"200": map[string]any{
		jsonKeyDescription: "Successful tool call",
		jsonKeyContent: map[string]any{
			contentTypeJSON: map[string]any{"schema": toolResponseSchema},
		},
	},
	"400": map[string]any{jsonKeyDescription: "Invalid request"},
	"422": map[string]any{jsonKeyDescription: "Tool returned an error"},
	"500": map[string]any{jsonKeyDescription: "Internal server error"},
}

// buildToolPath creates the OpenAPI path item for a single tool.
func buildToolPath(t *mcp.Tool) map[string]any {
	op := map[string]any{
		"operationId": t.Name,
		"responses":   toolResponses,
	}
	if t.Description != "" {
		op["summary"] = t.Description
	}
	if t.InputSchema != nil {
		op["requestBody"] = buildRequestBody(t.InputSchema)
	}
	return map[string]any{"post": op}
}

// buildRequestBody creates an OpenAPI request body from a tool input schema.
func buildRequestBody(schema any) map[string]any {
	return map[string]any{
		jsonKeyContent: map[string]any{
			contentTypeJSON: map[string]any{
				"schema": normalizeSchema(schema),
			},
		},
	}
}

// normalizeSchema converts the InputSchema (which may be *jsonschema.Schema
// or map[string]any) into a plain map suitable for JSON marshaling.
func normalizeSchema(schema any) any {
	if schema == nil {
		return nil
	}
	// If it's already a map, use it directly.
	if _, ok := schema.(map[string]any); ok {
		return schema
	}
	// Otherwise, round-trip through JSON to get a plain map.
	data, err := json.Marshal(schema)
	if err != nil {
		return schema
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return schema
	}
	return m
}

// writeJSON writes a JSON response with the given status code.
func (b *restBridge) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", contentTypeJSON)
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		b.logger.Error("failed to write JSON response", slog.Any("error", err))
	}
}

// writeError writes a JSON error response and logs the error.
func (b *restBridge) writeError(w http.ResponseWriter, status int, msg string, err error) {
	if err != nil {
		b.logger.Error(msg, slog.Any("error", err))
	} else {
		b.logger.Error(msg)
	}
	w.Header().Set("Content-Type", contentTypeJSON)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
