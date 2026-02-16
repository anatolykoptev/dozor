"""
Base types for security checks.

Provides common data structures used across all security modules.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from enum import Enum
from typing import TYPE_CHECKING, Optional, Protocol

if TYPE_CHECKING:
    from ...transport import SSHTransport

from ...models import AlertLevel


class SecurityCategory(str, Enum):
    """Categories for security issues."""

    NETWORK = 'network'
    FIREWALL = 'firewall'
    CONTAINER = 'container'
    AUTHENTICATION = 'authentication'
    GATEWAY = 'gateway'
    CONFIGURATION = 'configuration'
    RECONNAISSANCE = 'reconnaissance'
    API_HARDENING = 'api_hardening'
    UPSTREAM = 'upstream'


@dataclass
class SecurityIssue:
    """
    A detected security issue with actionable remediation.

    Attributes:
        level: Severity level (CRITICAL, ERROR, WARNING, INFO)
        category: Security category for grouping
        title: Short, descriptive title
        description: Detailed explanation of the issue
        remediation: Step-by-step fix instructions
        evidence: Raw data supporting the finding
        cwe_id: Common Weakness Enumeration ID (if applicable)
        references: Links to documentation or CVEs
    """

    level: AlertLevel
    category: str
    title: str
    description: str
    remediation: str
    evidence: Optional[str] = None
    cwe_id: Optional[str] = None
    references: list[str] = field(default_factory=list)


class SecurityChecker(Protocol):
    """Protocol for security checker modules."""

    def check(self, transport: 'SSHTransport') -> list[SecurityIssue]:
        """Run security checks and return issues."""
        ...
