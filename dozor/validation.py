"""
Input validation for Dozor.

Security-critical module - prevents command injection and path traversal.
"""

import re
import shlex
from typing import Optional


# Allowlist of safe commands for server_exec
# Only these command patterns are allowed
ALLOWED_COMMANDS = [
    # Docker commands (read-only)
    r'^docker\s+(ps|logs|inspect|stats|top|images|info|version)',
    r'^docker\s+compose\s+(ps|logs|config|images|top|version)',
    # System info (read-only)
    r'^(cat|head|tail|less|more)\s+/var/log/',
    r'^(ls|ll|dir)\s',
    r'^(df|du|free|top|htop|uptime|uname|hostname|whoami|pwd|date)',
    r'^(ps|pgrep|pidof)\s',
    r'^(netstat|ss|lsof)\s',
    r'^(curl|wget)\s.*(-I|--head)',  # HEAD requests only
    # Systemd (read-only)
    r'^systemctl\s+(status|is-active|is-enabled|list-units)',
    r'^journalctl\s',
    # Health checks
    r'^(ping|traceroute|dig|nslookup|host)\s',
    # File inspection (read-only, limited paths)
    r'^cat\s+~/[a-zA-Z0-9_/-]+\.(yml|yaml|json|conf|env\.example)$',
    r'^grep\s',
    r'^find\s+~/',
]

_ALLOWED_PATTERNS = [re.compile(p) for p in ALLOWED_COMMANDS]


# Dangerous patterns that are ALWAYS blocked
BLOCKED_PATTERNS = [
    r'rm\s+(-r|-f|-rf|--recursive|--force)',
    r'>\s*/dev/',
    r'mkfs',
    r'dd\s+if=',
    r'chmod\s+(-R\s+)?[0-7]{3,4}\s+/',
    r'chown\s+(-R\s+)?',
    r':\(\)\s*\{',  # Fork bomb
    r'\$\(',  # Command substitution
    r'`',  # Backtick execution
    r';\s*(rm|dd|mkfs|chmod|chown)',  # Chained dangerous commands
    r'\|\s*(rm|dd|mkfs|chmod|chown)',  # Piped dangerous commands
    r'&&\s*(rm|dd|mkfs|chmod|chown)',  # And-chained dangerous commands
    r'eval\s',
    r'exec\s',
    r'source\s',
    r'\.\s+/',  # Dot sourcing
    r'>/etc/',  # Writing to /etc
    r'>\s*/root/',  # Writing to /root
    r'curl.*\|\s*(bash|sh|zsh)',  # Curl pipe to shell
    r'wget.*\|\s*(bash|sh|zsh)',  # Wget pipe to shell
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
