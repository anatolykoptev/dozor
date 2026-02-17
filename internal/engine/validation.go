package engine

import (
	"fmt"
	"regexp"
	"strings"
)

var blockedPatterns = []*regexp.Regexp{
	// Destructive file operations
	regexp.MustCompile(`(?i)rm\s+(-r|-f|-rf|--recursive|--force)`),
	regexp.MustCompile(`(?i)mkfs`),
	regexp.MustCompile(`(?i)dd\s+if=`),

	// Permissions (only block recursive chmod on root and all chown)
	regexp.MustCompile(`(?i)chmod\s+(-R\s+)?[0-7]{3,4}\s+/`),
	regexp.MustCompile(`(?i)chown\s+(-R\s+)?`),

	// Fork bomb
	regexp.MustCompile(`(?i):\(\)\s*\{`),

	// Dangerous chained commands
	regexp.MustCompile(`(?i);\s*(rm|dd|mkfs|chmod|chown|mv|cp\s+-r|tar|zip|curl|wget|python|perl|ruby|node|bash|sh)`),
	regexp.MustCompile(`(?i)\|\s*(rm|dd|mkfs|chmod|chown|bash|sh|zsh|python|perl|ruby|node|xargs)`),
	regexp.MustCompile(`(?i)&&\s*(rm|dd|mkfs|chmod|chown|mv|cp\s+-r)`),

	// Shell eval/exec/source
	regexp.MustCompile(`(?i)\beval\s`),
	regexp.MustCompile(`(?i)^exec\s`),
	regexp.MustCompile(`(?i)\bsource\s`),
	regexp.MustCompile(`(?i)\.\s+/`),

	// Write redirects to home
	regexp.MustCompile(`>\s*~/`),
	regexp.MustCompile(`>>\s*~/`),

	// Remote code execution
	regexp.MustCompile(`(?i)curl.*\|\s*(bash|sh|zsh|python|perl)`),
	regexp.MustCompile(`(?i)wget.*\|\s*(bash|sh|zsh|python|perl)`),
	regexp.MustCompile(`(?i)curl.*-o\s`),
	regexp.MustCompile(`(?i)wget.*-O\s`),

	// Path traversal
	regexp.MustCompile(`\.\.`),

	// Sensitive files
	regexp.MustCompile(`/etc/shadow`),
	regexp.MustCompile(`/etc/passwd`),
	regexp.MustCompile(`\.ssh/`),
	regexp.MustCompile(`\.gnupg/`),
	regexp.MustCompile(`\.aws/`),
	regexp.MustCompile(`\.kube/config`),

	// Dangerous exec patterns
	regexp.MustCompile(`(?i)-exec\s`),
	regexp.MustCompile(`(?i)-delete`),

	// Network tools (potential reverse shells)
	regexp.MustCompile(`(?i)\bnc\s`),
	regexp.MustCompile(`(?i)\bncat\s`),
	regexp.MustCompile(`(?i)\bsocat\s`),

	// Cron modification
	regexp.MustCompile(`(?i)crontab\s+-[re]`),

	// Process killing
	regexp.MustCompile(`(?i)\bkill\b`),

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
			return false, fmt.Sprintf("blocked pattern: %s", p.String())
		}
	}

	// Check write redirects to system paths, allowing /dev/null
	if redirectAppendSystemRe.MatchString(cmd) {
		return false, "blocked: append redirect to system path"
	}
	if redirectToSystemRe.MatchString(cmd) || strings.HasPrefix(cmd, ">/") {
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
	if len(name) > 63 {
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
	if len(name) > 63 {
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

var deployIDRe = regexp.MustCompile(`^deploy-\d{10,13}$`)

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
