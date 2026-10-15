package main

import (
	"context"
	"slices"
	"testing"

	"github.com/anatolykoptev/dozor/internal/agentsurface"
	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/anatolykoptev/dozor/internal/toolreg"
	"github.com/anatolykoptev/dozor/internal/tools"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func listToolNames(t *testing.T, srv *mcp.Server) []string {
	t.Helper()
	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "f2", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	lt, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(lt.Tools))
	for _, tool := range lt.Tools {
		names = append(names, tool.Name)
	}
	return names
}

// TestF2_AgentToolsDisjointFromOperatorTools derives the operator tool set
// from the real registration path (core tools plus the extension-registered ones), so a tool
// added tomorrow is checked without anyone updating a list. A non-local host
// keeps the engine from touching Docker.
func TestF2_AgentToolsDisjointFromOperatorTools(t *testing.T) {
	eng := engine.NewAgent(engine.Config{Host: "f2-test.invalid"})
	defer eng.Close()
	op := buildMCPServer(eng, tools.ExecOptions{})
	buildExtensionRegistry(eng, toolreg.NewRegistry(), op, false, nil)
	operator := listToolNames(t, op)
	for _, must := range []string{"server_exec", "server_deploy", "server_extensions"} {
		if !slices.Contains(operator, must) {
			t.Fatalf("operator tool derivation is missing %q: %v", must, operator)
		}
	}

	agent := mcp.NewServer(&mcp.Implementation{Name: "f2-agent", Version: "0"}, nil)
	agentsurface.Register(agent, func(context.Context, *mcp.CallToolRequest) (string, error) { return "", nil }, agentsurface.Config{})
	got := listToolNames(t, agent)

	slices.Sort(got)
	want := slices.Sorted(slices.Values(agentsurface.ToolNames))
	if !slices.Equal(got, want) {
		t.Fatalf("registered agent tools %v != declared ToolNames %v", got, want)
	}
	for _, name := range got {
		if slices.Contains(operator, name) {
			t.Errorf("agent tool %q is also an operator tool", name)
		}
	}
}
