package agentworker

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"strings"
	"testing"

	"github.com/anatolykoptev/dozor/internal/agentproto"
)

// roundTrip sends raw bytes as the whole request and decodes the reply.
func roundTrip(t *testing.T, c *Config, raw string) agentproto.Response {
	t.Helper()
	server, client := socketPair(t)
	srvConn, err := net.FileConn(server)
	if err != nil {
		t.Fatal(err)
	}
	defer srvConn.Close()
	done := make(chan error, 1)
	go func() { done <- c.Serve(context.Background(), srvConn, server) }()
	go func() {
		_, _ = io.WriteString(client, raw)
		_ = client.(*net.UnixConn).CloseWrite()
	}()
	b, err := io.ReadAll(client)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("serve: %v", err)
	}
	var resp agentproto.Response
	if err := json.Unmarshal(b, &resp); err != nil {
		t.Fatalf("reply %q: %v", b, err)
	}
	return resp
}

func TestServe_HelloCarriesVersionAndBuild(t *testing.T) {
	c := newCfg(t)
	resp := roundTrip(t, c, `{"v":2,"op":"hello"}`)
	if resp.Error != nil || resp.V != agentproto.ProtoVersion || resp.Build != agentproto.BuildID {
		t.Fatalf("%+v", resp)
	}
	// N-1 requests are served too.
	if resp = roundTrip(t, c, `{"v":1,"op":"hello"}`); resp.Error != nil {
		t.Fatalf("N-1 refused: %+v", resp.Error)
	}
}

func TestServe_RejectsMalformed(t *testing.T) {
	c := newCfg(t)
	deep := strings.Repeat("[", 40) + strings.Repeat("]", 40)
	cases := map[string]string{
		"empty":            ``,
		"not json":         `hello`,
		"unknown field":    `{"v":2,"op":"hello","x":1}`,
		"trailing value":   `{"v":2,"op":"hello"}{"v":2,"op":"hello"}`,
		"trailing garbage": `{"v":2,"op":"hello"} x`,
		"future version":   `{"v":3,"op":"hello"}`,
		"old version":      `{"v":0,"op":"hello"}`,
		"missing version":  `{"op":"hello"}`,
		"unknown op":       `{"v":2,"op":"rm"}`,
		"too deep":         `{"v":2,"op":"hello","exec":{"command":` + deep + `}}`,
		"mismatched op":    `{"v":2,"op":"exec"}`,
	}
	for name, raw := range cases {
		resp := roundTrip(t, c, raw)
		if resp.Error == nil || resp.Error.Code != agentproto.CodeBadRequest {
			t.Errorf("%s: got %+v, want bad_request", name, resp)
		}
		if resp.V != agentproto.ProtoVersion {
			t.Errorf("%s: reply must carry our version, got %d", name, resp.V)
		}
	}
}

func TestServe_OversizedRequest(t *testing.T) {
	c := newCfg(t)
	raw := `{"v":2,"op":"hello","exec":{"command":"` + strings.Repeat("a", agentproto.MaxRequestBytes) + `"}}`
	if resp := roundTrip(t, c, raw); resp.Error == nil || !strings.Contains(resp.Error.Message, "too large") {
		t.Fatalf("%+v", resp)
	}
}

func TestServe_EndToEndOps(t *testing.T) {
	c := newCfg(t)
	resp := roundTrip(t, c, `{"v":2,"op":"exec","exec":{"command":"echo hi"}}`)
	if resp.Exec == nil || resp.Exec.Stdout != "hi\n" {
		t.Fatalf("exec: %+v", resp)
	}
	resp = roundTrip(t, c, `{"v":2,"op":"write_file","write_file":{"path":"f","text":"abc"}}`)
	if resp.WriteFile == nil || resp.Error != nil {
		t.Fatalf("write: %+v", resp)
	}
	resp = roundTrip(t, c, `{"v":2,"op":"read_file","read_file":{"path":"f"}}`)
	if resp.ReadFile == nil || resp.ReadFile.Content != "abc" {
		t.Fatalf("read: %+v", resp)
	}
	if resp = roundTrip(t, c, `{"v":2,"op":"status"}`); resp.Status == nil {
		t.Fatalf("status: %+v", resp)
	}
}
