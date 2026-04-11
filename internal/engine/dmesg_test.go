package engine

import (
	"strings"
	"testing"
	"time"
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
			if !e.Timestamp.IsZero() {
				t.Errorf("boot-relative dmesg should yield zero Timestamp, got %v", e.Timestamp)
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

func TestParseDmesgOOM_CtimePrefix(t *testing.T) {
	// dmesg --ctime emits a `[Day Mon DD HH:MM:SS YYYY]` prefix per line.
	input := `[Sat Apr 11 02:30:15 2026] oom-kill:constraint=CONSTRAINT_MEMCG,cpuset=docker-3549dc1e6b83.scope,task=chrome,pid=4033698
[Sat Apr 11 02:30:15 2026] Memory cgroup out of memory: Killed process 4033698 (chrome) total-vm:1459430400kB, anon-rss:93184kB, file-rss:0kB, shmem-rss:0kB, UID:0 pgtables:1024kB oom_score_adj:200`

	events := ParseDmesgOOM(input)
	if len(events) != 1 {
		t.Fatalf("expected 1 OOM event, got %d", len(events))
	}
	e := events[0]
	if e.Process != "chrome" {
		t.Errorf("expected chrome, got %s", e.Process)
	}
	if e.Timestamp.IsZero() {
		t.Fatal("expected non-zero Timestamp from ctime prefix")
	}
	want := time.Date(2026, time.April, 11, 2, 30, 15, 0, time.Local)
	if !e.Timestamp.Equal(want) {
		t.Errorf("timestamp mismatch: got %v, want %v", e.Timestamp, want)
	}
	if e.Container != "3549dc1e6b83" {
		t.Errorf("expected container 3549dc1e6b83, got %q", e.Container)
	}
}

func TestParseDmesgOOM_CtimeSingleDigitDay(t *testing.T) {
	// `dmesg --ctime` pads single-digit days with a space: `[Wed Apr  2 ...]`.
	input := `[Wed Apr  2 14:05:00 2026] Memory cgroup out of memory: Killed process 1234 (test) total-vm:1024kB, anon-rss:512kB, file-rss:0kB, shmem-rss:0kB, UID:0 pgtables:0kB oom_score_adj:0`
	events := ParseDmesgOOM(input)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Timestamp.IsZero() {
		t.Errorf("expected non-zero timestamp for single-digit day format")
	}
}

func TestFormatOOMReport_Empty(t *testing.T) {
	result := FormatOOMReport(nil)
	if result != "No OOM events found in kernel log." {
		t.Errorf("unexpected: %s", result)
	}
}

func TestFormatOOMReport_FreshnessActive(t *testing.T) {
	now := time.Date(2026, time.April, 11, 2, 35, 0, 0, time.Local)
	events := []OOMEvent{
		{
			Timestamp: now.Add(-2 * time.Minute),
			Process:   "chrome",
			PID:       1234,
			TotalVMKB: 1024,
			AnonRSSKB: 512,
			Container: "abc123",
		},
	}
	out := formatOOMReportAt(events, now)
	if !strings.Contains(out, "STATUS: ACTIVE") {
		t.Errorf("expected ACTIVE banner for 2-minute-old event, got:\n%s", out)
	}
	if !strings.Contains(out, "2m ago") {
		t.Errorf("expected '2m ago' marker, got:\n%s", out)
	}
}

func TestFormatOOMReport_FreshnessHistorical(t *testing.T) {
	now := time.Date(2026, time.April, 11, 2, 35, 0, 0, time.Local)
	events := []OOMEvent{
		{
			Timestamp: now.Add(-6 * time.Hour),
			Process:   "chrome",
			PID:       1234,
			TotalVMKB: 1024,
			AnonRSSKB: 512,
			Container: "abc123",
		},
	}
	out := formatOOMReportAt(events, now)
	if !strings.Contains(out, "STATUS: HISTORICAL") {
		t.Errorf("expected HISTORICAL banner for 6-hour-old event, got:\n%s", out)
	}
	if !strings.Contains(out, "NOT a current incident") {
		t.Errorf("expected explicit NOT-an-incident phrasing, got:\n%s", out)
	}
}

func TestFormatOOMReport_FreshnessStale(t *testing.T) {
	now := time.Date(2026, time.April, 11, 2, 35, 0, 0, time.Local)
	events := []OOMEvent{
		{
			Timestamp: now.Add(-72 * time.Hour),
			Process:   "old-proc",
			PID:       100,
			TotalVMKB: 1024,
			AnonRSSKB: 512,
		},
	}
	out := formatOOMReportAt(events, now)
	if !strings.Contains(out, "STATUS: STALE") {
		t.Errorf("expected STALE banner for 3-day-old event, got:\n%s", out)
	}
	if !strings.Contains(out, "3d ago") {
		t.Errorf("expected '3d ago' marker, got:\n%s", out)
	}
}

func TestFormatOOMReport_NoTimestamps(t *testing.T) {
	// Boot-relative dmesg yields zero timestamps; the report should warn the consumer.
	events := []OOMEvent{
		{
			Process:   "ox-codes",
			PID:       1,
			TotalVMKB: 1024,
			AnonRSSKB: 512,
		},
	}
	out := FormatOOMReport(events)
	if !strings.Contains(out, "STATUS: UNKNOWN-AGE") {
		t.Errorf("expected UNKNOWN-AGE banner when timestamps are missing, got:\n%s", out)
	}
	if !strings.Contains(out, "no timestamp") {
		t.Errorf("expected per-event 'no timestamp' marker, got:\n%s", out)
	}
}
