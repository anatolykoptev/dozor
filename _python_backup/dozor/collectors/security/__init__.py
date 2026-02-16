"""
Security collector package - modular security auditing for server infrastructure.

Provides comprehensive security analysis across multiple domains:
- Network: exposed ports, firewall rules, external accessibility
- Container: non-root users, security options, dangerous mounts
- Authentication: API keys, gateway tokens, CORS, rate limiting
- API Hardening: stack traces, security headers, injection protection
- Reconnaissance: bot scanner detection, threat indicators
- Upstream: Moltbot/Clawdbot known vulnerabilities tracking

Based on security research from moltbot/moltbot issues #1792, #1796, #2016, #2245
and krolik-server/docs/AUTH.md security checklist.

Usage:
    from dozor.collectors.security import SecurityCollector

    collector = SecurityCollector(transport)
    issues = collector.check_all()

    # Or run specific modules
    issues = collector.check_network()
    issues = collector.check_containers()
"""

from __future__ import annotations

from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from ...transport import SSHTransport

from ...models import AlertLevel
from .base import SecurityCategory, SecurityIssue

# Import checker modules (lazy loading for performance)
# These imports are deferred until needed


class SecurityCollector:
    """
    Main security collector that orchestrates all security checks.

    Provides a unified interface to run security audits across:
    - Network exposure and firewall
    - Container security
    - Authentication and authorization
    - API hardening
    - Reconnaissance detection
    - Upstream vulnerability tracking

    Example:
        collector = SecurityCollector(transport)

        # Full audit
        issues = collector.check_all()

        # Specific checks
        network_issues = collector.check_network()
        container_issues = collector.check_containers()
    """

    def __init__(self, transport: 'SSHTransport') -> None:
        """
        Initialize the security collector.

        Args:
            transport: SSH transport for executing remote commands
        """
        self.transport = transport
        self._checkers: dict = {}

    # =========================================================================
    # Lazy Loader for Checker Modules
    # =========================================================================

    def _get_network_checker(self):
        """Lazy load network security checker."""
        if 'network' not in self._checkers:
            from .network import NetworkSecurityChecker
            self._checkers['network'] = NetworkSecurityChecker()
        return self._checkers['network']

    def _get_container_checker(self):
        """Lazy load container security checker."""
        if 'container' not in self._checkers:
            from .container import ContainerSecurityChecker
            self._checkers['container'] = ContainerSecurityChecker()
        return self._checkers['container']

    def _get_auth_checker(self):
        """Lazy load authentication checker."""
        if 'auth' not in self._checkers:
            from .authentication import AuthenticationChecker
            self._checkers['auth'] = AuthenticationChecker()
        return self._checkers['auth']

    def _get_api_checker(self):
        """Lazy load API hardening checker."""
        if 'api' not in self._checkers:
            from .api_hardening import ApiHardeningChecker
            self._checkers['api'] = ApiHardeningChecker()
        return self._checkers['api']

    def _get_recon_checker(self):
        """Lazy load reconnaissance checker."""
        if 'recon' not in self._checkers:
            from .reconnaissance import ReconnaissanceChecker
            self._checkers['recon'] = ReconnaissanceChecker()
        return self._checkers['recon']

    def _get_upstream_checker(self):
        """Lazy load upstream vulnerability checker."""
        if 'upstream' not in self._checkers:
            from .upstream import UpstreamVulnerabilityChecker
            self._checkers['upstream'] = UpstreamVulnerabilityChecker()
        return self._checkers['upstream']

    # =========================================================================
    # Main Entry Points
    # =========================================================================

    def check_all(
        self,
        include_recon: bool = True,
        include_upstream: bool = True,
    ) -> list[SecurityIssue]:
        """
        Run all security checks and return consolidated issues.

        Args:
            include_recon: Include bot scanner detection (may be noisy)
            include_upstream: Include upstream vulnerability tracking

        Returns:
            List of SecurityIssue objects sorted by severity
        """
        issues: list[SecurityIssue] = []

        # Network layer
        issues.extend(self.check_network())

        # Container layer
        issues.extend(self.check_containers())

        # Authentication layer
        issues.extend(self.check_authentication())

        # API hardening
        issues.extend(self.check_api_hardening())

        # Reconnaissance (optional)
        if include_recon:
            issues.extend(self.check_reconnaissance())

        # Upstream vulnerabilities (optional)
        if include_upstream:
            issues.extend(self.check_upstream())

        # Sort by severity (CRITICAL first)
        return self._sort_issues(issues)

    def check_krolik_security(self) -> list[SecurityIssue]:
        """
        Run krolik-server specific security checks.

        Focused checks for the krolik stack based on AUTH.md:
        - MemOS API authentication
        - Gateway token configuration
        - Container user isolation
        - API hardening
        """
        issues: list[SecurityIssue] = []

        issues.extend(self.check_containers())
        issues.extend(self.check_authentication())
        issues.extend(self.check_api_hardening())

        # Check gateway specifically
        network = self._get_network_checker()
        issues.extend(network.check_gateway_exposure(self.transport))

        return self._sort_issues(issues)

    # =========================================================================
    # Module-Specific Check Methods
    # =========================================================================

    def check_network(self) -> list[SecurityIssue]:
        """
        Run all network security checks.

        Checks:
        - Exposed ports (services on 0.0.0.0)
        - Firewall status (UFW)
        - Gateway exposure
        """
        checker = self._get_network_checker()
        issues: list[SecurityIssue] = []

        issues.extend(checker.check_exposed_ports(self.transport))
        issues.extend(checker.check_firewall(self.transport))
        issues.extend(checker.check_gateway_exposure(self.transport))

        return issues

    def check_containers(self) -> list[SecurityIssue]:
        """
        Run all container security checks.

        Checks:
        - Non-root users
        - Security options (no-new-privileges, cap_drop)
        - Dangerous host mounts
        - Internal network segmentation
        """
        checker = self._get_container_checker()
        issues: list[SecurityIssue] = []

        issues.extend(checker.check_container_users(self.transport))
        issues.extend(checker.check_container_security_opts(self.transport))
        issues.extend(checker.check_dangerous_mounts(self.transport))
        issues.extend(checker.check_internal_networks(self.transport))

        return issues

    def check_authentication(self) -> list[SecurityIssue]:
        """
        Run all authentication security checks.

        Checks:
        - AUTH_ENABLED and secrets
        - Gateway token auth
        - CORS configuration
        - Rate limiting
        """
        checker = self._get_auth_checker()
        issues: list[SecurityIssue] = []

        issues.extend(checker.check_auth_configuration(self.transport))
        issues.extend(checker.check_gateway_auth(self.transport))
        issues.extend(checker.check_cors_configuration(self.transport))
        issues.extend(checker.check_rate_limiting(self.transport))

        return issues

    def check_api_hardening(self) -> list[SecurityIssue]:
        """
        Run all API hardening checks.

        Checks:
        - Stack trace exposure
        - Security headers
        - Path traversal patterns
        - SQL injection patterns
        """
        checker = self._get_api_checker()
        issues: list[SecurityIssue] = []

        issues.extend(checker.check_stack_traces(self.transport))
        issues.extend(checker.check_security_headers(self.transport))
        issues.extend(checker.check_path_traversal(self.transport))
        issues.extend(checker.check_sql_injection(self.transport))

        return issues

    def check_reconnaissance(self) -> list[SecurityIssue]:
        """
        Run reconnaissance detection checks.

        Checks:
        - Bot scanner activity in logs
        """
        checker = self._get_recon_checker()
        return checker.check_bot_activity(self.transport)

    def check_upstream(self) -> list[SecurityIssue]:
        """
        Check for known upstream vulnerabilities.

        Checks:
        - Moltbot/Clawdbot known security issues
        """
        checker = self._get_upstream_checker()
        return checker.check_upstream_vulnerabilities(self.transport)

    # =========================================================================
    # Utility Methods
    # =========================================================================

    def _sort_issues(self, issues: list[SecurityIssue]) -> list[SecurityIssue]:
        """Sort issues by severity (CRITICAL first)."""
        severity_order = {
            AlertLevel.CRITICAL: 0,
            AlertLevel.ERROR: 1,
            AlertLevel.WARNING: 2,
            AlertLevel.INFO: 3,
        }
        return sorted(issues, key=lambda x: severity_order.get(x.level, 99))

    def get_security_summary(self) -> str:
        """
        Generate human-readable security summary.

        Returns:
            Formatted string with all security findings.
        """
        issues = self.check_all()

        if not issues:
            return "Security check passed. No issues found."

        critical = [i for i in issues if i.level == AlertLevel.CRITICAL]
        warnings = [i for i in issues if i.level == AlertLevel.WARNING]
        info = [i for i in issues if i.level == AlertLevel.INFO]

        lines = [
            f"Security Audit Results: {len(issues)} issue(s) found",
            f"  CRITICAL: {len(critical)}",
            f"  WARNING: {len(warnings)}",
            f"  INFO: {len(info)}",
            "",
        ]

        for issue in issues:
            lines.append(f"[{issue.level.value.upper()}] {issue.title}")
            lines.append(f"  Category: {issue.category}")
            lines.append(f"  {issue.description[:200]}...")
            if issue.evidence:
                lines.append(f"  Evidence: {issue.evidence[:100]}")
            lines.append("")

        return "\n".join(lines)


# Re-export key types for convenience
__all__ = [
    'SecurityCollector',
    'SecurityIssue',
    'SecurityCategory',
]
