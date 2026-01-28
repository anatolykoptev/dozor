"""
Input validation for Dozor.

Security-critical module - prevents command injection and path traversal.
"""

import re
import shlex
from typing import Optional


# Allowlist of safe commands for server_exec
# Only these command patterns are allowed
# SECURITY: Patterns must be as restrictive as possible
ALLOWED_COMMANDS = [
    # Docker commands (read-only)
    r'^docker\s+(ps|logs|inspect|stats|top|images|info|version)(\s|$)',
    r'^docker\s+compose\s+(ps|logs|config|images|top|version)(\s|$)',
    # System info (read-only, no arguments that could be dangerous)
    r'^(cat|head|tail)\s+/var/log/[a-zA-Z0-9._/-]+$',
    r'^(ls|ll)\s+(-[lah]+\s+)?(/var/log/|~/|/home/)[a-zA-Z0-9._/-]*$',
    r'^(df|free|uptime|uname|hostname|whoami|pwd|date)(\s+-[a-z]+)?$',
    r'^(du)\s+-[sh]+\s+(/var/log/|~/|/tmp/)[a-zA-Z0-9._/-]*$',
    r'^(ps)\s+(aux|ef|-ef)$',
    r'^(netstat|ss)\s+-[tlnup]+$',
    r'^lsof\s+-i\s*$',
    # Systemd (read-only, specific commands only)
    r'^systemctl\s+(status|is-active|is-enabled)\s+[a-zA-Z0-9_-]+$',
    r'^systemctl\s+list-units(\s+--type=[a-z]+)?$',
    r'^journalctl\s+(-u\s+[a-zA-Z0-9_-]+|--since\s+"[^"]+"|--no-pager|-n\s+\d+|\s)+$',
    # Health checks (limited targets)
    r'^ping\s+-c\s+\d+\s+[a-zA-Z0-9.-]+$',
    r'^(dig|nslookup|host)\s+[a-zA-Z0-9.-]+$',
    # File inspection (read-only, STRICT path validation)
    # Only allow specific safe paths, no shell expansion
    r'^cat\s+~/[a-zA-Z0-9_-]+/[a-zA-Z0-9_.-]+\.(yml|yaml|json|conf|env\.example|txt|log)$',
    r'^cat\s+/var/log/[a-zA-Z0-9._/-]+$',
    # grep - RESTRICTED: only search in safe paths, no -r to prevent traversal
    r'^grep\s+(-[ivnc]+\s+)?["\'][^"\']+["\']\s+(/var/log/|~/)[a-zA-Z0-9._/-]+$',
    # find - RESTRICTED: only in home, only -name, no -exec
    r'^find\s+~/[a-zA-Z0-9_/-]*\s+-name\s+["\'][a-zA-Z0-9.*_-]+["\'](\s+-type\s+[fd])?$',
]

_ALLOWED_PATTERNS = [re.compile(p) for p in ALLOWED_COMMANDS]


# Dangerous patterns that are ALWAYS blocked
# SECURITY: These patterns are checked BEFORE allowlist
BLOCKED_PATTERNS = [
    # Destructive commands
    r'rm\s+(-r|-f|-rf|--recursive|--force)',
    r'>\s*/dev/',
    r'mkfs',
    r'dd\s+if=',
    r'chmod\s+(-R\s+)?[0-7]{3,4}\s+/',
    r'chown\s+(-R\s+)?',
    r':\(\)\s*\{',  # Fork bomb
    # Command injection vectors
    r'\$\(',  # Command substitution $(...)
    r'\$\{',  # Variable expansion ${...}
    r'`',     # Backtick execution
    r'\$\w+', # Variable reference $VAR (block all variable expansion)
    # Command chaining with dangerous commands
    r';\s*(rm|dd|mkfs|chmod|chown|mv|cp\s+-r|tar|zip|curl|wget|python|perl|ruby|node|bash|sh)',
    r'\|\s*(rm|dd|mkfs|chmod|chown|bash|sh|zsh|python|perl|ruby|node|xargs)',
    r'&&\s*(rm|dd|mkfs|chmod|chown|mv|cp\s+-r)',
    # Shell execution
    r'eval\s',
    r'exec\s',
    r'source\s',
    r'\.\s+/',  # Dot sourcing
    # File writing to sensitive locations
    r'>\s*/',     # Redirect to absolute path
    r'>>\s*/',    # Append to absolute path
    r'>\s*~/',    # Redirect to home
    r'>>\s*~/',   # Append to home
    # Dangerous downloads
    r'curl.*\|\s*(bash|sh|zsh|python|perl)',
    r'wget.*\|\s*(bash|sh|zsh|python|perl)',
    r'curl.*-o\s',  # Download to file
    r'wget.*-O\s',  # Download to file
    # Path traversal
    r'\.\.',      # Parent directory reference
    # Sensitive paths (read access)
    r'/etc/shadow',
    r'/etc/passwd',
    r'\.ssh/',
    r'\.gnupg/',
    r'\.aws/',
    r'\.kube/config',
    # Recursive operations that could be dangerous
    r'grep\s+-r',   # Recursive grep (could read entire filesystem)
    r'find\s+/',    # Find from root (should only allow ~/)
    r'-exec\s',     # Find with exec
    r'-delete',     # Find with delete
    # Network exfiltration
    r'nc\s',        # Netcat
    r'ncat\s',      # Nmap netcat
    r'socat\s',     # Socat
]

