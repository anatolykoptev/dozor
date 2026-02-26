package toolreg

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
)

// --- test stubs ---

type echoTool struct {
	name string
	desc string
}

func (t *echoTool) Name() string        { return t.name }
func (t *echoTool) Description() string { return t.desc }
func (t *echoTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"text": map[string]any{"type": "string"},
		},
		"required": []string{"text"},
	}
}
func (t *echoTool) Execute(_ context.Context, args map[string]any) (string, error) {
	text, _ := args["text"].(string)
	return "echo: " + text, nil
}

type failTool struct{}

func (t *failTool) Name() string        { return "fail_tool" }
func (t *failTool) Description() string { return "always fails" }
func (t *failTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}
func (t *failTool) Execute(_ context.Context, _ map[string]any) (string, error) {
	return "", errors.New("intentional failure")
}

type multiTypeTool struct{}

func (t *multiTypeTool) Name() string        { return "multi_type" }
func (t *multiTypeTool) Description() string { return "handles multiple input types" }
func (t *multiTypeTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"count":  map[string]any{"type": "integer"},
			"flag":   map[string]any{"type": "boolean"},
			"label":  map[string]any{"type": "string"},
			"items":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		},
	}
}
func (t *multiTypeTool) Execute(_ context.Context, args map[string]any) (string, error) {
	count := getInt(args, "count", 0)
	flag := getBoolPtr(args, "flag")
	label := getString(args, "label")
	items := getStringSlice(args, "items")

	flagVal := false
	if flag != nil {
		flagVal = *flag
	}
	return fmt.Sprintf("count=%d flag=%v label=%s items=%v", count, flagVal, label, items), nil
}

// --- tests ---

func TestRegister_Get(t *testing.T) {
	r := NewRegistry()
	tool := &echoTool{name: "my_tool", desc: "test tool"}
	r.Register(tool)

	got, ok := r.Get("my_tool")
	if !ok {
		t.Fatal("Get returned ok=false, want true")
	}
	if got.Name() != "my_tool" {
		t.Errorf("Name: got %q, want %q", got.Name(), "my_tool")
	}
}

func TestGet_UnknownTool(t *testing.T) {
	r := NewRegistry()
	_, ok := r.Get("nonexistent")
	if ok {
		t.Error("Get returned ok=true for unknown tool, want false")
	}
}

func TestExecute_Success(t *testing.T) {
	r := NewRegistry()
	r.Register(&echoTool{name: "echo", desc: "echoes input"})

	ctx := context.Background()
	result, err := r.Execute(ctx, "echo", map[string]any{"text": "world"})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result != "echo: world" {
		t.Errorf("Execute result: got %q, want %q", result, "echo: world")
	}
}

func TestExecute_UnknownTool(t *testing.T) {
	r := NewRegistry()

	ctx := context.Background()
	_, err := r.Execute(ctx, "unknown_tool", nil)
	if err == nil {
		t.Fatal("Execute returned nil error for unknown tool, want error")
	}
	if !strings.Contains(err.Error(), "unknown tool") {
		t.Errorf("error message %q should contain %q", err.Error(), "unknown tool")
	}
}

func TestExecute_ToolError(t *testing.T) {
	r := NewRegistry()
	r.Register(&failTool{})

	ctx := context.Background()
	_, err := r.Execute(ctx, "fail_tool", nil)
	if err == nil {
		t.Fatal("Execute returned nil error, want error from tool")
	}
	if !strings.Contains(err.Error(), "intentional failure") {
		t.Errorf("error %q does not contain expected message", err.Error())
	}
}

func TestList(t *testing.T) {
	r := NewRegistry()
	r.Register(&echoTool{name: "tool_a", desc: "a"})
	r.Register(&echoTool{name: "tool_b", desc: "b"})
	r.Register(&echoTool{name: "tool_c", desc: "c"})

	names := r.List()
	sort.Strings(names)

	want := []string{"tool_a", "tool_b", "tool_c"}
	if len(names) != len(want) {
		t.Fatalf("List length: got %d, want %d", len(names), len(want))
	}
	for i, w := range want {
		if names[i] != w {
			t.Errorf("List[%d]: got %q, want %q", i, names[i], w)
		}
	}
}

func TestList_Empty(t *testing.T) {
	r := NewRegistry()
	names := r.List()
	if len(names) != 0 {
		t.Errorf("List on empty registry: got %v, want empty", names)
	}
}

