package mcpself

import "testing"

func TestServerEntry_CarriesTokenWhenSet(t *testing.T) {
	t.Setenv(TokenEnv, "tok")
	e := ServerEntry("http://127.0.0.1:8765/mcp")
	h, ok := e["headers"].(map[string]string)
	if !ok || h["Authorization"] != "Bearer tok" {
		t.Fatalf("headers = %#v, want Authorization: Bearer tok", e["headers"])
	}
	if e["url"] != "http://127.0.0.1:8765/mcp" || e["type"] != "http" {
		t.Fatalf("entry = %#v", e)
	}
}

func TestServerEntry_NoHeaderWithoutToken(t *testing.T) {
	t.Setenv(TokenEnv, "")
	if _, ok := ServerEntry("http://x/mcp")["headers"]; ok {
		t.Fatal("headers present without a token")
	}
}