_BLOCKED_PATTERNS = [re.compile(p, re.IGNORECASE) for p in BLOCKED_PATTERNS]


def is_command_allowed(command: str) -> tuple[bool, str]:
    """
    Check if a command is allowed for execution.

    Returns:
        (allowed, reason) tuple
    """
    command = command.strip()

    # Check blocked patterns first (always blocked)
    for pattern in _BLOCKED_PATTERNS:
        if pattern.search(command):
            return False, f"Command contains blocked pattern"

    # Check if command matches any allowed pattern
    for pattern in _ALLOWED_PATTERNS:
        if pattern.match(command):
            return True, "Command matches allowed pattern"

    return False, "Command not in allowlist. Only read-only diagnostic commands are permitted."


def validate_service_name(name: str) -> tuple[bool, str]:
    """
    Validate a Docker service name.

    Valid names: alphanumeric, hyphens, underscores (1-63 chars)
    """
    if not name:
        return False, "Service name cannot be empty"

    if len(name) > 63:
        return False, "Service name too long (max 63 characters)"

    if not re.match(r'^[a-zA-Z][a-zA-Z0-9_-]*$', name):
        return False, "Invalid service name. Must start with letter, contain only letters, numbers, hyphens, underscores"

    return True, "Valid service name"


def validate_time_duration(duration: str) -> tuple[bool, str]:
    """
    Validate a time duration string (e.g., "1h", "30m", "2d").

    Used for --since parameter in docker logs.
    """
    if not duration:
        return True, "Empty duration is valid (means no filter)"

    # Valid format: number followed by time unit
    if not re.match(r'^\d+[smhd]$', duration):
        return False, "Invalid duration format. Use: 30s, 5m, 1h, or 1d"

    return True, "Valid duration"


def validate_lines_count(lines: int) -> tuple[bool, str]:
    """Validate log lines count."""
    if lines < 1:
        return False, "Lines count must be positive"

    if lines > 10000:
        return False, "Lines count too large (max 10000)"

    return True, "Valid lines count"


def validate_path(path: str, allow_home: bool = True) -> tuple[bool, str]:
    """
    Validate a filesystem path.

    Prevents path traversal attacks.
    """
    if not path:
        return False, "Path cannot be empty"

    # Check for path traversal
    if '..' in path:
        return False, "Path traversal (..) not allowed"

    # Must start with ~ or /
    if not (path.startswith('~') or path.startswith('/')):
        return False, "Path must be absolute (start with / or ~)"

    # If starts with ~, verify it's followed by / or end
    if path.startswith('~') and len(path) > 1 and path[1] != '/':
        return False, "Invalid home path format"

    # Block sensitive paths
    sensitive_paths = ['/etc/passwd', '/etc/shadow', '/root/.ssh', '/etc/ssh']
    for sensitive in sensitive_paths:
        if sensitive in path:
            return False, f"Access to {sensitive} is blocked"

    return True, "Valid path"


def sanitize_for_shell(value: str) -> str:
    """
    Sanitize a value for safe shell interpolation.

    Uses shlex.quote for maximum safety.
    """
    return shlex.quote(value)


def validate_host(host: str) -> tuple[bool, str]:
    """Validate hostname or IP address."""
    if not host:
        return False, "Host cannot be empty"

    # IPv4
    ipv4_pattern = r'^(\d{1,3}\.){3}\d{1,3}$'
    if re.match(ipv4_pattern, host):
        parts = host.split('.')
        if all(0 <= int(p) <= 255 for p in parts):
            return True, "Valid IPv4 address"
        return False, "Invalid IPv4 address"

    # Hostname (simplified validation)
    hostname_pattern = r'^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*$'
    if re.match(hostname_pattern, host):
        return True, "Valid hostname"

    return False, "Invalid host format"


def validate_port(port: int) -> tuple[bool, str]:
    """Validate port number."""
    if not isinstance(port, int):
        return False, "Port must be an integer"

    if port < 1 or port > 65535:
        return False, "Port must be between 1 and 65535"

    return True, "Valid port"