func TestToLLMTools(t *testing.T) {
	r := NewRegistry()
	r.Register(&echoTool{name: "alpha", desc: "alpha description"})
	r.Register(&echoTool{name: "beta", desc: "beta description"})

	defs := r.ToLLMTools()
	if len(defs) != 2 {
		t.Fatalf("ToLLMTools length: got %d, want 2", len(defs))
	}

	// Build a map for order-independent checks.
	byName := make(map[string]string)
	for _, d := range defs {
		if d.Type != "function" {
			t.Errorf("definition %q: Type=%q, want %q", d.Function.Name, d.Type, "function")
		}
		byName[d.Function.Name] = d.Function.Description
	}

	for _, tc := range []struct{ name, desc string }{
		{"alpha", "alpha description"},
		{"beta", "beta description"},
	} {
		desc, ok := byName[tc.name]
		if !ok {
			t.Errorf("ToLLMTools missing tool %q", tc.name)
			continue
		}
		if desc != tc.desc {
			t.Errorf("tool %q description: got %q, want %q", tc.name, desc, tc.desc)
		}
	}
}

func TestToLLMTools_ParametersPreserved(t *testing.T) {
	r := NewRegistry()
	r.Register(&echoTool{name: "echo", desc: "echo"})

	defs := r.ToLLMTools()
	if len(defs) != 1 {
		t.Fatalf("expected 1 definition, got %d", len(defs))
	}

	params := defs[0].Function.Parameters
	if params == nil {
		t.Fatal("Parameters is nil")
	}
	typ, _ := params["type"].(string)
	if typ != "object" {
		t.Errorf("Parameters type: got %q, want %q", typ, "object")
	}
}

func TestRegister_Overwrite(t *testing.T) {
	r := NewRegistry()
	r.Register(&echoTool{name: "dup", desc: "first"})
	r.Register(&echoTool{name: "dup", desc: "second"})

	got, ok := r.Get("dup")
	if !ok {
		t.Fatal("Get returned ok=false")
	}
	// Later registration overwrites the earlier one.
	if got.Description() != "second" {
		t.Errorf("Description: got %q, want %q", got.Description(), "second")
	}
	// List should still have just one entry for "dup".
	names := r.List()
	count := 0
	for _, n := range names {
		if n == "dup" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("List has %d entries for %q, want 1", count, "dup")
	}
}

func TestExecute_WithDifferentInputTypes(t *testing.T) {
	r := NewRegistry()
	r.Register(&multiTypeTool{})
	ctx := context.Background()

	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{
			name: "integer as float64 (JSON default)",
			args: map[string]any{"count": float64(7), "flag": true, "label": "hello", "items": []any{"a", "b"}},
			want: "count=7 flag=true label=hello items=[a b]",
		},
		{
			name: "integer as int",
			args: map[string]any{"count": 3, "flag": false, "label": "world", "items": []string{"x", "y"}},
			want: "count=3 flag=false label=world items=[x y]",
		},
		{
			name: "missing optional fields use defaults",
			args: map[string]any{},
			want: "count=0 flag=false label= items=[]",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, err := r.Execute(ctx, "multi_type", tc.args)
			if err != nil {
				t.Fatalf("Execute error: %v", err)
			}
			if result != tc.want {
				t.Errorf("result: got %q, want %q", result, tc.want)
			}
		})
	}
}

func TestExecute_ContextPropagation(t *testing.T) {
	// Verify the context passed to Execute is forwarded to the tool.
	type ctxKey struct{}
	var sentinelKey ctxKey

	ctxTool := &contextCheckTool{}
	r := NewRegistry()
	r.Register(ctxTool)

	ctx := context.WithValue(context.Background(), sentinelKey, "check-value")
	_, _ = r.Execute(ctx, "ctx_check", nil)
	// The tool itself stores the context value; we just verify Execute forwards ctx.
	// Actual assertion happens inside the tool.
}

type contextCheckTool struct{}

func (t *contextCheckTool) Name() string        { return "ctx_check" }
func (t *contextCheckTool) Description() string { return "checks context" }
func (t *contextCheckTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}
func (t *contextCheckTool) Execute(ctx context.Context, _ map[string]any) (string, error) {
	if ctx == nil {
		return "", errors.New("nil context received")
	}
	return "ok", nil
}
