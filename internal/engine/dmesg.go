package engine

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// OOMEvent represents a parsed kernel OOM kill event.
type OOMEvent struct {
	Process   string
	PID       int
	TotalVMKB int64
	AnonRSSKB int64
	Container string
}

const (
	// oomContainerIDLen is the max number of hex chars to keep from a docker container ID.
	oomContainerIDLen = 12
	// oomKillMatchLen is the expected number of submatch groups in reOOMKill (full match + 4 captures).
	oomKillMatchLen = 5
	// kbPerMB is the number of kilobytes in a megabyte.
	kbPerMB = 1024
)

var (
	reOOMKill   = regexp.MustCompile(`Killed process (\d+) \(([^)]+)\) total-vm:(\d+)kB, anon-rss:(\d+)kB`)
	reOOMCpuset = regexp.MustCompile(`cpuset=docker-([a-f0-9]+)\.scope`)
)

// ParseDmesgOOM extracts OOM kill events from dmesg output.
func ParseDmesgOOM(output string) []OOMEvent {
	var events []OOMEvent
	lines := strings.Split(output, "\n")
	var lastContainer string

	for _, line := range lines {
		if m := reOOMCpuset.FindStringSubmatch(line); len(m) > 1 {
			id := m[1]
			if len(id) > oomContainerIDLen {
				id = id[:oomContainerIDLen]
			}
			lastContainer = id
		}
		if m := reOOMKill.FindStringSubmatch(line); len(m) >= oomKillMatchLen {
			pid, _ := strconv.Atoi(m[1])
			totalVM, _ := strconv.ParseInt(m[3], 10, 64)
			anonRSS, _ := strconv.ParseInt(m[4], 10, 64)
			events = append(events, OOMEvent{
				Process:   m[2],
				PID:       pid,
				TotalVMKB: totalVM,
				AnonRSSKB: anonRSS,
				Container: lastContainer,
			})
			lastContainer = ""
		}
	}
	return events
}

// FormatOOMReport formats OOM events for display.
func FormatOOMReport(events []OOMEvent) string {
	if len(events) == 0 {
		return "No OOM events found in kernel log."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "OOM Events (%d found):\n\n", len(events))
	for i, e := range events {
		container := e.Container
		if container == "" {
			container = "host"
		}
		fmt.Fprintf(&b, "%d. %s (PID %d) — VM: %dMB, RSS: %dMB [%s]\n",
			i+1, e.Process, e.PID, e.TotalVMKB/kbPerMB, e.AnonRSSKB/kbPerMB, container)
	}
	return b.String()
}
