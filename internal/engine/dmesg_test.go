package engine

import (
	"testing"
)

func TestParseDmesgOOM(t *testing.T) {
	input := `[3259052.740429] oom-kill:constraint=CONSTRAINT_MEMCG,nodemask=(null),cpuset=docker-93f0eb72.scope,mems_allowed=0,oom_memcg=/system.slice/docker-93f0eb72.scope,task_memcg=/system.slice/docker-93f0eb72.scope,task=ox-codes,pid=3898744,uid=0
[3259052.740479] Memory cgroup out of memory: Killed process 3898744 (ox-codes) total-vm:1954960kB, anon-rss:511624kB, file-rss:1732kB, shmem-rss:0kB, UID:0 pgtables:2220kB oom_score_adj:0
[3259688.872016] Memory cgroup out of memory: Killed process 3519854 (chrome) total-vm:51214360kB, anon-rss:159528kB, file-rss:82048kB, shmem-rss:70724kB, UID:0 pgtables:3128kB oom_score_adj:200`

	events := ParseDmesgOOM(input)
	if len(events) < 2 {
		t.Fatalf("expected at least 2 OOM events, got %d", len(events))
	}

	// Find ox-codes event
	var foundOxCodes, foundChrome bool
	for _, e := range events {
		if e.Process == "ox-codes" {
			foundOxCodes = true
			if e.PID != 3898744 {
				t.Errorf("expected PID 3898744 for ox-codes, got %d", e.PID)
			}
		}
		if e.Process == "chrome" {
			foundChrome = true
		}
	}
	if !foundOxCodes {
		t.Error("ox-codes OOM event not found")
	}
	if !foundChrome {
		t.Error("chrome OOM event not found")
	}
}

func TestFormatOOMReport_Empty(t *testing.T) {
	result := FormatOOMReport(nil)
	if result != "No OOM events found in kernel log." {
		t.Errorf("unexpected: %s", result)
	}
}
