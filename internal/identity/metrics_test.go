package identity

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/anatolykoptev/dozor/internal/agentsurface"
	"github.com/prometheus/client_golang/prometheus"
)

func TestClientHooks(t *testing.T) {
	var buf bytes.Buffer
	var cfg agentsurface.Config
	ClientHooks(&cfg, slog.New(slog.NewTextHandler(&buf, nil)))

	cfg.OnReply("hook-agent", 2, false)
	if v := gaugeVal(t, "dozor_agent_worker_version_mismatch", "hook-agent"); v != 1 {
		t.Errorf("mismatch gauge = %v after a different build, want 1", v)
	}
	cfg.OnReply("hook-agent", 2, true)
	if v := gaugeVal(t, "dozor_agent_worker_version_mismatch", "hook-agent"); v != 0 {
		t.Errorf("mismatch gauge = %v after a matching build, want 0", v)
	}

	cfg.OnReject("hook-agent", agentsurface.RejectSlots)
	cfg.OnReject("hook-agent", agentsurface.RejectBacklog)
	cfg.OnReject("bad id\n", agentsurface.RejectBudget)
	for _, reason := range []string{"slots", "backlog"} {
		if v := sample(t, "dozor_agent_rejects_total", map[string]string{"identity": "hook-agent", "reason": reason}, false); v != 1 {
			t.Errorf("rejects_total{%s} = %v, want 1", reason, v)
		}
	}
	if v := sample(t, "dozor_agent_rejects_total", map[string]string{"identity": Unknown, "reason": "budget"}, false); v != 1 {
		t.Errorf("an unplain identity must be labelled %q, got %v", Unknown, v)
	}
	out := buf.String()
	if strings.Count(out, "reason=no-reply") != 1 || strings.Contains(out, "reason=slots") {
		t.Errorf("only the backlog path warns, once:\n%s", out)
	}
}

func gaugeVal(t *testing.T, name, id string) float64 {
	t.Helper()
	fams, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fams {
		if f.GetName() != name {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "identity" && l.GetValue() == id {
					return m.GetGauge().GetValue()
				}
			}
		}
	}
	return -1
}
