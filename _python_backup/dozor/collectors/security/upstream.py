"""
Upstream vulnerability tracking for Moltbot/Clawdbot.

Tracks known security issues from upstream moltbot/clawdbot repositories.
These are informational - user should monitor upstream for fixes.
"""

from __future__ import annotations

from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from ...transport import SSHTransport

from .base import SecurityIssue, SecurityCategory
from .constants import UPSTREAM_CRITICAL_VULNS, UPSTREAM_HIGH_VULNS
from ...models import AlertLevel


class UpstreamVulnerabilityChecker:
    """
    Checks for known upstream vulnerabilities in Moltbot/Clawdbot.

    Reports informational security issues based on known vulnerabilities
    from upstream GitHub issues. User should monitor upstream for patches.
    """

    CONTAINER_NAMES = ('moltbot', 'clawdbot')

    def check_upstream_vulnerabilities(
        self, transport: 'SSHTransport'
    ) -> list[SecurityIssue]:
        """
        Report known upstream security issues if containers are running.

        Args:
            transport: SSH transport for remote command execution

        Returns:
            List of SecurityIssue for each known vulnerability
        """
        issues: list[SecurityIssue] = []

        running_containers = self._get_running_containers(transport)

        for container in self.CONTAINER_NAMES:
            if container not in running_containers:
                continue

            issues.extend(self._report_critical_vulns(container))
            issues.extend(self._report_high_vulns(container))

        return issues

    def check_moltbot_version(
        self, transport: 'SSHTransport'
    ) -> list[SecurityIssue]:
        """
        Check if running a vulnerable version of Moltbot/Clawdbot.

        Args:
            transport: SSH transport for remote command execution

        Returns:
            List of SecurityIssue if vulnerable version detected
        """
        issues: list[SecurityIssue] = []

        for container in self.CONTAINER_NAMES:
            version_info = self._get_container_version(transport, container)
            if version_info is None:
                continue

            # All current versions have known vulnerabilities
            # until upstream fixes them
            issues.append(
                SecurityIssue(
                    level=AlertLevel.WARNING,
                    category=SecurityCategory.UPSTREAM,
                    title=f'{container} running with known vulnerabilities',
                    description=(
                        f'Container {container} (version: {version_info}) '
                        'has unfixed upstream security issues. '
                        'Monitor upstream repository for security patches.'
                    ),
                    remediation=(
                        '1. Monitor moltbot/moltbot GitHub issues for security fixes\n'
                        '2. Subscribe to security advisories\n'
                        '3. Consider additional network isolation\n'
                        '4. Implement compensating controls (WAF, network policies)'
                    ),
                    evidence=f'Container: {container}, Version: {version_info}',
                    references=[
                        'https://github.com/moltbot/moltbot/issues/1792',
                        'https://github.com/moltbot/moltbot/issues/1796',
                    ],
                )
            )

        return issues

    def _get_running_containers(self, transport: 'SSHTransport') -> set[str]:
        """Get set of running container names."""
        result = transport.execute(
            "docker ps --format '{{.Names}}' 2>/dev/null"
        )

        if not result.success:
            return set()

        return {
            name.strip().lower()
            for name in result.stdout.splitlines()
            if name.strip()
        }

    def _get_container_version(
        self, transport: 'SSHTransport', container: str
    ) -> str | None:
        """Get version info for a specific container."""
        result = transport.execute(
            f"docker inspect --format '{{{{.Config.Image}}}}' {container} 2>/dev/null"
        )

        if not result.success or not result.stdout.strip():
            return None

        image = result.stdout.strip()

        # Try to extract version tag
        if ':' in image:
            return image.split(':')[-1]

        return 'latest'

    def _report_critical_vulns(self, container: str) -> list[SecurityIssue]:
        """Generate SecurityIssue for each CRITICAL upstream vulnerability."""
        issues: list[SecurityIssue] = []

        for vuln in UPSTREAM_CRITICAL_VULNS:
            issues.append(
                SecurityIssue(
                    level=AlertLevel.ERROR,
                    category=SecurityCategory.UPSTREAM,
                    title=f"[{vuln['id']}] {vuln['title']}",
                    description=(
                        f"Known CRITICAL upstream vulnerability in {container}. "
                        f"Location: {vuln['location']}. "
                        f"Status: {'Fixed in ' + vuln['fixed_in'] if vuln['fixed_in'] else 'UNFIXED'}."
                    ),
                    remediation=(
                        '1. Monitor upstream for patches\n'
                        '2. Implement compensating controls\n'
                        '3. Restrict network access to affected components\n'
                        '4. Enable additional logging for affected functionality'
                    ),
                    evidence=f"Container: {container}, Vulnerability: {vuln['id']}",
                    cwe_id=vuln.get('cwe'),
                    references=[
                        'https://github.com/moltbot/moltbot/issues/1792',
                        'https://github.com/moltbot/moltbot/issues/1796',
                    ],
                )
            )

        return issues

    def _report_high_vulns(self, container: str) -> list[SecurityIssue]:
        """Generate SecurityIssue for each HIGH upstream vulnerability."""
        issues: list[SecurityIssue] = []

        for vuln in UPSTREAM_HIGH_VULNS:
            issues.append(
                SecurityIssue(
                    level=AlertLevel.WARNING,
                    category=SecurityCategory.UPSTREAM,
                    title=f"[{vuln['id']}] {vuln['title']}",
                    description=(
                        f"Known HIGH upstream vulnerability in {container}. "
                        f"Location: {vuln['location']}. "
                        f"Status: {'Fixed in ' + vuln['fixed_in'] if vuln['fixed_in'] else 'UNFIXED'}."
                    ),
                    remediation=(
                        '1. Monitor upstream for patches\n'
                        '2. Review affected component usage in your deployment\n'
                        '3. Consider disabling affected functionality if not needed'
                    ),
                    evidence=f"Container: {container}, Vulnerability: {vuln['id']}",
                    cwe_id=vuln.get('cwe'),
                    references=[
                        'https://github.com/moltbot/moltbot/issues/1792',
                        'https://github.com/moltbot/moltbot/issues/1796',
                    ],
                )
            )

        return issues
