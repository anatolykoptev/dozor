package engine

import (
	"fmt"
	"regexp"
	"strings"
)

var allowedCommands = []*regexp.Regexp{
	regexp.MustCompile(`^docker\s+(ps|logs|inspect|stats|top|images|info|version)(\s|$)`),
	regexp.MustCompile(`^docker\s+compose\s+(ps|logs|config|images|top|version)(\s|$)`),
	regexp.MustCompile(`^(cat|head|tail)\s+/var/log/[a-zA-Z0-9._/-]+$`),
	regexp.MustCompile(`^(ls|ll)\s+(-[lah]+\s+)?(/var/log/|~/|/home/)[a-zA-Z0-9._/-]*$`),
	regexp.MustCompile(`^(df|free|uptime|uname|hostname|whoami|pwd|date)(\s+-[a-z]+)?$`),
	regexp.MustCompile(`^echo\s+['"][a-zA-Z0-9_]+['"]$`),
	regexp.MustCompile(`^(du)\s+-[sh]+\s+(/var/log/|~/|/tmp/)[a-zA-Z0-9._/-]*$`),
	regexp.MustCompile(`^(ps)\s+(aux|ef|-ef)$`),
	regexp.MustCompile(`^(netstat|ss)\s+-[tlnup]+$`),
	regexp.MustCompile(`^lsof\s+-i\s*$`),
	regexp.MustCompile(`^systemctl\s+(status|is-active|is-enabled)\s+[a-zA-Z0-9_.-]+$`),
	regexp.MustCompile(`^systemctl\s+list-units(\s+--type=[a-z]+)?$`),
	regexp.MustCompile(`^journalctl\s+(-u\s+[a-zA-Z0-9_.-]+|--since\s+"[^"]+"|--no-pager|-n\s+\d+|\s)+$`),
	regexp.MustCompile(`^ping\s+-c\s+\d+\s+[a-zA-Z0-9.-]+$`),
	regexp.MustCompile(`^(dig|nslookup|host)\s+[a-zA-Z0-9.-]+$`),
	regexp.MustCompile(`^cat\s+~/[a-zA-Z0-9_-]+/[a-zA-Z0-9_.-]+\.(yml|yaml|json|conf|env\.example|txt|log)$`),
	regexp.MustCompile(`^cat\s+/var/log/[a-zA-Z0-9._/-]+$`),
	regexp.MustCompile(`^grep\s+(-[ivnc]+\s+)?["'][^"']+["']\s+(/var/log/|~/)[a-zA-Z0-9._/-]+$`),
	regexp.MustCompile(`^find\s+~/[a-zA-Z0-9_/-]*\s+-name\s+["'][a-zA-Z0-9.*_-]+["'](\s+-type\s+[fd])?$`),
}

var blockedPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)rm\s+(-r|-f|-rf|--recursive|--force)`),
	regexp.MustCompile(`(?i)>\s*/dev/`),
	regexp.MustCompile(`(?i)mkfs`),
	regexp.MustCompile(`(?i)dd\s+if=`),
	regexp.MustCompile(`(?i)chmod\s+(-R\s+)?[0-7]{3,4}\s+/`),
	regexp.MustCompile(`(?i)chown\s+(-R\s+)?`),
	regexp.MustCompile(`(?i):\(\)\s*\{`),
	regexp.MustCompile(`\$\(`),
	regexp.MustCompile(`\$\{`),
	regexp.MustCompile("`"),
	regexp.MustCompile(`\$\w+`),
	regexp.MustCompile(`(?i);\s*(rm|dd|mkfs|chmod|chown|mv|cp\s+-r|tar|zip|curl|wget|python|perl|ruby|node|bash|sh)`),
	regexp.MustCompile(`(?i)\|\s*(rm|dd|mkfs|chmod|chown|bash|sh|zsh|python|perl|ruby|node|xargs)`),
	regexp.MustCompile(`(?i)&&\s*(rm|dd|mkfs|chmod|chown|mv|cp\s+-r)`),
	regexp.MustCompile(`(?i)eval\s`),
	regexp.MustCompile(`(?i)exec\s`),
	regexp.MustCompile(`(?i)source\s`),
	regexp.MustCompile(`(?i)\.\s+/`),
	regexp.MustCompile(`>\s*/`),
	regexp.MustCompile(`>>\s*/`),
	regexp.MustCompile(`>\s*~/`),
	regexp.MustCompile(`>>\s*~/`),
	regexp.MustCompile(`(?i)curl.*\|\s*(bash|sh|zsh|python|perl)`),
	regexp.MustCompile(`(?i)wget.*\|\s*(bash|sh|zsh|python|perl)`),
	regexp.MustCompile(`(?i)curl.*-o\s`),
	regexp.MustCompile(`(?i)wget.*-O\s`),
	regexp.MustCompile(`\.\.`),
	regexp.MustCompile(`/etc/shadow`),
	regexp.MustCompile(`/etc/passwd`),
	regexp.MustCompile(`\.ssh/`),
	regexp.MustCompile(`\.gnupg/`),
	regexp.MustCompile(`\.aws/`),
	regexp.MustCompile(`\.kube/config`),
	regexp.MustCompile(`(?i)grep\s+-r`),
	regexp.MustCompile(`(?i)find\s+/`),
	regexp.MustCompile(`(?i)-exec\s`),
	regexp.MustCompile(`(?i)-delete`),
	regexp.MustCompile(`(?i)nc\s`),
	regexp.MustCompile(`(?i)ncat\s`),
	regexp.MustCompile(`(?i)socat\s`),
}

// IsCommandAllowed checks if a command passes the allowlist/blocklist.
// Returns (allowed, reason).
func IsCommandAllowed(command string) (bool, string) {
	cmd := strings.TrimSpace(command)
	if cmd == "" {
		return false, "empty command"
	}

	// Check blocklist first
	for _, p := range blockedPatterns {
		if p.MatchString(cmd) {
			return false, fmt.Sprintf("blocked pattern: %s", p.String())
		}
	}

	// Check allowlist
	for _, p := range allowedCommands {
		if p.MatchString(cmd) {
			return true, ""
		}
	}

	return false, "command not in allowlist"
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
