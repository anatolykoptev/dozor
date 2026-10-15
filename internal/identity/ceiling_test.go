package identity

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/anatolykoptev/dozor/internal/agentsurface"
	"github.com/anatolykoptev/go-mcpserver/keyfile"
	"github.com/modelcontextprotocol/go-sdk/auth"
)

// tokenInfo returns what the real key-file verifier produces for a key with
// these attrs, so the tests do not depend on keyfile's internal layout. The
// store is opened without ValidateAttrs so any role can be exercised.
func tokenInfo(t *testing.T, attrs map[string]string) *auth.TokenInfo {
	t.Helper()
	token, hash, err := keyfile.Mint(TokenPrefix)
	if err != nil {
		t.Fatal(err)
	}
	var kv []string
	for k, v := range attrs {
		kv = append(kv, fmt.Sprintf("%s: %q", k, v))
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "keys.yaml")
	content := fmt.Sprintf("version: 1\nkeys:\n  - name: kk\n    sha256: %s\n    expires: %s\n    tools: [agent_status]\n    attrs: {%s}\nend: true\n",
		hash, time.Now().UTC().Add(time.Hour).Format(time.RFC3339), strings.Join(kv, ", "))
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	kf, err := keyfile.Open(keyfile.Options{Path: path, Prefix: TokenPrefix, Tools: agentsurface.ToolNames, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	info, err := kf.Verifier()(t.Context(), token, nil)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func roleInfo(t *testing.T, role string) *auth.TokenInfo {
	return tokenInfo(t, map[string]string{AttrRole: role, AttrRunAs: "muse"})
}

// RED-on-revert: make Ceiling return next unconditionally (keyfile's own
// filter only), and a key whose list names more than its role allows passes.
func TestCeiling_DeniesToolOutsideRole(t *testing.T) {
	allowAll := func(context.Context, string, *auth.TokenInfo) bool { return true }
	f := Ceiling(allowAll)
	info := roleInfo(t, RoleAgentUser)
	for _, tool := range agentsurface.ToolNames {
		if !f(t.Context(), tool, info) {
			t.Errorf("agent tool %s denied for agent-user", tool)
		}
	}
	for _, tool := range []string{"server_exec", "server_deploy", "metric-pull", ""} {
		if f(t.Context(), tool, info) {
			t.Errorf("%q allowed although the role's tool set excludes it", tool)
		}
	}
	if f(t.Context(), "agent_exec", roleInfo(t, "operator")) || f(t.Context(), "agent_exec", roleInfo(t, "")) {
		t.Error("unknown role allowed")
	}
	if f(t.Context(), "agent_exec", nil) {
		t.Error("nil identity allowed")
	}
}

// The key's own list still narrows within the ceiling.
func TestCeiling_KeyListNarrows(t *testing.T) {
	only := func(_ context.Context, tool string, _ *auth.TokenInfo) bool { return tool == "agent_status" }
	f := Ceiling(only)
	if !f(t.Context(), "agent_status", roleInfo(t, RoleAgentUser)) || f(t.Context(), "agent_exec", roleInfo(t, RoleAgentUser)) {
		t.Error("key list not honoured inside the ceiling")
	}
}

func TestEffectiveTools(t *testing.T) {
	got := EffectiveTools(RoleAgentUser, []string{"server_exec", "agent_status", "agent_exec"})
	if want := []string{"agent_exec", "agent_status"}; !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if EffectiveTools("operator", agentsurface.ToolNames) != nil {
		t.Error("unknown role has tools")
	}
}
