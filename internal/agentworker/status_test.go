package agentworker

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/anatolykoptev/dozor/internal/agentproto"
)

func TestSliceRel(t *testing.T) {
	cases := map[string]string{
		"0::/dozor-agent-x.slice/dozor-worker-x@1.service\n": "dozor-agent-x.slice",
		"0::/a.slice/b.slice/c.service\n":                    "a.slice",
		"0::/system.slice\n":                                 "system.slice",
		"0::/init.scope\n":                                   "",
		"1:cpu:/foo.slice\n":                                 "",
		"":                                                   "",
	}
	for in, want := range cases {
		if got := sliceRel(in); got != want {
			t.Errorf("sliceRel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStatus_Cgroup(t *testing.T) {
	c := newCfg(t)
	root := t.TempDir()
	slice := filepath.Join(root, "dozor-agent-x.slice")
	if err := os.MkdirAll(slice, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"memory.current": "1048576\n", "memory.max": "2147483648\n",
		"pids.current": "7\n", "pids.max": "max\n",
		"cpu.stat": "usage_usec 5\nnr_throttled 3\nthrottled_usec 4200\n",
	}
	for n, v := range files {
		if err := os.WriteFile(filepath.Join(slice, n), []byte(v), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	procCg := filepath.Join(t.TempDir(), "cgroup")
	if err := os.WriteFile(procCg, []byte("0::/dozor-agent-x.slice/w.service\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c.CgroupRoot, c.ProcCgroup = root, procCg

	st := c.Status()
	cg := st.Cgroup
	if cg == nil || cg.Slice != "dozor-agent-x.slice" || cg.MemoryCurrent != 1048576 || cg.MemoryMax != 2147483648 ||
		cg.PidsCurrent != 7 || cg.PidsMax != -1 || cg.CPUNrThrottled != 3 || cg.CPUThrottledUsec != 4200 {
		t.Fatalf("cgroup = %+v", cg)
	}
	if st.UID != os.Getuid() || st.Home != c.Home || st.LoopbackCIDR != agentproto.LoopbackCIDR || st.FS == nil || st.FS.TotalBytes == 0 {
		t.Fatalf("status = %+v", st)
	}
	c.ProcCgroup = filepath.Join(t.TempDir(), "absent")
	if c.Status().Cgroup != nil {
		t.Fatal("cgroup stats must be omitted when unreadable")
	}
}
