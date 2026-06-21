package tools

import (
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestRegisterAllWithOpts_NoPanic is a registration smoke test. It exercises
// the mcp.AddTool path for every registered tool so that jsonschema-tag parse
// panics (e.g. "key=value" first element, which crashes AddTool as seen in
// PR #122/#123) are caught at test time rather than at process start.
//
// This does NOT test tool behaviour — it verifies that the schema reflection
// of every input struct succeeds without panic.
func TestRegisterAllWithOpts_NoPanic(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "smoke-test", Version: "0.0.0"}, nil)
	opts := ExecOptions{Config: NewExecConfig()}

	// Must not panic.
	RegisterAllWithOpts(server, nil, opts)
}
