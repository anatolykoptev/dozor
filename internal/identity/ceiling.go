package identity

import (
	"context"
	"slices"

	"github.com/anatolykoptev/dozor/internal/agentsurface"
	"github.com/anatolykoptev/go-mcpserver/keyfile"

	mcpserver "github.com/anatolykoptev/go-mcpserver"
)

// ToolFilter is mcpserver.BearerAuth.ToolFilter.
type ToolFilter = func(ctx context.Context, tool string, info *mcpserver.TokenInfo) bool

// roleTools is the ceiling: the most any key of a role may ever be granted.
// Adding a role is a code change here, never configuration.
var roleTools = map[string][]string{
	RoleAgentUser: agentsurface.ToolNames,
}

// Ceiling caps a key-file tool filter by the key's role. A tool passes only if
// it is in the role's tool set AND next (the key's own list) allows it, so a
// future role can never exceed its table even if an entry names more
// registered tools. A missing identity or an unknown role is denied.
func Ceiling(next ToolFilter) ToolFilter {
	return func(ctx context.Context, tool string, info *mcpserver.TokenInfo) bool {
		if info == nil || !slices.Contains(roleTools[keyfile.AttrsOf(info)[AttrRole]], tool) || !next(ctx, tool, info) {
			markDenied(ctx) // the audit layer above records a refusal as result=denied
			return false
		}
		return true
	}
}

// EffectiveTools is what a key with this role and granted list can call.
func EffectiveTools(role string, granted []string) []string {
	var out []string
	for _, t := range roleTools[role] {
		if slices.Contains(granted, t) {
			out = append(out, t)
		}
	}
	return out
}
