package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Boolean JSON Schemas (`true`, `false`) are legal in a subschema position,
// but several MCP clients type those positions as objects and reject the whole
// tools/list response when they meet a boolean (the MCP Python SDK 2.0 models
// did exactly that, and dropped every tool of the server).
// jsonschema-go emits booleans readily: an empty *Schema marshals as `true`,
// so `any` infers to `properties.x = true`, and map[string]any infers to
// `additionalProperties: true`.
//
// portableSchema rewrites an advertised schema so no boolean sits in a schema
// position. It only changes how the schema is advertised; argument validation
// keeps using the typed schema AddTool resolved.
//
// Rules:
//   - true subschema            -> {} (same meaning: accepts everything)
//   - false subschema           -> {"not": {}} (same meaning: accepts nothing)
//   - additionalProperties / unevaluatedProperties / additionalItems /
//     unevaluatedItems = true   -> key dropped (absent means true)
//   - additionalProperties / unevaluatedProperties = false is kept: it is the
//     closed-object idiom jsonschema-go emits on purpose.
//
// Non-schema values (enum, const, default, examples, `required`, unknown
// keywords) are never touched.
func portableSchema(schema any) any {
	if schema == nil {
		return nil
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		slog.Warn("portableSchema: marshal failed, advertising schema unchanged", slog.Any("error", err))
		return schema
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // keep numeric literals byte-exact through the round trip
	var v any
	if err := dec.Decode(&v); err != nil {
		slog.Warn("portableSchema: decode failed, advertising schema unchanged", slog.Any("error", err))
		return schema
	}
	return normSchema(v)
}

// JSON Schema keywords that need a name because goconst counts repeats
// across the package.
const (
	kwItems      = "items"
	kwProperties = "properties"
	kwAddlProps  = "additionalProperties"
	kwUnevalProp = "unevaluatedProperties"
	kwDefs       = "$defs"
	kwDefinition = "definitions"
	kwDependent  = "dependentSchemas"
	kwPatternPrp = "patternProperties"
)

// Keyword groups by the shape of their value. Everything not listed is
// copied through verbatim.
var (
	// one subschema (items may also be the legacy array form)
	schemaKeys = []string{kwItems, "contains", "propertyNames", "not", "if", "then", "else", "contentSchema"}
	// one subschema; `true` is dropped, `false` is kept
	closedKeys = []string{kwAddlProps, kwUnevalProp}
	// one subschema; `true` is dropped, `false` becomes {"not": {}}
	openKeys = []string{"additionalItems", "unevaluatedItems"}
	// array of subschemas
	schemaListKeys = []string{"prefixItems", "allOf", "anyOf", "oneOf"}
	// map name -> subschema
	schemaMapKeys = []string{kwProperties, kwPatternPrp, kwDefs, kwDefinition, kwDependent}
)

func normSchema(v any) any {
	switch s := v.(type) {
	case bool:
		if s {
			return map[string]any{}
		}
		return map[string]any{"not": map[string]any{}}
	case map[string]any:
		normSchemaObject(s)
		return s
	default:
		return v
	}
}

func normSchemaObject(m map[string]any) {
	for _, k := range schemaKeys {
		normSingle(m, k)
	}
	for _, k := range closedKeys {
		normSingleDropTrue(m, k)
	}
	for _, k := range openKeys {
		normSingleDropTrue(m, k)
	}
	for _, k := range schemaListKeys {
		if arr, ok := m[k].([]any); ok {
			normSchemaList(arr)
		}
	}
	for _, k := range schemaMapKeys {
		normSchemaMap(m[k])
	}
	// draft-07 "dependencies": values are a subschema or an array of names.
	if deps, ok := m["dependencies"].(map[string]any); ok {
		for name, child := range deps {
			if _, isNames := child.([]any); !isNames {
				deps[name] = normSchema(child)
			}
		}
	}
}

// normSingle normalizes a keyword holding one subschema, or (legacy
// "items") an array of them.
func normSingle(m map[string]any, k string) {
	sub, ok := m[k]
	if !ok {
		return
	}
	if arr, isArr := sub.([]any); isArr {
		normSchemaList(arr)
		return
	}
	m[k] = normSchema(sub)
}

// normSingleDropTrue is normSingle for keywords where an absent key already
// means `true`. For closedKeys `false` survives as a boolean; for openKeys it
// is rewritten by normSchema like any other false subschema.
func normSingleDropTrue(m map[string]any, k string) {
	sub, ok := m[k]
	if !ok {
		return
	}
	if b, isBool := sub.(bool); isBool {
		switch {
		case b:
			delete(m, k)
		case k == kwAddlProps || k == kwUnevalProp:
			// closed-object idiom: keep
		default:
			m[k] = normSchema(sub)
		}
		return
	}
	m[k] = normSchema(sub)
}

func normSchemaMap(v any) {
	if sub, ok := v.(map[string]any); ok {
		for name, child := range sub {
			sub[name] = normSchema(child)
		}
	}
}

func normSchemaList(arr []any) {
	for i, el := range arr {
		arr[i] = normSchema(el)
	}
}

// makeToolPortable normalizes the advertised input and output schema of t in
// place. Callers pass a copy they own.
func makeToolPortable(t *mcp.Tool) {
	t.InputSchema = portableSchema(t.InputSchema)
	t.OutputSchema = portableSchema(t.OutputSchema)
}

// portableToolsListMiddleware normalizes every schema in a tools/list result,
// covering tools registered with mcp.AddTool or mcp.Server.AddTool directly
// (not just this package's AddTool). Tools are copied, never mutated in place:
// the SDK shares the registered *mcp.Tool across sessions.
func portableToolsListMiddleware() mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			res, err := next(ctx, method, req)
			if err != nil || method != "tools/list" {
				return res, err
			}
			lr, ok := res.(*mcp.ListToolsResult)
			if !ok || lr == nil {
				return res, err
			}
			out := *lr
			out.Tools = make([]*mcp.Tool, len(lr.Tools))
			for i, t := range lr.Tools {
				if t == nil {
					continue
				}
				tc := *t
				makeToolPortable(&tc)
				out.Tools[i] = &tc
			}
			return &out, nil
		}
	}
}
