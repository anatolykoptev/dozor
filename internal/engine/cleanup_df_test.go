package engine

import (
	"context"
	"strings"
	"testing"
)

// dfMockTransport is a Transporter stub for df-measurement tests.
// It intercepts "df -BM" commands and returns canned output.
// All other commands (which, journalctl, du, find, rm) return success with empty stdout
// so the cleanX functions proceed without error but do nothing real.
type dfMockTransport struct {
	// dfResponses maps call index → canned df stdout (to simulate before/after).
	dfResponses []string
	dfCallIdx   int
}

func (m *dfMockTransport) ExecuteUnsafe(_ context.Context, cmd string) CommandResult {
	if len(cmd) >= 5 && cmd[:5] == "df -B" {
		if m.dfCallIdx < len(m.dfResponses) {
			out := m.dfResponses[m.dfCallIdx]
			m.dfCallIdx++
			return CommandResult{Success: true, Stdout: out}
		}
		return CommandResult{Success: true, Stdout: "Avail\n0M\n"}
	}
	// Simulate tool-present: "which <tool>" returns the tool path so probe() returns true.
	if len(cmd) > 6 && cmd[:6] == "which " {
		tool := strings.TrimSuffix(strings.TrimPrefix(cmd, "which "), " 2>/dev/null")
		return CommandResult{Success: true, Stdout: "/usr/bin/" + tool}
	}
	// All other commands succeed with empty/harmless output.
	return CommandResult{Success: true, Stdout: "done"}
}

func (m *dfMockTransport) DockerCommand(_ context.Context, _ string) CommandResult {
	return CommandResult{Success: true, Stdout: ""}
}

func (m *dfMockTransport) DockerComposeCommand(_ context.Context, _ string) CommandResult {
	return CommandResult{Success: true, Stdout: ""}
}

func (m *dfMockTransport) ResolveComposePath() string { return "" }

func dfOutput(mb int) string {
	return "Avail\n" + itoa(mb) + "M\n"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	if neg {
		s = "-" + s
	}
	return s
}

// --- cleanCaches df-delta tests ---

func TestCleanCaches_FreedFromDfDelta(t *testing.T) {
	t.Parallel()

	// before=10000MB, after=12000MB → freed=2000MB (df delta, positive)
	mock := &dfMockTransport{
		dfResponses: []string{
			dfOutput(10000),
			dfOutput(12000),
		},
	}
	c := &CleanupCollector{transport: mock}
	tgt := c.cleanCaches(context.Background())

	if tgt.Freed != "2000.0 MB" {
		t.Errorf("cleanCaches: expected Freed=2000.0 MB (df delta), got %q", tgt.Freed)
	}
}

func TestCleanCaches_NegativeDeltaClampedToZero(t *testing.T) {
	t.Parallel()

	// before=10000MB, after=9500MB → negative delta; clamp to 0.
	mock := &dfMockTransport{
		dfResponses: []string{
			dfOutput(10000),
			dfOutput(9500),
		},
	}
	c := &CleanupCollector{transport: mock}
	tgt := c.cleanCaches(context.Background())

	if tgt.Freed != "0.0 MB" {
		t.Errorf("cleanCaches negative delta: expected Freed=0.0 MB, got %q", tgt.Freed)
	}
}

// --- Table-driven: all cleanX functions follow the df-delta contract ---

func TestCleanAll_FreedFromDfDelta_TableDriven(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		before  int
		after   int
		wantMB  string
		runClean func(c *CleanupCollector, ctx context.Context) CleanupTarget
	}{
		{
			name:   "cleanJournal_positive_delta",
			before: 5000, after: 5800,
			wantMB: "800.0 MB",
			runClean: func(c *CleanupCollector, ctx context.Context) CleanupTarget {
				return c.cleanJournal(ctx, "7d")
			},
		},
		{
			name:   "cleanJournal_negative_clamped",
			before: 5000, after: 4800,
			wantMB: "0.0 MB",
			runClean: func(c *CleanupCollector, ctx context.Context) CleanupTarget {
				return c.cleanJournal(ctx, "7d")
			},
		},
		{
			name:   "cleanTmp_positive_delta",
			before: 3000, after: 3500,
			wantMB: "500.0 MB",
			runClean: func(c *CleanupCollector, ctx context.Context) CleanupTarget {
				return c.cleanTmp(ctx, "7d")
			},
		},
		{
			name:   "cleanTmp_negative_clamped",
			before: 3000, after: 2900,
			wantMB: "0.0 MB",
			runClean: func(c *CleanupCollector, ctx context.Context) CleanupTarget {
				return c.cleanTmp(ctx, "7d")
			},
		},
		{
			name:   "cleanCaches_zero_delta",
			before: 8000, after: 8000,
			wantMB: "0.0 MB",
			runClean: func(c *CleanupCollector, ctx context.Context) CleanupTarget {
				return c.cleanCaches(ctx)
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mock := &dfMockTransport{
				dfResponses: []string{dfOutput(tc.before), dfOutput(tc.after)},
			}
			c := &CleanupCollector{transport: mock}
			tgt := tc.runClean(c, context.Background())
			if tgt.Freed != tc.wantMB {
				t.Errorf("%s: expected Freed=%q (df delta), got %q", tc.name, tc.wantMB, tgt.Freed)
			}
		})
	}
}

// --- parseDfAvailMB unit tests ---

func TestParseDfAvailMB(t *testing.T) {
	t.Parallel()

	cases := []struct {
		input string
		want  float64
	}{
		{"Avail\n10000M\n", 10000},
		{"Avail\n0M\n", 0},
		{"Avail\n1234M\n", 1234},
		{"", 0},
		{"Avail\nnot-a-number\n", 0},
	}
	for _, tc := range cases {
		got := parseDfAvailMB(tc.input)
		if got != tc.want {
			t.Errorf("parseDfAvailMB(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}
