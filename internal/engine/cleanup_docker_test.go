package engine

import (
	"context"
	"math"
	"strconv"
	"strings"
	"testing"
)

// dockerAndDfTransport simulates a docker prune that self-reports N MB freed.
// df returns dfBefore until DockerCommand is called (simulating OS lazy freeing:
// disk space is not freed until the prune command actually runs). After DockerCommand
// is called, df returns dfAfter. This means:
//   - If prune runs INSIDE the measureFreedMB window: df delta = dfAfter - dfBefore ✓
//   - If prune runs OUTSIDE the window (before df "before"): df delta = 0 ✗
//
// Used to verify that cleanDockerDangling runs prune inside the closure.
// Also used to verify that cleanDockerBuilderAged does not double-count
// docker's self-report and the df delta.
type dockerAndDfTransport struct {
	dockerFreedOutput string // e.g. "Total reclaimed space: 1.000GB"
	dfBefore          int    // MB returned by df before docker prune runs
	dfAfter           int    // MB returned by df after docker prune runs
	dockerCalled      bool
}

func (d *dockerAndDfTransport) ExecuteUnsafe(_ context.Context, cmd string) CommandResult {
	if strings.Contains(cmd, "df -BM") {
		mb := d.dfBefore
		if d.dockerCalled {
			mb = d.dfAfter
		}
		return CommandResult{Success: true, Stdout: "Avail\n" + strconv.Itoa(mb) + "M\n"}
	}
	if strings.HasPrefix(cmd, "which ") {
		tool := strings.TrimSuffix(strings.TrimPrefix(cmd, "which "), " 2>/dev/null")
		return CommandResult{Success: true, Stdout: "/usr/bin/" + tool}
	}
	return CommandResult{Success: true, Stdout: ""}
}

func (d *dockerAndDfTransport) DockerCommand(_ context.Context, _ string) CommandResult {
	d.dockerCalled = true
	return CommandResult{Success: true, Stdout: d.dockerFreedOutput}
}

func (d *dockerAndDfTransport) DockerComposeCommand(_ context.Context, _ string) CommandResult {
	return CommandResult{Success: true, Stdout: ""}
}

func (d *dockerAndDfTransport) ResolveComposePath() string { return "" }

// TestCleanDockerBuilderAged_NoDoubleCount verifies that when docker self-reports
// 1024 MB freed AND df delta is also 1024 MB, the result is 1024 MB — not 2048 MB.
// The two sources must not be summed.
func TestCleanDockerBuilderAged_NoDoubleCount(t *testing.T) {
	t.Parallel()

	// Docker reports 1024 MB freed; df also shows 1024 MB more free after prune.
	mock := &dockerAndDfTransport{
		dockerFreedOutput: "Total reclaimed space: 1024MB",
		dfBefore:          10000,
		dfAfter:           11024, // 1024 MB freed per df
	}
	c := &CleanupCollector{transport: mock}
	got := c.cleanDockerBuilderAged(context.Background(), "72h")

	const want = 1024.0
	if got.FreedMB != want {
		t.Errorf("cleanDockerBuilderAged: FreedMB = %.1f, want %.1f — docker self-report and df delta must not be double-counted", got.FreedMB, want)
	}
}

// TestCleanDockerDangling_DfDeltaInWindow verifies that prune runs INSIDE the
// measureFreedMB window so the df delta is non-zero when docker output is not
// parseable. The transport returns dfBefore until DockerCommand fires, then
// dfAfter — so df delta is 1024 MB only if prune runs between the two df calls.
// With current code (prune outside closure), docker runs first → both df calls
// see dfAfter → delta = 0, and docker output has no parseable size → FreedMB = 0.
// After fix (prune inside closure): df before = dfBefore, docker runs, df after =
// dfAfter → delta = 1024 MB → FreedMB = 1024.
func TestCleanDockerDangling_DfDeltaInWindow(t *testing.T) {
	t.Parallel()

	// Docker output has no parseable "Total reclaimed space:" line.
	// df changes from 10000→11024 only after docker is called.
	mock := &dockerAndDfTransport{
		dockerFreedOutput: "Deleted images: sha256:abc123",
		dfBefore:          10000,
		dfAfter:           11024,
	}
	c := &CleanupCollector{transport: mock}
	got := c.cleanDockerDangling(context.Background())

	const want = 1024.0
	if got.FreedMB != want {
		t.Errorf("cleanDockerDangling: FreedMB = %.1f, want %.1f — prune must run inside measureFreedMB closure so df brackets the actual free", got.FreedMB, want)
	}
}

