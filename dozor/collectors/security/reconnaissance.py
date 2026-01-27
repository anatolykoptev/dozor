"""
Reconnaissance detection module.

Detects automated vulnerability scanning and tracks external IPs accessing the server.
"""

from __future__ import annotations

import re
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from ...transport import SSHTransport

from .base import SecurityCategory, SecurityIssue
from .constants import BOT_SCANNER_INDICATORS
from ...models import AlertLevel


class ReconnaissanceChecker:
    """
    Detects reconnaissance and scanning activity against the server.

    Identifies automated vulnerability scanners and tracks external IPs
    that may be probing for weaknesses.
    """

    def __init__(self, transport: SSHTransport) -> None:
        self.transport = transport

    def check_bot_activity(self) -> list[SecurityIssue]:
        """
        Detect automated vulnerability scanning activity.

        Identifies known bot scanner patterns in logs to assess
        the server's exposure and threat level.

        Returns:
            List of SecurityIssue objects for detected scanning activity.
        """
        issues: list[SecurityIssue] = []

        result = self.transport.execute(
            "docker compose logs --tail 1000 2>/dev/null | "
            "grep -iE '404|403|401' | head -100"
        )

        if not result.success:
            return issues

        bot_hits = 0
        sample_ips: set[str] = set()
        matched_patterns: set[str] = set()

        for line in result.stdout.split('\n'):
            for pattern in BOT_SCANNER_INDICATORS:
                if re.search(pattern, line, re.IGNORECASE):
                    bot_hits += 1
                    matched_patterns.add(pattern)

                    ip_match = re.search(
                        r'(?:ip["\']?\s*[:=]\s*["\']?|from\s+)(\d+\.\d+\.\d+\.\d+)',
                        line,
                        re.IGNORECASE
                    )
                    if ip_match and ip_match.group(1) not in ('127.0.0.1', '::1'):
                        sample_ips.add(ip_match.group(1))
                    break

        if bot_hits > 20:
            level = AlertLevel.WARNING if bot_hits > 100 else AlertLevel.INFO

            issues.append(SecurityIssue(
                level=level,
                category=SecurityCategory.RECONNAISSANCE,
                title=f'Automated scanning detected ({bot_hits} hits)',
                description=(
                    'Vulnerability scanners are actively probing the server. '
                    'This is normal for internet-exposed services but indicates '
                    'the server is discoverable and being targeted.\n\n'
                    f'Matched patterns: {", ".join(list(matched_patterns)[:5])}'
                ),
                remediation=(
                    '1. Ensure unnecessary ports are blocked\n'
                    '2. Consider fail2ban to rate-limit offenders:\n'
                    '   sudo apt install fail2ban\n'
                    '3. Review exposed services for known vulnerabilities\n'
                    '4. Monitor for successful exploitation attempts'
                ),
                evidence=f'Sample IPs: {", ".join(list(sample_ips)[:5])}' if sample_ips else None,
            ))

        return issues

    def get_external_ips(self) -> list[SecurityIssue]:
        """
        Get list of external IPs accessing the server.

        Analyzes logs to identify unique external IP addresses that have
        made requests to the server. Useful for threat intelligence.

        Returns:
            List of SecurityIssue objects with external IP information.
        """
        issues: list[SecurityIssue] = []

        result = self.transport.execute(
            "docker compose logs --tail 5000 2>/dev/null | "
            "grep -oE '([0-9]{1,3}\\.){3}[0-9]{1,3}' | "
            "sort | uniq -c | sort -rn | head -20"
        )

        if not result.success or not result.stdout.strip():
            return issues

        external_ips: list[tuple[int, str]] = []
        internal_prefixes = ('127.', '10.', '172.16.', '172.17.', '172.18.',
                            '172.19.', '172.2', '172.3', '192.168.', '::1')

        for line in result.stdout.strip().split('\n'):
            parts = line.strip().split()
            if len(parts) >= 2:
                try:
                    count = int(parts[0])
                    ip = parts[1]

                    if not ip.startswith(internal_prefixes):
                        external_ips.append((count, ip))
                except ValueError:
                    continue

        if external_ips:
            top_ips = external_ips[:10]
            ip_list = '\n'.join(f'  {ip}: {count} requests' for count, ip in top_ips)

            issues.append(SecurityIssue(
                level=AlertLevel.INFO,
                category=SecurityCategory.RECONNAISSANCE,
                title=f'External IP activity detected ({len(external_ips)} unique IPs)',
                description=(
                    'External IP addresses have been observed accessing the server. '
                    'Review for suspicious patterns or known malicious sources.\n\n'
                    f'Top IPs by request count:\n{ip_list}'
                ),
                remediation=(
                    '1. Cross-reference IPs with threat intelligence feeds\n'
                    '2. Block persistent offenders via firewall:\n'
                    '   sudo ufw deny from <IP>\n'
                    '3. Consider geographic blocking if traffic is unexpected\n'
                    '4. Set up automated IP reputation checking'
                ),
                evidence=f'Total unique external IPs: {len(external_ips)}',
            ))

        return issues

    def check(self) -> list[SecurityIssue]:
        """
        Run all reconnaissance checks.

        Returns:
            Combined list of SecurityIssue objects from all checks.
        """
        issues: list[SecurityIssue] = []
        issues.extend(self.check_bot_activity())
        issues.extend(self.get_external_ips())
        return issues
