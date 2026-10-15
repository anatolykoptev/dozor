package engine

import (
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	// maxNameLen is the maximum length for service, binary, and GitHub names.
	maxNameLen = 63
)

var blockedPatterns = []*regexp.Regexp{
	// Destructive file operations
	regexp.MustCompile(`(?i)rm\s+(-r|-f|-rf|--recursive|--force)`),
	regexp.MustCompile(`(?i)mkfs`),
	regexp.MustCompile(`(?i)dd\s+if=`),

	// Permissions — only block recursive chmod on system root paths
	regexp.MustCompile(`(?i)chmod\s+(-R\s+)?[0-7]{3,4}\s+/`),
	// chown allowed — agent needs it for file ownership management

	// Fork bomb
	regexp.MustCompile(`(?i):\(\)\s*\{`),

	// Dangerous chained commands
	regexp.MustCompile(`(?i);\s*(rm|dd|mkfs|chmod|mv|cp\s+-r|python|perl|ruby|node|bash|sh)\b`),
	regexp.MustCompile(`(?i)\|\s*(rm|dd|mkfs|chmod|bash|sh|zsh|python|perl|ruby|node|xargs)`),
	regexp.MustCompile(`(?i)&&\s*(rm|dd|mkfs|chmod|mv|cp\s+-r)\b`),

	// Shell eval/source (exec removed — needed for docker exec)
	regexp.MustCompile(`(?i)\beval\s`),
	regexp.MustCompile(`(?i)\bsource\s`),
	regexp.MustCompile(`(?i)\.\s+/`),

	// Remote code execution via pipe — downloading and piping to shell
	regexp.MustCompile(`(?i)curl.*\|\s*(bash|sh|zsh|python|perl)`),
	regexp.MustCompile(`(?i)wget.*\|\s*(bash|sh|zsh|python|perl)`),
	// curl -o and wget -O allowed — agent needs file downloads

	// Sensitive files
	regexp.MustCompile(`/etc/shadow`),
	regexp.MustCompile(`/etc/passwd`),
	regexp.MustCompile(`\.ssh/`),
	regexp.MustCompile(`\.gnupg/`),
	regexp.MustCompile(`\.aws/`),
	regexp.MustCompile(`\.kube/config`),

	// Dangerous find -delete (find -exec allowed for legitimate operations)
	regexp.MustCompile(`(?i)-delete`),

	// Network tools (reverse shells)
	regexp.MustCompile(`(?i)\bnc\s`),
	regexp.MustCompile(`(?i)\bncat\s`),
	regexp.MustCompile(`(?i)\bsocat\s`),

	// Cron modification
	regexp.MustCompile(`(?i)crontab\s+-[re]`),

	// kill allowed — agent needs it for process management.
	// BUT: hard-block kill/pkill against user-driven interactive sessions.
	// These are live user activity, not background debris. A prior incident
	// had the agent propose killing `claude --dangerously-skip-permissions`
	// sessions to "free memory" during its own build.
	regexp.MustCompile(`(?i)\b(kill|pkill|killall)\b[^&|;]*\b(claude|claude-code|windsurf|windsurf-server|code-review-graph|cursor|cursor-server)\b`),

	// Database destruction
	regexp.MustCompile(`(?i)\bDROP\s+DATABASE\b`),
	regexp.MustCompile(`(?i)\bDROP\s+SCHEMA\b`),

	// System reboot/shutdown
	regexp.MustCompile(`(?i)\breboot\b|\bshutdown\b|\bhalt\b|\bpoweroff\b`),

	// Firewall modification
	regexp.MustCompile(`(?i)\biptables\b|\bufw\b|\bnft\b`),

	// User management
	regexp.MustCompile(`(?i)\buseradd\b|\buserdel\b|\busermod\b|\bpasswd\b`),

	// Mount operations
	regexp.MustCompile(`(?i)\bmount\b|\bumount\b`),
}

// redirectToSystemPath matches > /path or >> /path but not 2>/dev/null or >/dev/null.
var redirectToSystemRe = regexp.MustCompile(`[^2]>\s*/|^>\s*/`)
var redirectAppendSystemRe = regexp.MustCompile(`>>\s*/`)
var devNullRe = regexp.MustCompile(`>\s*/dev/null`)

// IsCommandAllowed checks if a command passes the blocklist.
// Returns (allowed, reason).
func IsCommandAllowed(command string) (bool, string) {
	cmd := strings.TrimSpace(command)
	if cmd == "" {
		return false, "empty command"
	}

	// Check blocklist
	for _, p := range blockedPatterns {
		if p.MatchString(cmd) {
			return false, "blocked pattern: " + p.String()
		}
	}

	// Check write redirects to system paths, allowing /dev/null
	if redirectAppendSystemRe.MatchString(cmd) {
		// >> /anything is always blocked
		return false, "blocked: append redirect to system path"
	}
	if redirectToSystemRe.MatchString(cmd) || strings.HasPrefix(cmd, ">/") {
		// Has redirect to system path — check if it's only /dev/null
		// Remove all 2>/dev/null and >/dev/null occurrences, then re-check
		cleaned := devNullRe.ReplaceAllString(cmd, "")
		if redirectToSystemRe.MatchString(cleaned) || strings.HasPrefix(cleaned, ">/") {
			return false, "blocked: write redirect to system path"
		}
	}

	return true, ""
}

var serviceNameRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_.-]*$`)

// ValidateServiceName checks if a service name is valid.
func ValidateServiceName(name string) (bool, string) {
	if name == "" {
		return false, "service name is required"
	}
	if len(name) > maxNameLen {
		return false, "service name too long (max 63 characters)"
	}
	if !serviceNameRe.MatchString(name) {
		return false, "invalid service name: must start with letter, contain only alphanumeric, underscore, hyphen, or dot"
	}
	return true, ""
}

var binaryNameRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9._-]*$`)

