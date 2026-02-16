package main

import (
	"context"
	"fmt"

	"github.com/anatolykoptev/dozor/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerPrune(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "server_prune",
		Description: "Clean up Docker resources (unused images, build cache, volumes). Shows disk usage after cleanup.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input engine.PruneInput) (*mcp.CallToolResult, engine.TextOutput, error) {
		images := true
		if input.Images != nil {
			images = *input.Images
		}
		buildCache := true
		if input.BuildCache != nil {
			buildCache = *input.BuildCache
		}
		volumes := false
		if input.Volumes != nil {
			volumes = *input.Volumes
		}
		age := input.Age
		if age == "" {
			age = "24h"
		}
		if ok, reason := engine.ValidateTimeDuration(age); !ok {
			return nil, engine.TextOutput{}, fmt.Errorf("invalid age: %s", reason)
		}

		result := agent.PruneDocker(ctx, images, buildCache, volumes, age)
		return nil, engine.TextOutput{Text: result}, nil
	})
}
