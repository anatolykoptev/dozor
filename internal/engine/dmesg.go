package engine

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// OOMEvent represents a parsed kernel OOM kill event.
type OOMEvent struct {
	Timestamp time.Time // zero if dmesg output had no parseable ctime prefix
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
	// dmesgCtimeLayout is the layout produced by `dmesg --ctime`: e.g. "Sat Apr 11 02:30:15 2026".
	dmesgCtimeLayout = "Mon Jan _2 15:04:05 2006"
	// oomFreshThreshold defines what "active right now" means for OOM events.
	oomFreshThreshold = 10 * time.Minute
	// oomRecentThreshold defines what "recent" means.
	oomRecentThreshold = 1 * time.Hour
	// oomHistoricalThreshold separates same-day-historical from stale.
	oomHistoricalThreshold = 24 * time.Hour
)

var (
	reOOMKill   = regexp.MustCompile(`Killed process (\d+) \(([^)]+)\) total-vm:(\d+)kB, anon-rss:(\d+)kB`)
	reOOMCpuset = regexp.MustCompile(`cpuset=docker-([a-f0-9]+)\.scope`)
	// reCtimePrefix matches the `[Sat Apr 11 02:30:15 2026]` prefix from `dmesg --ctime`.
	reCtimePrefix = regexp.MustCompile(`^\[([A-Z][a-z]{2}\s+[A-Z][a-z]{2}\s+[ 0-9]\d\s+\d{2}:\d{2}:\d{2}\s+\d{4})\]`)
)

// parseDmesgCtime extracts the timestamp from a `dmesg --ctime` line prefix.
// Returns the zero time if the line is in legacy boot-relative format `[12345.678]`.
func parseDmesgCtime(line string) time.Time {
	m := reCtimePrefix.FindStringSubmatch(line)
	if len(m) < 2 {
		return time.Time{}
	}
	// Collapse internal whitespace runs to single spaces so the layout matches.
	cleaned := strings.Join(strings.Fields(m[1]), " ")
	t, err := time.ParseInLocation(dmesgCtimeLayout, cleaned, time.Local)
	if err != nil {
		return time.Time{}
	}
	return t
}

// ParseDmesgOOM extracts OOM kill events from dmesg output.
//
// Lines should come from `dmesg --ctime` so timestamps can be attached to each event.
// Legacy boot-relative format `[12345.678]` is also accepted but produces events with
// zero Timestamp — callers should treat such events as historical until verified by
// other means (e.g. checking container uptime).
func ParseDmesgOOM(output string) []OOMEvent {
	var events []OOMEvent
	lines := strings.Split(output, "\n")
	var lastContainer string
	var lastTimestamp time.Time

	for _, line := range lines {
		if ts := parseDmesgCtime(line); !ts.IsZero() {
			lastTimestamp = ts
		}
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
				Timestamp: lastTimestamp,
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

// formatAge renders a duration in a compact, human-readable form: "5s ago", "12m ago", "3h ago", "2d ago".
func formatAge(d time.Duration) string {
	switch {
	case d < 0:
		return "in the future"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < oomHistoricalThreshold:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d/oomHistoricalThreshold))
	}
}

// classifyOOMRecency classifies the most recent OOM event by age and returns
// a structured banner that the consuming agent cannot reasonably misread as
// "active incident" when it is in fact historical.
//
// This is the single most important fix: without it, agents see a list of OOM
// events with no timestamps and assume they happened "right now".
func classifyOOMRecency(events []OOMEvent, now time.Time) string {
	if len(events) == 0 {
		return ""
	}
	var latest time.Time
	for _, e := range events {
		if e.Timestamp.After(latest) {
			latest = e.Timestamp
		}
	}
	if latest.IsZero() {
		return "STATUS: UNKNOWN-AGE — dmesg output has no timestamps (legacy boot-relative format). " +
			"Cannot determine recency. Treat as HISTORICAL until verified by checking container uptime " +
			"and restart count via docker_ps / server_inspect mode=status.\n\n"
	}
	age := now.Sub(latest)
	switch {
	case age < oomFreshThreshold:
		return fmt.Sprintf("STATUS: ACTIVE — most recent OOM was %s. Treat as ongoing incident, "+
			"verify the affected container is currently in a crash loop before acting.\n\n", formatAge(age))
	case age < oomRecentThreshold:
		return fmt.Sprintf("STATUS: RECENT — most recent OOM was %s. Worth investigating, "+
			"but the immediate pressure may have already resolved. Check current memory before acting.\n\n", formatAge(age))
	case age < oomHistoricalThreshold:
		return fmt.Sprintf("STATUS: HISTORICAL (same day) — most recent OOM was %s. NOT a current incident. "+
			"Do not restart anything unless the affected container is currently restarting.\n\n", formatAge(age))
	default:
		return fmt.Sprintf("STATUS: STALE — most recent OOM was %s. NOT a current incident. "+
			"This is background noise from the kernel ring buffer; events this old should not drive any action.\n\n", formatAge(age))
	}
}

// FormatOOMReport formats OOM events for display, including a freshness banner
// and per-event timestamps.
func FormatOOMReport(events []OOMEvent) string {
	return formatOOMReportAt(events, time.Now())
}

// formatOOMReportAt is the testable variant of FormatOOMReport with an injectable clock.
func formatOOMReportAt(events []OOMEvent, now time.Time) string {
	if len(events) == 0 {
		return "No OOM events found in kernel log."
	}
	var b strings.Builder
	b.WriteString(classifyOOMRecency(events, now))
	fmt.Fprintf(&b, "OOM Events (%d found):\n\n", len(events))
	for i, e := range events {
		container := e.Container
		if container == "" {
			container = "host"
		}
		var when string
		if e.Timestamp.IsZero() {
			when = "no timestamp"
		} else {
			when = fmt.Sprintf("%s, %s", e.Timestamp.Format("2006-01-02 15:04:05"), formatAge(now.Sub(e.Timestamp)))
		}
		fmt.Fprintf(&b, "%d. [%s] %s (PID %d) — VM: %dMB, RSS: %dMB [%s]\n",
			i+1, when, e.Process, e.PID, e.TotalVMKB/kbPerMB, e.AnonRSSKB/kbPerMB, container)
	}
	return b.String()
}
