package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/anatolykoptev/dozor/internal/agentsurface"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/prometheus/client_golang/prometheus"
)

// counter returns the dozor_mcp_calls_total sample for the exact label set.
func counter(t *testing.T, id, tool, result string) float64 {
	t.Helper()
	return sample(t, "dozor_mcp_calls_total", map[string]string{"identity": id, "tool": tool, "result": result}, false)
}

func sample(t *testing.T, name string, want map[string]string, hist bool) float64 {
	t.Helper()
	fams, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fams {
		if f.GetName() != name {
			continue
		}
	next:
		for _, m := range f.GetMetric() {
			got := map[string]string{}
			for _, l := range m.GetLabel() {
				got[l.GetName()] = l.GetValue()
			}
			for k, v := range want {
				if got[k] != v {
					continue next
				}
			}
			if hist {
				return float64(m.GetHistogram().GetSampleCount())
			}
			return m.GetCounter().GetValue()
		}
	}
	return 0
}

func scrapeAll(t *testing.T) string {
	t.Helper()
	fams, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, f := range fams {
		if strings.HasPrefix(f.GetName(), "dozor_mcp_") {
			fmt.Fprintln(&b, f.String())
		}
	}
	return b.String()
}

func callReq(name string, args any, info *auth.TokenInfo) mcp.Request {
	raw, _ := json.Marshal(args)
	return &mcp.ServerRequest[*mcp.CallToolParamsRaw]{
		Params: &mcp.CallToolParamsRaw{Name: name, Arguments: raw},
		Extra:  &mcp.RequestExtra{TokenInfo: info},
	}
}

// TestAudit_NoArgsNoTokens: the journal line and the metrics carry the
// identity, tool, result and duration, and nothing the caller sent: not an
// argument value, not an error string built from one, not token material.
func TestAudit_NoArgsNoTokens(t *testing.T) {
	const (
		argSecret = "ARGVALUE-7f3a9c"
		tokSecret = "dzk1_TOKENMATERIAL_91b2"
	)
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))

	failing := func(context.Context, string, mcp.Request) (mcp.Result, error) {
		var res mcp.CallToolResult
		res.SetError(errors.New("failed on " + argSecret))
		return &res, nil
	}
	info := &auth.TokenInfo{
		UserID: "k-noargs", Scopes: []string{tokSecret},
		Extra: map[string]any{"token": tokSecret, "hash": tokSecret},
	}
	req := callReq("agent_exec", map[string]any{"command": "echo " + argSecret, "stdin": argSecret}, info)
	if _, err := Audit(log)(failing)(t.Context(), "tools/call", req); err != nil {
		t.Fatal(err)
	}

	line := buf.String()
	for _, want := range []string{"identity=k-noargs", "tool=agent_exec", "result=error", "dur="} {
		if !strings.Contains(line, want) {
			t.Errorf("audit line %q lacks %q", line, want)
		}
	}
	if n := strings.Count(strings.TrimSpace(line), "\n"); n != 0 {
		t.Errorf("want exactly one audit line, got %d", n+1)
	}
	for name, out := range map[string]string{"journal": line, "metrics": scrapeAll(t)} {
		for _, secret := range []string{argSecret, tokSecret} {
			if strings.Contains(out, secret) {
				t.Errorf("%s leaks %q:\n%s", name, secret, out)
			}
		}
	}
	if got := counter(t, "k-noargs", "agent_exec", "error"); got != 1 {
		t.Errorf("calls_total = %v, want 1", got)
	}
	if got := sample(t, "dozor_mcp_call_duration_seconds", map[string]string{"identity": "k-noargs", "tool": "agent_exec"}, true); got != 1 {
		t.Errorf("duration samples = %v, want 1", got)
	}
}

func connect(t *testing.T, srv *mcp.Server) *mcp.ClientSession {
	t.Helper()
	ctx := t.Context()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// The classification must see the tool handler's Go error: a typed error
// returned by a handler registered the normal way reaches the audit as
// busy/denied/unavailable, not a flat "error".
func TestAudit_ClassifiesHandlerError(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "audit-test", Version: "0"}, nil)
	srv.AddReceivingMiddleware(Audit(slog.New(slog.DiscardHandler)))
	type in struct{}
	type out struct{}
	cases := map[string]struct {
		err  error
		want string
	}{
		"audit_t_ok":          {nil, ResultOK},
		"audit_t_busy":        {fmt.Errorf("%w: slots", agentsurface.ErrBusy), agentsurface.ResultBusy},
		"audit_t_denied":      {agentsurface.ErrDenied, agentsurface.ResultDenied},
		"audit_t_unavailable": {fmt.Errorf("dial: %w", agentsurface.ErrUnavailable), agentsurface.ResultUnavailable},
		"audit_t_plain":       {errors.New("boom"), agentsurface.ResultError},
	}
	for name, c := range cases {
		mcp.AddTool(srv, &mcp.Tool{Name: name}, func(context.Context, *mcp.CallToolRequest, in) (*mcp.CallToolResult, out, error) {
			return nil, out{}, c.err
		})
	}
	cs := connect(t, srv)
	for name, c := range cases {
		if _, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{}}); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := counter(t, Anonymous, name, c.want); got != 1 {
			t.Errorf("%s: calls_total{result=%q} = %v, want 1", name, c.want, got)
		}
	}
}

// A made-up tool name must not mint a metric series or a journal token.
func TestAudit_UnknownToolIsNotALabel(t *testing.T) {
	var buf bytes.Buffer
	srv := mcp.NewServer(&mcp.Implementation{Name: "audit-test", Version: "0"}, nil)
	srv.AddReceivingMiddleware(Audit(slog.New(slog.NewTextHandler(&buf, nil))))
	cs := connect(t, srv)

	const bogus = "made-up-tool-5b1e"
	if _, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: bogus}); err == nil {
		t.Fatal("expected an unknown-tool error")
	}
	if strings.Contains(scrapeAll(t), bogus) || strings.Contains(buf.String(), bogus) {
		t.Error("unregistered tool name leaked into a label or the journal")
	}
	if got := counter(t, Anonymous, Unknown, agentsurface.ResultError); got < 1 {
		t.Errorf("unknown-tool call not counted, got %v", got)
	}
}

func TestLabel(t *testing.T) {
	long := strings.Repeat("a", maxLabelLen+1)
	for in, want := range map[string]string{
		"operator": "operator", "muse-1": "muse-1", "": Unknown,
		"a b": Unknown, "x\ny": Unknown, "\"quoted\"": Unknown, long: Unknown, "-lead": Unknown,
	} {
		if got := label(in); got != want {
			t.Errorf("label(%q) = %q, want %q", in, got, want)
		}
	}
}
