package engine

import (
	"fmt"
	"regexp"
	"strings"
)

// Process tags. These are appended to `ps aux` lines so the consuming LLM
// can distinguish the system's own activity from real application load.
//
// The motivation: dozor's own telemetry gathering (`docker compose logs`,
// `docker stats`), the background Go build used to iterate on dozor itself,
// and live user-driven sessions (`claude`, `windsurf`) frequently dominate
// the CPU top during development. A prior incident had the agent conclude
// "system under load, must free memory by killing these processes" — the
// processes it wanted to kill were the user's active Claude Code sessions.
const (
	tagAgentSelf   = "[agent-self]"
	tagUserSession = "[user-session]"
	tagBuild       = "[build]"
	// topProcessTagThresholdNum / topProcessTagThresholdDen define the fraction
	// of top-process slots that must be tagged before the "LOAD SOURCE" banner
	// fires. >50% → banner.
	topProcessTagThresholdNum = 1
	topProcessTagThresholdDen = 2
)

// processClassifier matches a process command line against a category. The
// first matching rule wins; rule order encodes priority. Rules are anchored
// loosely because `ps aux` truncates long command lines and prefixes them
// with the full binary path.
type processClassifier struct {
	tag string
	re  *regexp.Regexp
}

var processClassifiers = []processClassifier{
	// User-driven interactive sessions — NEVER kill these. Each entry must
	// be a live command the user is actively running.
	{
		tag: tagUserSession,
		re:  regexp.MustCompile(`(?i)\b(claude|claude-code)(\s|$)`),
	},
	{
		tag: tagUserSession,
		re:  regexp.MustCompile(`(?i)\bwindsurf(-server)?\b`),
	},
	{
		tag: tagUserSession,
		re:  regexp.MustCompile(`(?i)\bcode-review-graph\s+(update|build|serve)`),
	},
	{
		tag: tagUserSession,
		re:  regexp.MustCompile(`(?i)\bcursor(-server)?\b`),
	},
	// The agent's own telemetry-gathering footprint — docker compose logs,
	// docker stats, ps aux invocations from dozor's own overview/triage
	// handlers. Looks like "high activity" but is self-induced.
	{
		// Match all four docker compose CLI shapes:
		//   `docker compose logs` (native v2)
		//   `docker-compose compose logs` (plugin path shown in ps aux)
		//   `docker-compose logs` (legacy standalone)
		//   `docker stats` / `docker ps` (direct docker CLI)
		tag: tagAgentSelf,
		re:  regexp.MustCompile(`(?:docker-compose|docker)\s+(?:compose\s+)?(logs|stats|ps)\b`),
	},
	{
		tag: tagAgentSelf,
		re:  regexp.MustCompile(`\bjournalctl\b.*--follow`),
	},
	// Active builds — short-lived but CPU-heavy. Not an incident even if
	// load average is elevated.
	{
		tag: tagBuild,
		re:  regexp.MustCompile(`/usr/local/go/pkg/tool/.+?/compile\b`),
	},
	{
		tag: tagBuild,
		re:  regexp.MustCompile(`/tmp/go-build\d+/`),
	},
	{
		tag: tagBuild,
		re:  regexp.MustCompile(`\bcargo\s+(build|run|test|check)\b`),
	},
	{
		tag: tagBuild,
		re:  regexp.MustCompile(`\brustc\b`),
	},
	{
		tag: tagBuild,
		re:  regexp.MustCompile(`\bdocker(-compose|\s+compose)?\s+build\b`),
	},
	{
		tag: tagBuild,
		re:  regexp.MustCompile(`\b(npm|pnpm|yarn)\s+(run\s+)?build\b`),
	},
	{
		tag: tagBuild,
		re:  regexp.MustCompile(`\bdu\b.*-[hsd]`),
	},
	{
		tag: tagBuild,
		re:  regexp.MustCompile(`\btar\s+(-c|c)`),
	},
}

// classifyProcess returns a tag for the given `ps aux` line, or "" if the
// line does not match any self/user/build pattern.
func classifyProcess(line string) string {
	for _, c := range processClassifiers {
		if c.re.MatchString(line) {
			return c.tag
		}
	}
	return ""
}

// tagTopProcesses post-processes raw `ps aux` output:
//  1. Appends a tag suffix to each matching line.
//  2. Returns the annotated text plus counts of data rows and tagged rows.
//
// The header line (COMMAND, USER, ...) is copied verbatim with no tag.
func tagTopProcesses(raw string) (string, int, int) {
	if raw == "" {
		return "", 0, 0
	}
	lines := strings.Split(raw, "\n")
	var out strings.Builder
	total := 0
	tagged := 0
	for i, line := range lines {
		if line == "" {
			if i < len(lines)-1 {
				out.WriteByte('\n')
			}
			continue
		}
		// Header line from `ps aux` starts with "USER".
		if strings.HasPrefix(line, "USER") {
			out.WriteString(line)
			out.WriteByte('\n')
			continue
		}
		total++
		if tag := classifyProcess(line); tag != "" {
			tagged++
			fmt.Fprintf(&out, "%s %s\n", line, tag)
		} else {
			out.WriteString(line)
			out.WriteByte('\n')
		}
	}
	return strings.TrimRight(out.String(), "\n"), total, tagged
}

// topProcessLoadBanner returns a banner that tells the consuming LLM that
// the observed "high load" is self/user/build activity rather than an
// incident. Returns empty string when the tagged fraction is below the
// threshold (= there really is foreign load worth investigating).
func topProcessLoadBanner(total, tagged int) string {
	if total == 0 || tagged == 0 {
		return ""
	}
	// >50% tagged → banner.
	if tagged*topProcessTagThresholdDen <= total*topProcessTagThresholdNum {
		return ""
	}
	return fmt.Sprintf(
		"LOAD SOURCE: %d of %d top processes are agent-self, user-session, or build activity — NOT a foreign incident. Do NOT kill tagged processes.\n",
		tagged, total,
	)
}