func TestComputeDockerReclaimableMB(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantMB  float64
		wantOk  bool
		epsilon float64
	}{
		{
			name:   "invalid JSON returns ok=false",
			raw:    "not json at all",
			wantOk: false,
		},
		{
			name:    "empty response is valid and yields 0 MB",
			raw:     `{"Images":[],"BuildCache":[],"Containers":[],"Volumes":[]}`,
			wantMB:  0,
			wantOk:  true,
			epsilon: 0.001,
		},
		{
			name: "running images contribute nothing",
			raw: `{
				"Images":[{"Size":1048576,"SharedSize":0,"Containers":2}],
				"BuildCache":[],"Containers":[],"Volumes":[]
			}`,
			wantMB:  0,
			wantOk:  true,
			epsilon: 0.001,
		},
		{
			name: "unused image Size minus SharedSize",
			raw: `{
				"Images":[{"Size":10485760,"SharedSize":2097152,"Containers":0}],
				"BuildCache":[],"Containers":[],"Volumes":[]
			}`,
			// (10 MB - 2 MB) = 8 MB
			wantMB:  8,
			wantOk:  true,
			epsilon: 0.001,
		},
		{
			name: "build cache reclaimable only when !InUse",
			raw: `{
				"Images":[],
				"BuildCache":[
					{"Size":1048576,"InUse":true},
					{"Size":5242880,"InUse":false}
				],
				"Containers":[],"Volumes":[]
			}`,
			// 5 MB
			wantMB:  5,
			wantOk:  true,
			epsilon: 0.001,
		},
		{
			name: "stopped containers contribute SizeRw",
			raw: `{
				"Images":[],"BuildCache":[],
				"Containers":[
					{"State":"running","SizeRw":999999999},
					{"State":"exited","SizeRw":3145728}
				],
				"Volumes":[]
			}`,
			// 3 MB
			wantMB:  3,
			wantOk:  true,
			epsilon: 0.001,
		},
		{
			name: "unused volumes contribute Size",
			raw: `{
				"Images":[],"BuildCache":[],"Containers":[],
				"Volumes":[
					{"UsageData":{"RefCount":0,"Size":2097152}},
					{"UsageData":{"RefCount":3,"Size":99999999}}
				]
			}`,
			// 2 MB
			wantMB:  2,
			wantOk:  true,
			epsilon: 0.001,
		},
		{
			name: "Size < SharedSize clamped to 0",
			raw: `{
				"Images":[{"Size":1,"SharedSize":10485760,"Containers":0}],
				"BuildCache":[],"Containers":[],"Volumes":[]
			}`,
			wantMB:  0,
			wantOk:  true,
			epsilon: 0.001,
		},
		{
			name: "sum of all four categories",
			raw: `{
				"Images":[{"Size":10485760,"SharedSize":0,"Containers":0}],
				"BuildCache":[{"Size":5242880,"InUse":false}],
				"Containers":[{"State":"exited","SizeRw":3145728}],
				"Volumes":[{"UsageData":{"RefCount":0,"Size":2097152}}]
			}`,
			// 10 + 5 + 3 + 2 = 20 MB
			wantMB:  20,
			wantOk:  true,
			epsilon: 0.001,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := computeDockerReclaimableMB([]byte(tt.raw))
			if ok != tt.wantOk {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOk)
			}
			if !ok {
				return
			}
			if math.Abs(got-tt.wantMB) > tt.epsilon {
				t.Errorf("MB = %v, want %v (±%v)", got, tt.wantMB, tt.epsilon)
			}
		})
	}
}
