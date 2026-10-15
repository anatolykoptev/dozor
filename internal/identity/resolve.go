package identity

import (
	"context"

	"github.com/anatolykoptev/dozor/internal/agentproto"
	"github.com/anatolykoptev/dozor/internal/agentsurface"
	"github.com/anatolykoptev/go-mcpserver/keyfile"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RunAsResolver is the agentsurface.Resolver for key-file identities. The
// worker identity comes only from the verified token (the key's run_as attr),
// never from tool input; agentsurface maps it to agentproto.SocketPath, which
// admits only a valid agent name.
func RunAsResolver(_ context.Context, req *mcp.CallToolRequest) (string, error) {
	if req == nil || req.Extra == nil || req.Extra.TokenInfo == nil {
		return "", agentsurface.ErrDenied
	}
	attrs := keyfile.AttrsOf(req.Extra.TokenInfo)
	if attrs[AttrRole] != RoleAgentUser || !agentproto.ValidRunAs(attrs[AttrRunAs]) {
		return "", agentsurface.ErrDenied
	}
	return attrs[AttrRunAs], nil
}
