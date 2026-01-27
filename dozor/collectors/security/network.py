"""
Network security checks.

Checks for:
- Services exposed on public interfaces (0.0.0.0)
- UFW firewall status
- Moltbot gateway exposure without authentication
"""

from __future__ import annotations

import json
import re
from typing import TYPE_CHECKING, Optional

if TYPE_CHECKING:
    from ...transport import SSHTransport

from .base import SecurityCategory, SecurityIssue
from .constants import GATEWAY_CONFIG_PATHS, GATEWAY_PORT, INTERNAL_ONLY_PORTS
from ...models import AlertLevel


class NetworkSecurityChecker:
    """
    Network security checker for exposed ports and firewall configuration.

    Performs checks:
    - Services bound to 0.0.0.0 that should be localhost-only
    - UFW firewall status
    - Moltbot gateway exposure without authentication

    Usage:
        checker = NetworkSecurityChecker(transport)
        issues = checker.check_exposed_ports()
        issues.extend(checker.check_firewall())
        issues.extend(checker.check_gateway_exposure())
    """

    def __init__(self, transport: 'SSHTransport') -> None:
        """
        Initialize the network security checker.

        Args:
            transport: SSH transport for executing remote commands
        """
        self._transport = transport
        self._ufw_status: Optional[dict] = None

    def check_exposed_ports(self, transport: 'SSHTransport') -> list[SecurityIssue]:
        """
        Check for services exposed on public interfaces (0.0.0.0).

        Identifies services bound to all interfaces that should be
        localhost-only, considering firewall rules.

        Args:
            transport: SSH transport for executing remote commands

        Returns:
            List of SecurityIssue objects for exposed ports
        """
        issues: list[SecurityIssue] = []

        ufw_status = self._get_ufw_status(transport)

        result = transport.execute(
            "ss -tlnp 2>/dev/null | grep LISTEN || "
            "netstat -tlnp 2>/dev/null | grep LISTEN"
        )

        if not result.success:
            return issues

        for line in result.stdout.split('\n'):
            if not line.strip():
                continue

            match = re.search(r'(?:0\.0\.0\.0|::):(\d+)', line)
            if not match:
                continue

            port = int(match.group(1))

            if ufw_status['active'] and port not in ufw_status['allowed_ports']:
                continue

            if port in INTERNAL_ONLY_PORTS:
                service_name = INTERNAL_ONLY_PORTS[port]

                if port == GATEWAY_PORT:
                    continue

                issues.append(SecurityIssue(
                    level=AlertLevel.CRITICAL,
                    category=SecurityCategory.NETWORK,
                    title=f'{service_name} exposed on public interface',
                    description=(
                        f'Port {port} ({service_name}) is bound to 0.0.0.0, '
                        f'making it accessible from the internet. '
                        f'This service should only be accessible locally or via reverse proxy.'
                    ),
                    remediation=(
                        f'Option 1: Bind to localhost in docker-compose.yml:\n'
                        f'  ports:\n'
                        f'    - "127.0.0.1:{port}:{port}"\n\n'
                        f'Option 2: Block with firewall:\n'
                        f'  sudo ufw deny {port}\n\n'
                        f'Option 3: Use internal Docker network:\n'
                        f'  networks:\n'
                        f'    internal:\n'
                        f'      internal: true'
                    ),
                    evidence=line.strip(),
                    cwe_id='CWE-284',
                    references=[
                        'https://cwe.mitre.org/data/definitions/284.html',
                    ],
                ))

        return issues

    def check_firewall(self, transport: 'SSHTransport') -> list[SecurityIssue]:
        """
        Check if firewall is enabled and properly configured.

        Args:
            transport: SSH transport for executing remote commands

        Returns:
            List of SecurityIssue objects for firewall issues
        """
        issues: list[SecurityIssue] = []
        ufw_status = self._get_ufw_status(transport)

        if not ufw_status['installed']:
            issues.append(SecurityIssue(
                level=AlertLevel.WARNING,
                category=SecurityCategory.FIREWALL,
                title='Firewall (UFW) is not installed',
                description=(
                    'No host-level firewall detected. The server relies only on '
                    'network-level security (cloud provider firewalls, if any).'
                ),
                remediation=(
                    '1. Install UFW:\n'
                    '   sudo apt install ufw\n\n'
                    '2. Configure basic rules:\n'
                    '   sudo ufw default deny incoming\n'
                    '   sudo ufw default allow outgoing\n'
                    '   sudo ufw allow 22/tcp  # SSH\n'
                    '   sudo ufw allow 80/tcp  # HTTP\n'
                    '   sudo ufw allow 443/tcp # HTTPS\n\n'
                    '3. Enable:\n'
                    '   sudo ufw enable'
                ),
                evidence=ufw_status.get('raw_output', 'UFW not found'),
            ))
        elif not ufw_status['active']:
            issues.append(SecurityIssue(
                level=AlertLevel.WARNING,
                category=SecurityCategory.FIREWALL,
                title='Firewall (UFW) is not active',
                description=(
                    'UFW is installed but not enabled. All open ports are '
                    'accessible from the internet without filtering.'
                ),
                remediation=(
                    '1. Review current rules:\n'
                    '   sudo ufw status verbose\n\n'
                    '2. Enable firewall:\n'
                    '   sudo ufw enable\n\n'
                    'Warning: Ensure SSH (port 22) is allowed before enabling!'
                ),
                evidence=ufw_status.get('raw_output', ''),
            ))

        return issues

    def check_gateway_exposure(self, transport: 'SSHTransport') -> list[SecurityIssue]:
        """
        Check if Moltbot/Clawdbot gateway port is exposed without auth.

        Based on security research: 923 instances found on Shodan with no auth.
        Reference: moltbot/moltbot PR #2016

        Args:
            transport: SSH transport for executing remote commands

        Returns:
            List of SecurityIssue objects for gateway exposure issues
        """
        issues: list[SecurityIssue] = []

        result = transport.execute(
            f"ss -tlnp 2>/dev/null | grep ':{GATEWAY_PORT}'"
        )

        if not result.success or not result.stdout.strip():
            return issues

        is_public = bool(re.search(r'(?:0\.0\.0\.0|::):', result.stdout))

        if not is_public:
            return issues

        ufw_status = self._get_ufw_status(transport)

        if ufw_status['active'] and GATEWAY_PORT not in ufw_status['allowed_ports']:
            return issues

        gateway_auth = self._check_gateway_config(transport)

        if not gateway_auth['has_auth']:
            issues.append(SecurityIssue(
                level=AlertLevel.CRITICAL,
                category=SecurityCategory.GATEWAY,
                title='Moltbot gateway exposed without authentication',
                description=(
                    f'Port {GATEWAY_PORT} is bound to 0.0.0.0 without '
                    f'authentication. This allows anyone on the internet to:\n'
                    f'- Execute commands via the AI agent\n'
                    f'- Access conversation history\n'
                    f'- Retrieve stored credentials\n\n'
                    f'923 similar instances were found on Shodan.'
                ),
                remediation=(
                    '1. Enable gateway authentication in config:\n'
                    '   "gateway": {\n'
                    '     "auth": {\n'
                    '       "mode": "token",\n'
                    '       "token": "${GATEWAY_TOKEN}"\n'
                    '     }\n'
                    '   }\n\n'
                    '2. Generate a secure token:\n'
                    '   openssl rand -hex 32\n\n'
                    '3. Or bind to localhost only:\n'
                    '   "gateway": {\n'
                    '     "bind": "loopback"\n'
                    '   }\n\n'
                    '4. Or block with firewall:\n'
                    '   sudo ufw deny 18789'
                ),
                evidence=result.stdout.strip(),
                cwe_id='CWE-306',
                references=[
                    'https://github.com/moltbot/moltbot/pull/2016',
                    'https://www.shodan.io/search?query=%22moltbot%22+port%3A18789',
                ],
            ))
        elif gateway_auth['has_auth'] and is_public:
            issues.append(SecurityIssue(
                level=AlertLevel.INFO,
                category=SecurityCategory.GATEWAY,
                title='Moltbot gateway exposed (auth configured)',
                description=(
                    f'Port {GATEWAY_PORT} is publicly accessible but '
                    f'authentication is configured. Ensure the token is strong '
                    f'and regularly rotated.'
                ),
                remediation=(
                    '1. Verify token strength (min 32 chars):\n'
                    '   echo -n "$GATEWAY_TOKEN" | wc -c\n\n'
                    '2. Consider binding to localhost and using reverse proxy:\n'
                    '   "gateway": { "bind": "loopback" }'
                ),
                evidence=f'bind={gateway_auth.get("bind", "unknown")}',
            ))

        return issues

    def _get_ufw_status(self, transport: 'SSHTransport') -> dict:
        """
        Get UFW firewall status and allowed ports.

        Returns cached result for efficiency.

        Args:
            transport: SSH transport for executing remote commands

        Returns:
            Dict with keys: installed, active, allowed_ports, raw_output
        """
        if self._ufw_status is not None:
            return self._ufw_status

        result = transport.execute(
            "sudo ufw status verbose 2>/dev/null || echo 'UFW_NOT_INSTALLED'"
        )

        status: dict = {
            'installed': 'UFW_NOT_INSTALLED' not in result.stdout,
            'active': False,
            'allowed_ports': set(),
            'raw_output': result.stdout.strip(),
        }

        if status['installed']:
            status['active'] = 'status: active' in result.stdout.lower()

            for line in result.stdout.split('\n'):
                port_match = re.search(r'^(\d+)(?:/tcp|/udp)?\s+ALLOW', line)
                if port_match:
                    status['allowed_ports'].add(int(port_match.group(1)))

        self._ufw_status = status
        return status

    def _check_gateway_config(self, transport: 'SSHTransport') -> dict:
        """
        Parse gateway configuration from config files.

        Args:
            transport: SSH transport for executing remote commands

        Returns:
            Dict with keys: found, bind, auth_mode, token, has_auth
        """
        config: dict = {
            'found': False,
            'bind': 'loopback',
            'auth_mode': 'off',
            'token': '',
            'has_auth': False,
        }

        for config_path in GATEWAY_CONFIG_PATHS:
            result = transport.execute(f"cat {config_path} 2>/dev/null")

            if not result.success or not result.stdout.strip():
                continue

            try:
                data = json.loads(result.stdout)
                gateway = data.get('gateway', {})

                config['found'] = True
                config['bind'] = gateway.get('bind', 'loopback')

                auth = gateway.get('auth', {})
                config['auth_mode'] = auth.get('mode', 'off')
                config['token'] = auth.get('token', '')

                config['has_auth'] = (
                    config['auth_mode'] not in ('off', '') and
                    (config['token'] or config['auth_mode'] == 'password')
                )

                break

            except json.JSONDecodeError:
                continue

        return config
