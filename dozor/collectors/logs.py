"""
Container log collector.

Security: Validates all inputs before shell execution.
"""

import re
from datetime import datetime
from typing import Optional

from ..transport import SSHTransport
from ..models import LogEntry
from ..analyzers.log_analyzer import is_bot_scanner_request
from ..validation import validate_service_name, validate_time_duration, sanitize_for_shell


class LogCollector:
    """Collects and parses container logs."""

    # Timestamp patterns for different log formats
    TIMESTAMP_PATTERNS = [
        # ISO format: 2024-01-15T10:30:45.123Z
        (r'^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:?\d{2})?)', '%Y-%m-%dT%H:%M:%S'),
        # Docker compose format: 2024-01-15 10:30:45
        (r'^(\d{4}-\d{2}-\d{2}\s+\d{2}:\d{2}:\d{2})', '%Y-%m-%d %H:%M:%S'),
        # Syslog-like: Jan 15 10:30:45
        (r'^([A-Z][a-z]{2}\s+\d{1,2}\s+\d{2}:\d{2}:\d{2})', '%b %d %H:%M:%S'),
    ]

    # Log level patterns
    LEVEL_PATTERNS = [
        (r'\b(ERROR|FATAL|CRITICAL)\b', 'ERROR'),
        (r'\b(WARN|WARNING)\b', 'WARNING'),
        (r'\b(INFO)\b', 'INFO'),
        (r'\b(DEBUG|TRACE)\b', 'DEBUG'),
        # Postgres specific
        (r'\bERROR:', 'ERROR'),
        (r'\bWARNING:', 'WARNING'),
        (r'\bLOG:', 'INFO'),
        # Node.js/n8n specific
        (r'"level":\s*"error"', 'ERROR'),
        (r'"level":\s*"warn"', 'WARNING'),
        (r'"level":\s*"info"', 'INFO'),
        # Shell/system errors
        (r'Permission denied', 'ERROR'),
        (r'cannot create', 'ERROR'),
        (r'No such file or directory', 'ERROR'),
        (r'command not found', 'ERROR'),
        (r'Segmentation fault', 'ERROR'),
        (r'Killed', 'ERROR'),
        (r'OOM', 'ERROR'),
        (r'out of memory', 'ERROR'),
    ]

    def __init__(self, transport: SSHTransport):
        self.transport = transport

    def get_logs(
        self,
        service: str,
        lines: int = 100,
        since: Optional[str] = None,
    ) -> list[LogEntry]:
        """
        Get logs for a service.

        Args:
            service: Service name (validated)
            lines: Number of lines to fetch
            since: Optional time filter (e.g., "1h", "30m") (validated)

        Returns:
            List of parsed LogEntry objects
        """
        # Validate service name (prevents command injection)
        valid, reason = validate_service_name(service)
        if not valid:
            raise ValueError(f"Invalid service name: {reason}")

        # Validate and sanitize lines count
        if lines < 1 or lines > 10000:
            lines = 100

        cmd_parts = [f'logs --tail {lines} --no-color']

        # Validate since parameter (prevents command injection)
        if since:
            valid, reason = validate_time_duration(since)
            if not valid:
                raise ValueError(f"Invalid time duration: {reason}")
            cmd_parts.append(f'--since {sanitize_for_shell(since)}')

        cmd_parts.append(sanitize_for_shell(service))
        cmd = ' '.join(cmd_parts)

        result = self.transport.docker_compose_command(cmd)

        if not result.success:
            return []

        return self._parse_logs(result.stdout, service)

    def _parse_logs(self, raw_logs: str, service: str) -> list[LogEntry]:
        """Parse raw log output into LogEntry objects."""
        entries = []

        for line in raw_logs.split('\n'):
            if not line.strip():
                continue

            # Skip docker compose prefix if present (e.g., "service-1  | ")
            clean_line = self._strip_compose_prefix(line)

            timestamp = self._extract_timestamp(clean_line)
            level = self._detect_level(clean_line)
            message = self._extract_message(clean_line)

            entries.append(LogEntry(
                timestamp=timestamp,
                level=level,
                message=message,
                service=service,
                raw=line,
            ))

        return entries

    def _strip_compose_prefix(self, line: str) -> str:
        """Remove docker compose service prefix from log line."""
        # Pattern: "service-name-1  | actual log content"
        match = re.match(r'^[\w-]+-\d+\s*\|\s*(.*)$', line)
        if match:
            return match.group(1)
        return line

    def _extract_timestamp(self, line: str) -> Optional[datetime]:
        """Extract timestamp from log line."""
        for pattern, date_format in self.TIMESTAMP_PATTERNS:
            match = re.search(pattern, line, re.IGNORECASE)
            if match:
                ts_str = match.group(1)
                # Clean up timezone for parsing
                ts_str = re.sub(r'Z$', '', ts_str)
                ts_str = re.sub(r'[+-]\d{2}:?\d{2}$', '', ts_str)
                ts_str = re.sub(r'\.\d+$', '', ts_str)

                try:
                    return datetime.strptime(ts_str, date_format)
                except ValueError:
                    continue
        return None

    def _detect_level(self, line: str) -> str:
        """Detect log level from line content."""
        for pattern, level in self.LEVEL_PATTERNS:
            if re.search(pattern, line, re.IGNORECASE):
                return level

        # Default to INFO if no level detected
        return 'INFO'

    def _extract_message(self, line: str) -> str:
        """Extract the message portion of the log line."""
        # Remove timestamp prefix if present
        for pattern, _ in self.TIMESTAMP_PATTERNS:
            line = re.sub(pattern, '', line, count=1).strip()

        # Remove common log level prefixes
        line = re.sub(r'^[\[\(]?(ERROR|WARN|WARNING|INFO|DEBUG|LOG|FATAL)[\]\)]?:?\s*', '', line, flags=re.IGNORECASE)

        return line.strip() or line

    def get_error_logs(
        self,
        service: str,
        lines: int = 100,
        since: Optional[str] = None,
    ) -> list[LogEntry]:
        """Get only error-level logs for a service (excludes bot scanner noise)."""
        all_logs = self.get_logs(service, lines, since)
        return [
            log for log in all_logs
            if log.level in ('ERROR', 'CRITICAL', 'FATAL')
            and not is_bot_scanner_request(log.raw)
        ]

    def search_logs(
        self,
        service: str,
        pattern: str,
        lines: int = 100,
    ) -> list[LogEntry]:
        """Search logs for a specific pattern."""
        all_logs = self.get_logs(service, lines)
        compiled = re.compile(pattern, re.IGNORECASE)
        return [log for log in all_logs if compiled.search(log.raw)]
