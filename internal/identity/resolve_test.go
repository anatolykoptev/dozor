package identity

import (
	"errors"
	"testing"

	"github.com/anatolykoptev/dozor/internal/agentsurface"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func reqWith(info *auth.TokenInfo) *mcp.CallToolRequest {
	return &mcp.CallToolRequest{Extra: &mcp.RequestExtra{TokenInfo: info}}
}

func TestRunAsResolver(t *testing.T) {
	if id, err := RunAsResolver(t.Context(), reqWith(roleInfo(t, RoleAgentUser))); err != nil || id != "muse" {
		t.Fatalf("got %q, %v", id, err)
	}
	bad := tokenInfo(t, map[string]string{AttrRole: RoleAgentUser, AttrRunAs: "root"})
	for name, r := range map[string]*mcp.CallToolRequest{
		"nil request":   nil,
		"no extra":      {},
		"no token info": reqWith(nil),
		"wrong role":    reqWith(roleInfo(t, "operator")),
		"reserved user": reqWith(bad),
	} {
		if _, err := RunAsResolver(t.Context(), r); !errors.Is(err, agentsurface.ErrDenied) {
			t.Errorf("%s: err = %v, want ErrDenied", name, err)
		}
	}
}
