"""
Authentication security checks.

Verifies authentication configuration, gateway auth, CORS, and rate limiting.
"""

from __future__ import annotations

import json
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from ...transport import SSHTransport

from .base import SecurityCategory, SecurityIssue
from .constants import GATEWAY_CONFIG_PATHS, REQUIRED_AUTH_VARS, REQUIRED_CORS_HEADERS
from ...models import AlertLevel


class AuthenticationChecker:
    """
    Authentication security checker.

    Performs comprehensive authentication security checks:
    - API authentication configuration
    - Gateway token authentication
    - CORS whitelist configuration
    - Rate limiting configuration
    """

    def check_auth_configuration(self, transport: 'SSHTransport') -> list[SecurityIssue]:
        """
        Check authentication middleware configuration.

        Verifies:
        - AUTH_ENABLED=true in .env
        - Required secrets are configured
        - Master key hash is set
        """
        issues: list[SecurityIssue] = []

        env_result = transport.execute("cat .env 2>/dev/null")
        env_content = env_result.stdout if env_result.success else ''

        env_vars = self._parse_env_content(env_content)

        issues.extend(self._check_auth_enabled(env_vars))
        issues.extend(self._check_required_secrets(env_vars))

        return issues

    def check_gateway_auth(self, transport: 'SSHTransport') -> list[SecurityIssue]:
        """
        Check Moltbot gateway authentication configuration.

        Verifies:
        - Auth mode is not 'off'
        - Token is configured if using token auth
        - Token is sufficiently strong
        """
        issues: list[SecurityIssue] = []
        gateway_config = self._check_gateway_config(transport)

        if not gateway_config['found']:
            return issues

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

        if auth_mode == 'token':
            issues.extend(self._check_token_strength(gateway_config))

        return issues

    def check_cors_configuration(self, transport: 'SSHTransport') -> list[SecurityIssue]:
        """
        Check CORS whitelist configuration.

        Verifies:
        - CORS is configured (not wildcard *)
        - Required CORS headers are present
        - Origins are explicitly whitelisted
        """
        issues: list[SecurityIssue] = []

        env_result = transport.execute("cat .env 2>/dev/null")
        env_content = env_result.stdout if env_result.success else ''
        env_vars = self._parse_env_content(env_content)

        cors_origins = env_vars.get('CORS_ORIGINS', '')
        cors_allowed = env_vars.get('CORS_ALLOWED_ORIGINS', '')
        cors_config = cors_origins or cors_allowed

        if not cors_config:
            issues.append(SecurityIssue(
                level=AlertLevel.WARNING,
                category=SecurityCategory.CONFIGURATION,
                title='CORS origins not configured',
                description=(
                    'No CORS_ORIGINS or CORS_ALLOWED_ORIGINS configured. '
                    'This may allow requests from any origin or block all '
                    'cross-origin requests depending on defaults.'
                ),
                remediation=(
                    'Configure allowed origins in .env:\n'
                    '  CORS_ORIGINS=https://yourdomain.com,https://app.yourdomain.com\n\n'
                    'Or for single origin:\n'
                    '  CORS_ALLOWED_ORIGINS=https://yourdomain.com'
                ),
                cwe_id='CWE-942',
            ))
        elif cors_config == '*':
            issues.append(SecurityIssue(
                level=AlertLevel.CRITICAL,
                category=SecurityCategory.CONFIGURATION,
                title='CORS allows all origins (wildcard *)',
                description=(
                    'CORS is configured with wildcard (*), allowing requests '
                    'from any origin. This enables:\n'
                    '- Cross-site request forgery attacks\n'
                    '- Data exfiltration from malicious sites\n'
                    '- Credential theft if credentials mode is enabled'
                ),
                remediation=(
                    'Replace wildcard with explicit origins:\n'
                    '  CORS_ORIGINS=https://yourdomain.com,https://app.yourdomain.com\n\n'
                    'Never use wildcard (*) in production.'
                ),
                evidence=f'CORS configuration: {cors_config}',
                cwe_id='CWE-942',
            ))

        issues.extend(self._check_cors_headers(transport))

        return issues

    def check_rate_limiting(self, transport: 'SSHTransport') -> list[SecurityIssue]:
        """
        Check rate limiting configuration.

        Verifies:
        - Rate limiting is enabled
        - Limits are configured appropriately
        - Both API and authentication endpoints are protected
        """
        issues: list[SecurityIssue] = []

        env_result = transport.execute("cat .env 2>/dev/null")
        env_content = env_result.stdout if env_result.success else ''
        env_vars = self._parse_env_content(env_content)

        rate_limit_enabled = env_vars.get('RATE_LIMIT_ENABLED', '').lower()
        rate_limit_max = env_vars.get('RATE_LIMIT_MAX', '')
        rate_limit_window = env_vars.get('RATE_LIMIT_WINDOW_MS', '')

        if rate_limit_enabled not in ('true', '1', 'yes'):
            issues.append(SecurityIssue(
                level=AlertLevel.WARNING,
                category=SecurityCategory.API_HARDENING,
                title='Rate limiting is not enabled',
                description=(
                    'Rate limiting is not configured or disabled. This allows:\n'
                    '- Brute force attacks on authentication\n'
                    '- API abuse and resource exhaustion\n'
                    '- Denial of service attacks'
                ),
                remediation=(
                    'Enable rate limiting in .env:\n'
                    '  RATE_LIMIT_ENABLED=true\n'
                    '  RATE_LIMIT_MAX=100\n'
                    '  RATE_LIMIT_WINDOW_MS=60000\n\n'
                    'For authentication endpoints, use stricter limits:\n'
                    '  AUTH_RATE_LIMIT_MAX=5\n'
                    '  AUTH_RATE_LIMIT_WINDOW_MS=300000'
                ),
                cwe_id='CWE-770',
            ))
        else:
            if not rate_limit_max:
                issues.append(SecurityIssue(
                    level=AlertLevel.INFO,
                    category=SecurityCategory.API_HARDENING,
                    title='Rate limit maximum not configured',
                    description='RATE_LIMIT_MAX is not set, using defaults.',
                    remediation='Set RATE_LIMIT_MAX in .env (e.g., 100 requests)',
                ))

            if not rate_limit_window:
                issues.append(SecurityIssue(
                    level=AlertLevel.INFO,
                    category=SecurityCategory.API_HARDENING,
                    title='Rate limit window not configured',
                    description='RATE_LIMIT_WINDOW_MS is not set, using defaults.',
                    remediation='Set RATE_LIMIT_WINDOW_MS in .env (e.g., 60000 for 1 minute)',
                ))

        issues.extend(self._check_auth_rate_limiting(env_vars))

        return issues

    def _parse_env_content(self, env_content: str) -> dict[str, str]:
        """Parse .env file content into a dictionary."""
        env_vars: dict[str, str] = {}
        for line in env_content.split('\n'):
            if '=' in line and not line.strip().startswith('#'):
                key, _, value = line.partition('=')
                env_vars[key.strip()] = value.strip().strip('"\'')
        return env_vars

    def _check_auth_enabled(self, env_vars: dict[str, str]) -> list[SecurityIssue]:
        """Check if AUTH_ENABLED is set to true."""
        issues: list[SecurityIssue] = []

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

        return issues

    def _check_required_secrets(self, env_vars: dict[str, str]) -> list[SecurityIssue]:
        """Check that all required authentication secrets are configured."""
        issues: list[SecurityIssue] = []

        for var in REQUIRED_AUTH_VARS:
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

    def _check_gateway_config(self, transport: 'SSHTransport') -> dict:
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

    def _check_token_strength(self, gateway_config: dict) -> list[SecurityIssue]:
        """Check if the gateway token is strong enough."""
        issues: list[SecurityIssue] = []

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

    def _check_cors_headers(self, transport: 'SSHTransport') -> list[SecurityIssue]:
        """Check CORS headers in nginx/traefik configuration."""
        issues: list[SecurityIssue] = []

        nginx_result = transport.execute(
            "cat /etc/nginx/nginx.conf /etc/nginx/conf.d/*.conf 2>/dev/null | "
            "grep -i 'access-control' || true"
        )

        traefik_result = transport.execute(
            "cat traefik.yml traefik.toml docker-compose.yml 2>/dev/null | "
            "grep -i 'accesscontrol\\|cors' || true"
        )

        has_cors_config = bool(
            nginx_result.stdout.strip() or traefik_result.stdout.strip()
        )

        if not has_cors_config:
            missing_headers = REQUIRED_CORS_HEADERS
            if missing_headers:
                issues.append(SecurityIssue(
                    level=AlertLevel.INFO,
                    category=SecurityCategory.CONFIGURATION,
                    title='CORS headers not found in proxy configuration',
                    description=(
                        'No CORS configuration found in nginx or traefik. '
                        'CORS may be handled by the application server.'
                    ),
                    remediation=(
                        'If using a reverse proxy, configure CORS headers:\n\n'
                        'Nginx:\n'
                        '  add_header Access-Control-Allow-Origin "https://yourdomain.com";\n'
                        '  add_header Access-Control-Allow-Methods "GET, POST, OPTIONS";\n'
                        '  add_header Access-Control-Allow-Headers "Content-Type, Authorization";\n\n'
                        'Traefik:\n'
                        '  Use cors middleware with allowedOrigins configuration.'
                    ),
                ))

        return issues

    def _check_auth_rate_limiting(self, env_vars: dict[str, str]) -> list[SecurityIssue]:
        """Check rate limiting specifically for authentication endpoints."""
        issues: list[SecurityIssue] = []

        auth_rate_limit = env_vars.get('AUTH_RATE_LIMIT_MAX', '')
        login_rate_limit = env_vars.get('LOGIN_RATE_LIMIT', '')

        if not auth_rate_limit and not login_rate_limit:
            issues.append(SecurityIssue(
                level=AlertLevel.WARNING,
                category=SecurityCategory.AUTHENTICATION,
                title='No dedicated rate limiting for authentication',
                description=(
                    'Authentication endpoints do not have dedicated rate limiting. '
                    'This makes brute force attacks easier:\n'
                    '- Password guessing attacks\n'
                    '- Token enumeration\n'
                    '- Account lockout bypass'
                ),
                remediation=(
                    'Configure stricter rate limits for auth endpoints:\n'
                    '  AUTH_RATE_LIMIT_MAX=5\n'
                    '  AUTH_RATE_LIMIT_WINDOW_MS=300000\n\n'
                    'Or use login-specific limiting:\n'
                    '  LOGIN_RATE_LIMIT=5/5m'
                ),
                cwe_id='CWE-307',
            ))

        return issues
