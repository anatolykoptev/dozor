"""
Safe logging utilities for Dozor.

Provides logging that:
- Sanitizes sensitive data (passwords, tokens, keys)
- Formats output for both human and AI consumption
- Respects log levels from environment
"""

from __future__ import annotations

import logging
import os
import re
import sys
from typing import Optional


# Patterns for sensitive data that should be redacted
# SECURITY: Add new patterns as new token formats are discovered
SENSITIVE_PATTERNS = [
    # Generic key=value patterns
    (r'(password|passwd|pwd|pass)["\']?\s*[:=]\s*["\']?[^"\'\s,}]+', r'\1=***'),
    (r'(token|api_key|apikey|secret|private_key|secret_key)["\']?\s*[:=]\s*["\']?[^"\'\s,}]+', r'\1=***'),
    (r'(auth|credential|cred)["\']?\s*[:=]\s*["\']?[^"\'\s,}]+', r'\1=***'),
    # HTTP headers
    (r'(Authorization|X-Api-Key|X-Auth-Token|X-Secret):\s*\S+', r'\1: ***'),
    (r'Bearer\s+[A-Za-z0-9\-_]+\.?[A-Za-z0-9\-_]*\.?[A-Za-z0-9\-_]*', 'Bearer ***'),
    (r'Basic\s+[A-Za-z0-9+/=]+', 'Basic ***'),
    # Specific token formats
    (r'(mk_|krlk_|sk-)[A-Za-z0-9_-]+', r'\1***'),           # Internal tokens
    (r'ghp_[A-Za-z0-9]{36,}', 'ghp_***'),                   # GitHub PAT
    (r'gho_[A-Za-z0-9]{36,}', 'gho_***'),                   # GitHub OAuth
    (r'github_pat_[A-Za-z0-9_]{22,}', 'github_pat_***'),    # GitHub fine-grained PAT
    (r'npm_[A-Za-z0-9]{36,}', 'npm_***'),                   # NPM tokens
    (r'pypi-[A-Za-z0-9_-]{32,}', 'pypi-***'),               # PyPI tokens
    # Cloud provider keys
    (r'AKIA[A-Z0-9]{16}', 'AKIA***'),                       # AWS Access Key ID
    (r'[A-Za-z0-9/+=]{40}(?=\s|$|")', '***'),               # AWS Secret (40 chars base64)
    (r'ya29\.[A-Za-z0-9_-]+', 'ya29.***'),                  # Google OAuth
    (r'AIza[A-Za-z0-9_-]{35}', 'AIza***'),                  # Google API Key
    (r'[0-9]+-[A-Za-z0-9_]{32}\.apps\.googleusercontent\.com', '***-***.apps.googleusercontent.com'),
    # Database connection strings
    (r'(postgres|mysql|mongodb)://[^@]+@', r'\1://***@'),   # DB URLs with creds
    (r'redis://:[^@]+@', 'redis://***@'),                   # Redis with password
    # SSH/Private keys (partial match to avoid huge replacements)
    (r'-----BEGIN [A-Z ]+ PRIVATE KEY-----', '-----BEGIN *** PRIVATE KEY-----'),
    (r'ssh-rsa\s+[A-Za-z0-9+/=]+', 'ssh-rsa ***'),
    (r'ssh-ed25519\s+[A-Za-z0-9+/]+', 'ssh-ed25519 ***'),
    # IP addresses with ports (potential internal services)
    (r'\b(?:192\.168|10\.|172\.(?:1[6-9]|2[0-9]|3[01]))\.\d+\.\d+:\d+\b', '***:***'),
]


class SafeFormatter(logging.Formatter):
    """Formatter that redacts sensitive information."""

    def format(self, record: logging.LogRecord) -> str:
        message = super().format(record)
        return sanitize_message(message)


def sanitize_message(message: str) -> str:
    """Remove sensitive data from log message."""
    for pattern, replacement in SENSITIVE_PATTERNS:
        message = re.sub(pattern, replacement, message, flags=re.IGNORECASE)
    return message


def get_safe_logger(
    name: str,
    level: Optional[str] = None,
) -> logging.Logger:
    """
    Get a logger that sanitizes sensitive data.

    Args:
        name: Logger name (usually __name__)
        level: Log level (DEBUG, INFO, WARNING, ERROR). Defaults to env LOG_LEVEL or INFO.

    Returns:
        Configured logger instance.
    """
    logger = logging.getLogger(name)

    # Only configure if not already configured
    if not logger.handlers:
        # Determine log level
        if level is None:
            level = os.environ.get('LOG_LEVEL', 'INFO').upper()

        numeric_level = getattr(logging, level, logging.INFO)
        logger.setLevel(numeric_level)

        # Create handler with safe formatter
        handler = logging.StreamHandler(sys.stderr)
        handler.setLevel(numeric_level)

        # Format: timestamp - level - name - message
        formatter = SafeFormatter(
            '%(asctime)s - %(levelname)s - %(name)s - %(message)s',
            datefmt='%Y-%m-%d %H:%M:%S'
        )
        handler.setFormatter(formatter)

        logger.addHandler(handler)

        # Prevent propagation to root logger
        logger.propagate = False

    return logger


def log_command(logger: logging.Logger, command: str, result: str, success: bool) -> None:
    """
    Log a command execution with sanitized output.

    Args:
        logger: Logger instance
        command: Command that was executed
        result: Command output
        success: Whether command succeeded
    """
    sanitized_cmd = sanitize_message(command)
    sanitized_result = sanitize_message(result[:500])  # Truncate long output

    if success:
        logger.debug(f"Command succeeded: {sanitized_cmd}")
        logger.debug(f"Output: {sanitized_result}")
    else:
        logger.warning(f"Command failed: {sanitized_cmd}")
        logger.warning(f"Error: {sanitized_result}")
