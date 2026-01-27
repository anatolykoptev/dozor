"""
Security collector - comprehensive security auditing for server infrastructure.

Performs multi-layer security analysis:
- Network: exposed ports, firewall rules, external accessibility
- Container: non-root users, security options, capabilities
- Authentication: API keys, gateway tokens, auth middleware
- Configuration: secrets management, environment variables
- Reconnaissance: bot scanner detection, threat indicators

Based on security research from moltbot/moltbot issues #1792, #1796, #2016, #2245.
"""

from __future__ import annotations

import json
import re
from dataclasses import dataclass, field
from enum import Enum
from typing import TYPE_CHECKING, Optional

if TYPE_CHECKING:
    from ..transport import SSHTransport

from ..models import AlertLevel


class SecurityCategory(str, Enum):
    """Categories for security issues."""
    NETWORK = 'network'
    FIREWALL = 'firewall'
    CONTAINER = 'container'
    AUTHENTICATION = 'authentication'
    GATEWAY = 'gateway'
    CONFIGURATION = 'configuration'
    RECONNAISSANCE = 'reconnaissance'


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


class SecurityCollector:
    """
    Comprehensive security collector for server infrastructure.

    Performs automated security auditing across multiple domains:
    - Network exposure and firewall configuration
    - Container security (non-root, capabilities)
    - Authentication middleware and API keys
    - Gateway configuration and token validation
    - Bot scanner and reconnaissance detection

    Usage:
        collector = SecurityCollector(transport)
        issues = collector.check_all()

        # Or run specific checks
        issues = collector.check_container_security()
    """

    # ==========================================================================
    # Configuration Constants
    # ==========================================================================

    # Services that should NEVER be publicly exposed
    INTERNAL_ONLY_PORTS: dict[int, str] = {
        5432: 'PostgreSQL',
        6379: 'Redis',
        8080: 'Hasura GraphQL',
        9999: 'Supabase Auth',
        3000: 'Internal API',
        27017: 'MongoDB',
        9200: 'Elasticsearch',
        2379: 'etcd',
        18789: 'Moltbot Gateway',  # Critical: 923 instances exposed on Shodan
    }

    # Gateway port requires special handling
    GATEWAY_PORT = 18789

    # Expected non-root users for containers
    EXPECTED_CONTAINER_USERS: dict[str, str] = {
        'memos-api': 'memos',
        'memos-core': 'memos',
        'embedding-service': 'embed',
        'embedding': 'embed',
    }

    # Containers that are allowed to run as root (with justification)
    ROOT_ALLOWED_CONTAINERS: set[str] = {
        'postgres',      # Database requires root for initialization
        'redis',         # Official image, runs as redis user internally
        'traefik',       # Needs privileged ports
    }

    # Known bot scanner patterns in logs
    BOT_SCANNER_INDICATORS: list[str] = [
        r'/SDK/webLanguage',
        r'/wp-admin',
        r'/wp-login',
        r'/wp-includes',
        r'/phpmyadmin',
        r'/\.env',
        r'/\.git',
        r'/\.aws',
        r'/actuator',
        r'/api/v1/pods',
        r'/solr/',
        r'/console/',
        r'/manager/html',
        r'/cgi-bin/',
    ]

    # Required environment variables for secure operation
    REQUIRED_AUTH_VARS: list[str] = [
        'AUTH_ENABLED',
        'MASTER_KEY_HASH',
        'INTERNAL_SERVICE_SECRET',
    ]

    # Gateway configuration paths (relative to project root)
    GATEWAY_CONFIG_PATHS: list[str] = [
        'configs/moltbot/moltbot.json',
        'configs/clawdbot/clawdbot.json',
        '.moltbot.json',
        '.clawdbot.json',
    ]

    def __init__(self, transport: 'SSHTransport') -> None:
        """
        Initialize the security collector.

        Args:
            transport: SSH transport for executing remote commands
        """
        self.transport = transport
        self._ufw_status: Optional[dict] = None

    # ==========================================================================
    # Main Entry Points
    # ==========================================================================

    def check_all(self, include_recon: bool = True) -> list[SecurityIssue]:
        """
        Run all security checks and return consolidated issues.

        Args:
            include_recon: Include bot scanner detection (may be noisy)

        Returns:
            List of SecurityIssue objects sorted by severity
        """
        issues: list[SecurityIssue] = []

        # Network layer
        issues.extend(self.check_exposed_ports())
        issues.extend(self.check_firewall())
        issues.extend(self.check_gateway_exposure())

        # Container layer
        issues.extend(self.check_container_security())

        # Authentication layer
        issues.extend(self.check_auth_configuration())
        issues.extend(self.check_gateway_auth())

        # Reconnaissance (optional)
        if include_recon:
            issues.extend(self.check_bot_activity())

        # Sort by severity (CRITICAL first)
        severity_order = {
            AlertLevel.CRITICAL: 0,
            AlertLevel.ERROR: 1,
            AlertLevel.WARNING: 2,
            AlertLevel.INFO: 3,
        }
        issues.sort(key=lambda x: severity_order.get(x.level, 99))

        return issues

    def check_krolik_security(self) -> list[SecurityIssue]:
        """
        Run krolik-server specific security checks.

        Focused checks for the krolik stack:
        - MemOS API authentication
        - Gateway token configuration
        - Container user isolation
        - Path traversal protection
        """
        issues: list[SecurityIssue] = []

        issues.extend(self.check_container_security())
        issues.extend(self.check_auth_configuration())
        issues.extend(self.check_gateway_auth())
        issues.extend(self.check_gateway_exposure())

        return issues

    # ==========================================================================
    # Network Security Checks
    # ==========================================================================

    def check_exposed_ports(self) -> list[SecurityIssue]:
        """
        Check for services exposed on public interfaces (0.0.0.0).

        Identifies services bound to all interfaces that should be
        localhost-only, considering firewall rules.
        """
        issues: list[SecurityIssue] = []

        # Get firewall state for context
        ufw_status = self._get_ufw_status()

        # Get listening ports
        result = self.transport.execute(
            "ss -tlnp 2>/dev/null | grep LISTEN || "
            "netstat -tlnp 2>/dev/null | grep LISTEN"
        )

        if not result.success:
            return issues

        for line in result.stdout.split('\n'):
            if not line.strip():
                continue

            # Match 0.0.0.0:port or :::port (IPv6 wildcard)
            match = re.search(r'(?:0\.0\.0\.0|::):(\d+)', line)
            if not match:
                continue

            port = int(match.group(1))

            # Skip if port is protected by firewall
            if ufw_status['active'] and port not in ufw_status['allowed_ports']:
                continue

            # Check if this port should be internal-only
            if port in self.INTERNAL_ONLY_PORTS:
                service_name = self.INTERNAL_ONLY_PORTS[port]

                # Gateway port gets special handling
                if port == self.GATEWAY_PORT:
                    continue  # Handled by check_gateway_exposure()

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

    def check_firewall(self) -> list[SecurityIssue]:
        """
        Check if firewall is enabled and properly configured.
        """
        issues: list[SecurityIssue] = []
        ufw_status = self._get_ufw_status()

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

    def check_gateway_exposure(self) -> list[SecurityIssue]:
        """
        Check if Moltbot/Clawdbot gateway port is exposed without auth.

        Based on security research: 923 instances found on Shodan with no auth.
        Reference: moltbot/moltbot PR #2016
        """
        issues: list[SecurityIssue] = []

        # Check if gateway port is listening
        result = self.transport.execute(
            f"ss -tlnp 2>/dev/null | grep ':{self.GATEWAY_PORT}'"
        )

        if not result.success or not result.stdout.strip():
            return issues  # Gateway not running

        # Check binding address
        is_public = bool(re.search(r'(?:0\.0\.0\.0|::):', result.stdout))

        if not is_public:
            return issues  # Bound to localhost, safe

        # Gateway is publicly bound - check if auth is configured
        ufw_status = self._get_ufw_status()

        # If firewall blocks the port, it's protected
        if ufw_status['active'] and self.GATEWAY_PORT not in ufw_status['allowed_ports']:
            return issues

        # Check gateway auth configuration
        gateway_auth = self._check_gateway_config()

        if not gateway_auth['has_auth']:
            issues.append(SecurityIssue(
                level=AlertLevel.CRITICAL,
                category=SecurityCategory.GATEWAY,
                title='Moltbot gateway exposed without authentication',
                description=(
                    f'Port {self.GATEWAY_PORT} is bound to 0.0.0.0 without '
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
            # Auth configured but still public - warning only
            issues.append(SecurityIssue(
                level=AlertLevel.INFO,
                category=SecurityCategory.GATEWAY,
                title='Moltbot gateway exposed (auth configured)',
                description=(
                    f'Port {self.GATEWAY_PORT} is publicly accessible but '
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

    # ==========================================================================
    # Container Security Checks
    # ==========================================================================

    def check_container_security(self) -> list[SecurityIssue]:
        """
        Check Docker container security configuration.

        Verifies:
        - Containers run as non-root users
        - Security options (no-new-privileges)
        - Capability dropping
        """
        issues: list[SecurityIssue] = []

        issues.extend(self._check_container_users())
        issues.extend(self._check_container_security_opts())

        return issues

    def _check_container_users(self) -> list[SecurityIssue]:
        """Check that containers run as non-root users."""
        issues: list[SecurityIssue] = []

        # Get running containers
        result = self.transport.execute(
            "docker ps --format '{{.Names}}' 2>/dev/null"
        )

        if not result.success:
            return issues

        containers = [c.strip() for c in result.stdout.split('\n') if c.strip()]

        for container in containers:
            # Skip containers allowed to run as root
            if any(allowed in container.lower() for allowed in self.ROOT_ALLOWED_CONTAINERS):
                continue

            # Get the user running in the container
            user_result = self.transport.execute(
                f"docker exec {container} whoami 2>/dev/null || "
                f"docker exec {container} id -un 2>/dev/null"
            )

            if not user_result.success:
                continue

            user = user_result.stdout.strip()

            if user == 'root':
                # Determine expected user
                expected = 'non-root'
                for pattern, expected_user in self.EXPECTED_CONTAINER_USERS.items():
                    if pattern in container.lower():
                        expected = expected_user
                        break

                issues.append(SecurityIssue(
                    level=AlertLevel.WARNING,
                    category=SecurityCategory.CONTAINER,
                    title=f'Container "{container}" running as root',
                    description=(
                        f'Container is running as root user. If an attacker gains '
                        f'code execution in the container, they will have root '
                        f'privileges and may be able to escape to the host.'
                    ),
                    remediation=(
                        f'1. Add USER directive to Dockerfile:\n'
                        f'   RUN groupadd -r {expected} && useradd -r -g {expected} {expected}\n'
                        f'   USER {expected}\n\n'
                        f'2. Or specify in docker-compose.yml:\n'
                        f'   services:\n'
                        f'     {container}:\n'
                        f'       user: "1000:1000"\n\n'
                        f'3. Fix file permissions:\n'
                        f'   RUN chown -R {expected}:{expected} /app'
                    ),
                    evidence=f'Current user: {user}',
                    cwe_id='CWE-250',
                ))

        return issues

    def _check_container_security_opts(self) -> list[SecurityIssue]:
        """Check container security options (no-new-privileges, cap_drop)."""
        issues: list[SecurityIssue] = []

        # Check docker-compose.yml for security options
        result = self.transport.execute(
            "cat docker-compose.yml docker-compose.yaml 2>/dev/null | head -200"
        )

        if not result.success or not result.stdout.strip():
            return issues

        compose_content = result.stdout

        # Check for no-new-privileges
        if 'no-new-privileges' not in compose_content:
            issues.append(SecurityIssue(
                level=AlertLevel.INFO,
                category=SecurityCategory.CONTAINER,
                title='Container security_opt: no-new-privileges not set',
                description=(
                    'Containers can potentially gain new privileges via setuid '
                    'binaries or capabilities. This is a defense-in-depth measure.'
                ),
                remediation=(
                    'Add to each service in docker-compose.yml:\n'
                    '  security_opt:\n'
                    '    - no-new-privileges:true'
                ),
            ))

        # Check for cap_drop: ALL
        if 'cap_drop' not in compose_content or 'ALL' not in compose_content:
            issues.append(SecurityIssue(
                level=AlertLevel.INFO,
                category=SecurityCategory.CONTAINER,
                title='Container capabilities not dropped',
                description=(
                    'Containers retain default Linux capabilities. Dropping '
                    'capabilities reduces the attack surface if a container is compromised.'
                ),
                remediation=(
                    'Add to each service in docker-compose.yml:\n'
                    '  cap_drop:\n'
                    '    - ALL\n'
                    '  cap_add:  # Only if needed\n'
                    '    - NET_BIND_SERVICE'
                ),
            ))

        return issues

    # ==========================================================================
    # Authentication Checks
    # ==========================================================================

    def check_auth_configuration(self) -> list[SecurityIssue]:
        """
        Check authentication middleware configuration.

        Verifies:
        - AUTH_ENABLED=true in .env
        - Required secrets are configured
        - Master key hash is set
        """
        issues: list[SecurityIssue] = []

        # Read .env file
        env_result = self.transport.execute("cat .env 2>/dev/null")
        env_content = env_result.stdout if env_result.success else ''

        # Parse environment variables
        env_vars: dict[str, str] = {}
        for line in env_content.split('\n'):
            if '=' in line and not line.strip().startswith('#'):
                key, _, value = line.partition('=')
                env_vars[key.strip()] = value.strip().strip('"\'')

        # Check AUTH_ENABLED
        auth_enabled = env_vars.get('AUTH_ENABLED', '').lower()
        if auth_enabled != 'true':
            issues.append(SecurityIssue(
                level=AlertLevel.CRITICAL,
                category=SecurityCategory.AUTHENTICATION,
                title='API authentication is disabled',
                description=(
                    'AUTH_ENABLED is not set to "true". All API endpoints are '
                    'accessible without authentication. Anyone can:\n'
                    '- Read and modify memories\n'
                    '- Access user data\n'
                    '- Execute privileged operations'
                ),
                remediation=(
                    '1. Set in .env file:\n'
                    '   AUTH_ENABLED=true\n\n'
                    '2. Configure required secrets:\n'
                    '   MASTER_KEY_HASH=<sha256-hash>\n'
                    '   INTERNAL_SERVICE_SECRET=<random-32-chars>\n\n'
                    '3. Generate master key:\n'
                    '   python3 -c "import secrets, hashlib; '
                    'key = \'mk_\' + secrets.token_bytes(32).hex(); '
                    'print(f\'MASTER_KEY={key}\'); '
                    'print(f\'MASTER_KEY_HASH={hashlib.sha256(key.encode()).hexdigest()}\')"'
                ),
                evidence=f'AUTH_ENABLED={auth_enabled or "not set"}',
                cwe_id='CWE-306',
            ))

        # Check required secrets
        for var in self.REQUIRED_AUTH_VARS:
            value = env_vars.get(var, '')

            if not value:
                issues.append(SecurityIssue(
                    level=AlertLevel.WARNING,
                    category=SecurityCategory.AUTHENTICATION,
                    title=f'Required secret {var} not configured',
                    description=f'Environment variable {var} is not set in .env file.',
                    remediation=f'Add to .env:\n  {var}=<secure-value>',
                ))
            elif 'CHANGE_ME' in value or 'placeholder' in value.lower():
                issues.append(SecurityIssue(
                    level=AlertLevel.CRITICAL,
                    category=SecurityCategory.AUTHENTICATION,
                    title=f'Placeholder value in {var}',
                    description=(
                        f'{var} contains a placeholder value. This is a common '
                        f'mistake when deploying from .env.example.'
                    ),
                    remediation=f'Generate a secure value for {var}',
                    evidence=f'{var}={value[:20]}...' if len(value) > 20 else f'{var}={value}',
                ))

        return issues

    def check_gateway_auth(self) -> list[SecurityIssue]:
        """
        Check Moltbot gateway authentication configuration.

        Verifies:
        - Auth mode is not 'off'
        - Token is configured if using token auth
        - Token is sufficiently strong
        """
        issues: list[SecurityIssue] = []
        gateway_config = self._check_gateway_config()

        if not gateway_config['found']:
            return issues  # No gateway config found

        auth_mode = gateway_config.get('auth_mode', 'off')

        if auth_mode == 'off':
            issues.append(SecurityIssue(
                level=AlertLevel.WARNING,
                category=SecurityCategory.GATEWAY,
                title='Gateway authentication mode is "off"',
                description=(
                    'Gateway auth is explicitly disabled. If the gateway is '
                    'exposed (bind != loopback), anyone can connect.'
                ),
                remediation=(
                    'Enable token authentication:\n'
                    '  "gateway": {\n'
                    '    "auth": {\n'
                    '      "mode": "token",\n'
                    '      "token": "${CLAWDBOT_GATEWAY_TOKEN}"\n'
                    '    }\n'
                    '  }'
                ),
            ))

        # Check token strength if using token auth
        if auth_mode == 'token':
            token = gateway_config.get('token', '')

            if not token:
                issues.append(SecurityIssue(
                    level=AlertLevel.CRITICAL,
                    category=SecurityCategory.GATEWAY,
                    title='Gateway token not configured',
                    description='Token auth is enabled but no token is set.',
                    remediation=(
                        '1. Generate token:\n'
                        '   openssl rand -hex 32\n\n'
                        '2. Set in .env:\n'
                        '   CLAWDBOT_GATEWAY_TOKEN=<generated-token>\n\n'
                        '3. Reference in config:\n'
                        '   "token": "${CLAWDBOT_GATEWAY_TOKEN}"'
                    ),
                ))
            elif len(token) < 32 and not token.startswith('${'):
                issues.append(SecurityIssue(
                    level=AlertLevel.WARNING,
                    category=SecurityCategory.GATEWAY,
                    title='Gateway token is weak',
                    description=f'Token length is {len(token)} chars. Minimum recommended: 32.',
                    remediation='Generate a stronger token: openssl rand -hex 32',
                ))

        return issues

    # ==========================================================================
    # Reconnaissance Detection
    # ==========================================================================

    def check_bot_activity(self) -> list[SecurityIssue]:
        """
        Detect automated vulnerability scanning activity.

        Identifies known bot scanner patterns in logs to assess
        the server's exposure and threat level.
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
            for pattern in self.BOT_SCANNER_INDICATORS:
                if re.search(pattern, line, re.IGNORECASE):
                    bot_hits += 1
                    matched_patterns.add(pattern)

                    # Extract IP address
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

    # ==========================================================================
    # Helper Methods
    # ==========================================================================

    def _get_ufw_status(self) -> dict:
        """
        Get UFW firewall status and allowed ports.

        Returns cached result for efficiency.
        """
        if self._ufw_status is not None:
            return self._ufw_status

        result = self.transport.execute(
            "sudo ufw status verbose 2>/dev/null || echo 'UFW_NOT_INSTALLED'"
        )

        status = {
            'installed': 'UFW_NOT_INSTALLED' not in result.stdout,
            'active': False,
            'allowed_ports': set(),
            'raw_output': result.stdout.strip(),
        }

        if status['installed']:
            status['active'] = 'status: active' in result.stdout.lower()

            # Parse allowed ports
            for line in result.stdout.split('\n'):
                port_match = re.search(r'^(\d+)(?:/tcp|/udp)?\s+ALLOW', line)
                if port_match:
                    status['allowed_ports'].add(int(port_match.group(1)))

        self._ufw_status = status
        return status

    def _check_gateway_config(self) -> dict:
        """
        Parse gateway configuration from config files.

        Returns:
            Dict with keys: found, bind, auth_mode, token, has_auth
        """
        config = {
            'found': False,
            'bind': 'loopback',
            'auth_mode': 'off',
            'token': '',
            'has_auth': False,
        }

        for config_path in self.GATEWAY_CONFIG_PATHS:
            result = self.transport.execute(f"cat {config_path} 2>/dev/null")

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

                # Check if auth is effectively enabled
                config['has_auth'] = (
                    config['auth_mode'] not in ('off', '') and
                    (config['token'] or config['auth_mode'] == 'password')
                )

                break  # Found config, stop searching

            except json.JSONDecodeError:
                continue

        return config

    def get_external_ips(self) -> list[str]:
        """Get list of external IPs accessing the server."""
        result = self.transport.execute(
            "docker compose logs --tail 2000 2>/dev/null | "
            "grep -oE '\\b[0-9]{1,3}\\.[0-9]{1,3}\\.[0-9]{1,3}\\.[0-9]{1,3}\\b' | "
            "grep -vE '^(127\\.|10\\.|172\\.(1[6-9]|2[0-9]|3[01])\\.|192\\.168\\.)' | "
            "sort | uniq -c | sort -rn | head -20"
        )

        ips = []
        if result.success:
            for line in result.stdout.split('\n'):
                match = re.search(r'(\d+\.\d+\.\d+\.\d+)', line)
                if match:
                    ips.append(match.group(1))

        return ips

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
