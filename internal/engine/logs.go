package engine

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// LogCollector gathers and parses container logs.
type LogCollector struct {
	transport *Transport
}

type timestampPattern struct {
	re     *regexp.Regexp
	layout string
}

var timestampPatterns = []timestampPattern{
	{regexp.MustCompile(`^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})`), "2006-01-02T15:04:05"},
	{regexp.MustCompile(`^(\d{4}-\d{2}-\d{2}\s+\d{2}:\d{2}:\d{2})`), "2006-01-02 15:04:05"},
	{regexp.MustCompile(`^([A-Z][a-z]{2}\s+\d{1,2}\s+\d{2}:\d{2}:\d{2})`), "Jan 2 15:04:05"},
}

type levelPattern struct {
	re    *regexp.Regexp
	level string
}

var levelPatterns = []levelPattern{
	{regexp.MustCompile(`\b(ERROR|FATAL|CRITICAL)\b`), "ERROR"},
	{regexp.MustCompile(`\b(WARN|WARNING)\b`), "WARNING"},
	{regexp.MustCompile(`\b(INFO)\b`), "INFO"},
	{regexp.MustCompile(`\b(DEBUG|TRACE)\b`), "DEBUG"},
	{regexp.MustCompile(`\bERROR:`), "ERROR"},
	{regexp.MustCompile(`\bWARNING:`), "WARNING"},
	{regexp.MustCompile(`\bLOG:`), "INFO"},
	{regexp.MustCompile(`"level":\s*"error"`), "ERROR"},
	{regexp.MustCompile(`"level":\s*"warn"`), "WARNING"},
	{regexp.MustCompile(`"level":\s*"info"`), "INFO"},
	{regexp.MustCompile(`Permission denied`), "ERROR"},
	{regexp.MustCompile(`cannot create`), "ERROR"},
	{regexp.MustCompile(`No such file or directory`), "ERROR"},
	{regexp.MustCompile(`command not found`), "ERROR"},
	{regexp.MustCompile(`Segmentation fault`), "ERROR"},
	{regexp.MustCompile(`Killed`), "ERROR"},
	{regexp.MustCompile(`OOM`), "ERROR"},
	{regexp.MustCompile(`out of memory`), "ERROR"},
}

// logsSinceWindow limits log collection to recent entries only,
// preventing old log lines from being reported as current issues.
const logsSinceWindow = "1h"

// GetLogs fetches logs for a service.
func (c *LogCollector) GetLogs(ctx context.Context, service string, lines int, errorsOnly bool) []LogEntry {
	tailN := lines
	if tailN <= 0 {
		tailN = 100
	}

	cmd := fmt.Sprintf("logs --tail %d --since %s --timestamps %s 2>&1", tailN, logsSinceWindow, service)
	res := c.transport.DockerComposeCommand(ctx, cmd)
	if !res.Success {
		return nil
	}

	entries := parseLogLines(res.Stdout, service)

	if errorsOnly {
		filtered := make([]LogEntry, 0)
		for _, e := range entries {
			if e.IsErrorLevel() {
				filtered = append(filtered, e)
			}
		}
		return filtered
	}

	// Filter bot scanner noise
	entries = filterBotScanner(entries)

	return entries
}

// GetErrorLogs fetches only error logs.
func (c *LogCollector) GetErrorLogs(ctx context.Context, service string, lines int) []LogEntry {
	return c.GetLogs(ctx, service, lines, true)
}

func parseLogLines(output, service string) []LogEntry {
	lines := strings.Split(output, "\n")
	entries := make([]LogEntry, 0, len(lines))

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Strip docker compose prefix "service-name  | "
		msg := line
		if idx := strings.Index(line, " | "); idx > 0 {
			msg = line[idx+3:]
		}

		entry := LogEntry{
			Service: service,
			Raw:     line,
			Message: msg,
			Level:   detectLevel(msg),
		}
		entry.Timestamp = parseTimestamp(msg)
		entries = append(entries, entry)
	}

	return entries
}

func detectLevel(line string) string {
	for _, p := range levelPatterns {
		if p.re.MatchString(line) {
			return p.level
		}
	}
	return "INFO"
}

func parseTimestamp(line string) *time.Time {
	for _, p := range timestampPatterns {
		if m := p.re.FindString(line); m != "" {
			if t, err := time.Parse(p.layout, m); err == nil {
				return &t
			}
		}
	}
	return nil
}

// Bot scanner paths to filter out noise
var botScannerPaths = []*regexp.Regexp{
	regexp.MustCompile(`/SDK/webLanguage`),
	regexp.MustCompile(`/sdk/`),
	regexp.MustCompile(`/wp-admin`),
	regexp.MustCompile(`/wp-login`),
	regexp.MustCompile(`/wp-content`),
	regexp.MustCompile(`/wp-includes`),
	regexp.MustCompile(`/xmlrpc\.php`),
	regexp.MustCompile(`\.php$`),
	regexp.MustCompile(`/phpmyadmin`),
	regexp.MustCompile(`/phpMyAdmin`),
	regexp.MustCompile(`/pma`),
	regexp.MustCompile(`/\.env`),
	regexp.MustCompile(`/\.git`),
	regexp.MustCompile(`/config\.json`),
	regexp.MustCompile(`/\.aws`),
	regexp.MustCompile(`/\.ssh`),
	regexp.MustCompile(`/admin`),
	regexp.MustCompile(`/administrator`),
	regexp.MustCompile(`/manager`),
	regexp.MustCompile(`/console`),
	regexp.MustCompile(`\.bak$`),
	regexp.MustCompile(`\.backup$`),
	regexp.MustCompile(`\.sql$`),
	regexp.MustCompile(`\.tar\.gz$`),
	regexp.MustCompile(`\.zip$`),
	regexp.MustCompile(`/api/v\d+/swagger`),
	regexp.MustCompile(`/swagger`),
	regexp.MustCompile(`/actuator`),
	regexp.MustCompile(`/metrics`),
	regexp.MustCompile(`/debug`),
	regexp.MustCompile(`/cgi-bin`),
	regexp.MustCompile(`/shell`),
	regexp.MustCompile(`/cmd`),
	regexp.MustCompile(`/eval`),
	regexp.MustCompile(`/exec`),
}

func filterBotScanner(entries []LogEntry) []LogEntry {
	out := make([]LogEntry, 0, len(entries))
	for _, e := range entries {
		isBot := false
		for _, p := range botScannerPaths {
			if p.MatchString(e.Message) {
				isBot = true
				break
			}
		}
		if !isBot {
			out = append(out, e)
		}
	}
	return out
}