// ValidateBinaryName checks if a binary name is valid.
func ValidateBinaryName(name string) (bool, string) {
	if name == "" {
		return false, "binary name is required"
	}
	if len(name) > maxNameLen {
		return false, "binary name too long (max 63 characters)"
	}
	if !binaryNameRe.MatchString(name) {
		return false, "invalid binary name: must start with letter, contain only alphanumeric, underscore, hyphen, or dot"
	}
	return true, ""
}

var githubNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

// ValidateGitHubName checks if a GitHub owner or repo name is valid.
func ValidateGitHubName(name string) (bool, string) {
	if name == "" {
		return false, "name is required"
	}
	if len(name) > 100 {
		return false, "name too long (max 100 characters)"
	}
	if !githubNameRe.MatchString(name) {
		return false, "invalid GitHub name"
	}
	return true, ""
}

var timeDurationRe = regexp.MustCompile(`^\d+[smhd]$`)

// ValidateTimeDuration checks if a time duration string is valid (e.g., "24h", "30m").
func ValidateTimeDuration(duration string) (bool, string) {
	if !timeDurationRe.MatchString(duration) {
		return false, "invalid time duration: must be number followed by s/m/h/d"
	}
	return true, ""
}

// deploy-<ts> is the legacy nohup path; deploy-manual-<ts> is the SHA-pinned
// queue path — both are minted by this process and both must validate.
var deployIDRe = regexp.MustCompile(`^deploy(-manual)?-\d{10,13}$`)

// ValidateDeployID checks if a deploy ID format is valid.
func ValidateDeployID(id string) (bool, string) {
	if !deployIDRe.MatchString(id) {
		return false, "invalid deploy ID format"
	}
	return true, ""
}

// SanitizeForShell wraps a value in single quotes for shell safety.
func SanitizeForShell(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

// unitNameRe covers systemd units (including template instances such as
// user@1000.service) and docker container names. The first character is
// alphanumeric so a name can never be parsed as a command-line option.
var unitNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9@._:-]{0,254}$`)

// ValidateUnitName checks a caller-supplied systemd unit or container name
// before it is placed into a shell command. It is wider than
// ValidateServiceName (which keeps its narrower 63-char compose-service rule
// for its existing callers) because unit names legitimately contain '@' and ':'.
func ValidateUnitName(name string) (bool, string) {
	if name == "" {
		return false, "unit name is required"
	}
	if !unitNameRe.MatchString(name) {
		return false, "invalid unit name: must start with a letter or digit and contain only letters, digits, '@', '.', '_', ':' or '-' (max 255 characters)"
	}
	return true, ""
}

// ValidateUnitNames validates every name in names and reports the first failure.
func ValidateUnitNames(names []string) (bool, string) {
	for _, n := range names {
		if ok, reason := ValidateUnitName(n); !ok {
			return false, fmt.Sprintf("%q: %s", n, reason)
		}
	}
	return true, ""
}

var absPathRe = regexp.MustCompile(`^/[A-Za-z0-9/._-]+$`)

// ValidateRepoPath checks a caller-supplied filesystem path before it is
// placed into a shell command: absolute, already clean (no "..", no doubled
// or trailing slash), a conservative character set, and never option-shaped.
func ValidateRepoPath(path string) (bool, string) {
	if path == "" {
		return false, "path is required"
	}
	if strings.HasPrefix(path, "-") {
		return false, "invalid path: must not start with '-'"
	}
	if !absPathRe.MatchString(path) {
		return false, "invalid path: must be absolute and contain only letters, digits, '/', '.', '_' or '-'"
	}
	if filepath.Clean(path) != path {
		return false, "invalid path: must be clean (no '..', '.', or repeated '/')"
	}
	return true, ""
}

var (
	scanHostRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?$`)
	scanPathRe = regexp.MustCompile(`^[A-Za-z0-9._~/%+-]*$`)
	portRe     = regexp.MustCompile(`^[0-9]{1,5}$`)
)

// ValidateScanTarget checks a caller-supplied vulnerability-scan target: an
// http(s) URL with a plain host (hostname or IPv4), optional port and path,
// and no userinfo, query or fragment.
func ValidateScanTarget(target string) (bool, string) {
	const reason = "invalid scan target: must be an http(s) URL with a plain host, optional port and path"
	if target == "" {
		return false, "scan target is required"
	}
	u, err := url.Parse(target)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false, reason
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || strings.Contains(target, "#") || strings.Contains(target, "?") {
		return false, reason
	}
	host := u.Hostname()
	if host == "" || !scanHostRe.MatchString(host) {
		return false, reason
	}
	if p := u.Port(); p != "" && !portRe.MatchString(p) {
		return false, reason
	}
	if !scanPathRe.MatchString(u.EscapedPath()) {
		return false, reason
	}
	// Reject anything the parser normalised away (spaces, control characters,
	// shell metacharacters in the authority): re-assemble and compare.
	rebuilt := u.Scheme + "://" + u.Host + u.EscapedPath()
	if rebuilt != target {
		return false, reason
	}
	return true, ""
}

var nucleiSeverities = map[string]bool{
	"info": true, "low": true, "medium": true, "high": true, "critical": true, "unknown": true,
}

// ValidateNucleiSeverities checks a comma-separated nuclei severity list.
func ValidateNucleiSeverities(csv string) (bool, string) {
	if csv == "" {
		return true, ""
	}
	for _, s := range strings.Split(csv, ",") {
		if !nucleiSeverities[s] {
			return false, "invalid severities: each entry must be one of info, low, medium, high, critical, unknown"
		}
	}
	return true, ""
}
