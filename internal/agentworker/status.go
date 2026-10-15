package agentworker

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/anatolykoptev/dozor/internal/agentproto"
)

const (
	defaultCgroupRoot = "/sys/fs/cgroup"
	defaultProcCgroup = "/proc/self/cgroup"
	sliceSuffix       = ".slice"
)

// Status reports the worker's uid, home, the statfs view of home (the quota
// view) and the agent slice's cgroup v2 counters. Missing pieces are omitted,
// not faked.
func (c *Config) Status() *agentproto.StatusResult {
	res := &agentproto.StatusResult{
		UID:          os.Getuid(),
		Home:         c.Home,
		LoopbackCIDR: agentproto.LoopbackCIDR,
		PortRange:    fmt.Sprintf("%d-%d", agentproto.PortRangeLow, agentproto.PortRangeHigh),
		Notes: []string{
			"localhost resolves to " + agentproto.LoopbackCIDR + "; programs that hardcode 127.0.0.1 cannot reach their own servers",
		},
	}
	res.Notes = append(res.Notes, c.Notes...)
	var st syscall.Statfs_t
	if err := syscall.Statfs(c.Home, &st); err == nil {
		bs := uint64(st.Bsize) //nolint:gosec // block size is positive
		res.FS = &agentproto.FSStat{
			TotalBytes: st.Blocks * bs, FreeBytes: st.Bavail * bs,
			TotalInodes: st.Files, FreeInodes: st.Ffree,
		}
	}
	res.Cgroup = c.cgroupStat()
	return res
}

func (c *Config) cgroupStat() *agentproto.CgroupStat {
	root, proc := c.CgroupRoot, c.ProcCgroup
	if root == "" {
		root = defaultCgroupRoot
	}
	if proc == "" {
		proc = defaultProcCgroup
	}
	raw, err := os.ReadFile(proc)
	if err != nil {
		return nil
	}
	rel := sliceRel(string(raw))
	if rel == "" {
		return nil
	}
	dir := filepath.Join(root, rel)
	cs := &agentproto.CgroupStat{Slice: filepath.Base(rel)}
	cs.MemoryCurrent = readInt(filepath.Join(dir, "memory.current"))
	cs.MemoryMax = readInt(filepath.Join(dir, "memory.max"))
	cs.PidsCurrent = readInt(filepath.Join(dir, "pids.current"))
	cs.PidsMax = readInt(filepath.Join(dir, "pids.max"))
	cs.CPUNrThrottled, cs.CPUThrottledUsec = readCPUStat(filepath.Join(dir, "cpu.stat"))
	return cs
}

// sliceRel extracts the path of the outermost *.slice cgroup from the
// cgroup v2 line ("0::/a.slice/b.service") of /proc/self/cgroup.
func sliceRel(procCgroup string) string {
	for _, line := range strings.Split(procCgroup, "\n") {
		path, ok := strings.CutPrefix(line, "0::")
		if !ok {
			continue
		}
		var parts []string
		for _, seg := range strings.Split(path, "/") {
			if seg == "" {
				continue
			}
			parts = append(parts, seg)
			if strings.HasSuffix(seg, sliceSuffix) {
				return filepath.Join(parts...)
			}
		}
	}
	return ""
}

// readInt parses a cgroup file holding one integer or "max" (-1); -2 marks
// an unreadable file.
func readInt(path string) int64 {
	b, err := os.ReadFile(path)
	if err != nil {
		return -2
	}
	s := strings.TrimSpace(string(b))
	if s == "max" {
		return -1
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return -2
	}
	return v
}

func readCPUStat(path string) (nr, usec int64) {
	b, err := os.ReadFile(path)
	if err != nil {
		return -2, -2
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 { //nolint:mnd // "key value"
			continue
		}
		v, err := strconv.ParseInt(f[1], 10, 64)
		if err != nil {
			continue
		}
		switch f[0] {
		case "nr_throttled":
			nr = v
		case "throttled_usec":
			usec = v
		}
	}
	return nr, usec
}
