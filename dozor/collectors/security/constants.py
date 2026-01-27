"""
Security constants and configuration.

Centralized configuration for all security checks.
Based on security research from moltbot/moltbot issues #1792, #1796, #2016, #2245.
"""

# =============================================================================
# Network Security
# =============================================================================

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

# =============================================================================
# Container Security
# =============================================================================

# Expected non-root users for containers
EXPECTED_CONTAINER_USERS: dict[str, str] = {
    'memos-api': 'memos',
    'memos-core': 'memos',
    'embedding-service': 'embed',
    'embedding': 'embed',
    'clawdbot': 'node',
    'moltbot': 'node',
}

# Containers that are allowed to run as root (with justification)
ROOT_ALLOWED_CONTAINERS: set[str] = {
    'postgres',      # Database requires root for initialization
    'redis',         # Official image, runs as redis user internally
    'traefik',       # Needs privileged ports
    'caddy',         # Reverse proxy, needs port 80/443
}

# Dangerous host mounts that should never be exposed to containers
DANGEROUS_HOST_MOUNTS: list[str] = [
    '/.claude',           # Claude credentials
    '/.ssh',              # SSH keys
    '/.aws',              # AWS credentials
    '/.kube',             # Kubernetes config
    '/.gnupg',            # GPG keys
    '/etc/shadow',        # System passwords
    '/etc/passwd',        # System users
    '/var/run/docker.sock',  # Docker socket (container escape)
]

# =============================================================================
# Authentication
# =============================================================================

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

# Required CORS configuration
REQUIRED_CORS_HEADERS: list[str] = [
    'Access-Control-Allow-Origin',
    'Access-Control-Allow-Methods',
    'Access-Control-Allow-Headers',
]

# =============================================================================
# API Hardening
# =============================================================================

# Security headers that should be present
REQUIRED_SECURITY_HEADERS: dict[str, str] = {
    'X-Content-Type-Options': 'nosniff',
    'X-Frame-Options': 'DENY',
    'X-XSS-Protection': '1; mode=block',
    'Strict-Transport-Security': 'max-age=',
    'Content-Security-Policy': '',  # Just check presence
}

# Patterns indicating stack trace exposure
STACK_TRACE_PATTERNS: list[str] = [
    r'Traceback \(most recent call last\)',
    r'at .+\(.+:\d+:\d+\)',  # JS stack trace
    r'File ".+", line \d+',  # Python stack trace
    r'Exception in thread',  # Java stack trace
]

# SQL injection indicators in code
SQL_INJECTION_PATTERNS: list[str] = [
    r'f".*SELECT.*{',           # f-string with SELECT
    r"f'.*SELECT.*{",           # f-string with SELECT
    r'f".*INSERT.*{',           # f-string with INSERT
    r'f".*UPDATE.*{',           # f-string with UPDATE
    r'f".*DELETE.*{',           # f-string with DELETE
    r'\.format\(.*\).*SELECT',  # .format() with SQL
    r'\+ *[\'"].*SELECT',       # String concatenation with SQL
]

# Path traversal indicators
PATH_TRAVERSAL_PATTERNS: list[str] = [
    r'\.\./',                   # Parent directory traversal
    r'%2e%2e%2f',               # URL encoded ../
    r'%252e%252e%252f',         # Double URL encoded
]

# =============================================================================
# Reconnaissance Detection
# =============================================================================

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

# =============================================================================
# Moltbot/Clawdbot Upstream Vulnerabilities
# Based on GitHub issues #1792, #1796
# =============================================================================

UPSTREAM_CRITICAL_VULNS: list[dict] = [
    {
        'id': 'MOLT-001',
        'title': 'OAuth tokens stored plaintext',
        'location': 'auth-profiles/store.ts',
        'cwe': 'CWE-312',
        'fixed_in': None,
    },
    {
        'id': 'MOLT-002',
        'title': 'CSRF in OAuth (no state validation)',
        'location': 'qwen-portal-auth/oauth.ts',
        'cwe': 'CWE-352',
        'fixed_in': None,
    },
    {
        'id': 'MOLT-003',
        'title': 'Hardcoded OAuth client secrets',
        'location': 'google-antigravity-auth/index.ts',
        'cwe': 'CWE-798',
        'fixed_in': None,
    },
    {
        'id': 'MOLT-004',
        'title': 'Webhook signature bypass',
        'location': 'voice-call/webhook-security.ts',
        'cwe': 'CWE-347',
        'fixed_in': None,
    },
    {
        'id': 'MOLT-005',
        'title': 'Token refresh race condition',
        'location': 'auth-profiles/oauth.ts',
        'cwe': 'CWE-362',
        'fixed_in': None,
    },
    {
        'id': 'MOLT-006',
        'title': 'Path traversal in agent dirs',
        'location': 'commands/auth-choice.ts',
        'cwe': 'CWE-22',
        'fixed_in': None,
    },
    {
        'id': 'MOLT-007',
        'title': 'Insufficient file permission checks',
        'location': 'security/fix.ts',
        'cwe': 'CWE-732',
        'fixed_in': None,
    },
    {
        'id': 'MOLT-008',
        'title': 'Token expiry fallback to stale',
        'location': 'auth-profiles/oauth.ts',
        'cwe': 'CWE-613',
        'fixed_in': None,
    },
]

UPSTREAM_HIGH_VULNS: list[dict] = [
    {
        'id': 'MOLT-101',
        'title': 'WhatsApp data sent to AI providers',
        'location': 'web/inbound/monitor.ts',
        'cwe': 'CWE-359',
        'fixed_in': None,
    },
    {
        'id': 'MOLT-102',
        'title': 'Plugins without sandbox',
        'location': 'plugins/loader.ts',
        'cwe': 'CWE-94',
        'fixed_in': None,
    },
    {
        'id': 'MOLT-103',
        'title': 'ws:// instead of wss://',
        'location': 'CHANGELOG.md, apps/macos',
        'cwe': 'CWE-319',
        'fixed_in': None,
    },
    {
        'id': 'MOLT-104',
        'title': 'Docker runs as root',
        'location': 'Dockerfile',
        'cwe': 'CWE-250',
        'fixed_in': None,
    },
    {
        'id': 'MOLT-105',
        'title': 'Incomplete shell metachar blocklist',
        'location': 'infra/exec-safety.ts',
        'cwe': 'CWE-78',
        'fixed_in': None,
    },
    {
        'id': 'MOLT-106',
        'title': 'TOCTOU race in media server',
        'location': 'media/server.ts',
        'cwe': 'CWE-367',
        'fixed_in': None,
    },
    {
        'id': 'MOLT-107',
        'title': 'eval() in browser automation',
        'location': 'browser/pw-tools-core.ts',
        'cwe': 'CWE-95',
        'fixed_in': None,
    },
    {
        'id': 'MOLT-108',
        'title': 'Security bypass with elevatedMode=full',
        'location': 'agents/bash-tools.exec.ts',
        'cwe': 'CWE-269',
        'fixed_in': None,
    },
    {
        'id': 'MOLT-109',
        'title': 'Prototype pollution in WebSocket',
        'location': 'ui/src/ui/gateway.ts',
        'cwe': 'CWE-1321',
        'fixed_in': None,
    },
]
