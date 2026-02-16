"""
API hardening security checks.

Validates API security configuration:
- Stack trace exposure in error responses
- Security headers (X-Frame-Options, X-Content-Type-Options, etc.)
- Path traversal vulnerability patterns
- SQL injection patterns in source code
"""

from __future__ import annotations

import re
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from ...transport import SSHTransport

from ...models import AlertLevel
from .base import SecurityCategory, SecurityIssue
from .constants import (
    PATH_TRAVERSAL_PATTERNS,
    REQUIRED_SECURITY_HEADERS,
    SQL_INJECTION_PATTERNS,
    STACK_TRACE_PATTERNS,
)


class ApiHardeningChecker:
    """API hardening security checker for stack traces, headers, and injection patterns."""

    def check_stack_traces(self, transport: 'SSHTransport') -> list[SecurityIssue]:
        """Check if error responses expose stack traces in logs."""
        issues: list[SecurityIssue] = []
        result = transport.execute(
            "docker compose logs --tail 500 2>/dev/null | "
            "grep -iE 'error|exception|traceback|failed' | head -100"
        )
        if not result.success:
            return issues

        matched_patterns: set[str] = set()
        sample_traces: list[str] = []

        for line in result.stdout.split('\n'):
            if not line.strip():
                continue
            for pattern in STACK_TRACE_PATTERNS:
                if re.search(pattern, line, re.IGNORECASE):
                    matched_patterns.add(pattern)
                    if len(sample_traces) < 3:
                        sample_traces.append(line[:150] + '...' if len(line) > 150 else line)
                    break

        if matched_patterns:
            issues.append(SecurityIssue(
                level=AlertLevel.WARNING,
                category=SecurityCategory.API_HARDENING,
                title='Stack traces detected in logs',
                description=(
                    f'Error responses may expose stack traces ({len(matched_patterns)} patterns). '
                    'This reveals internal paths, framework versions, and implementation details.'
                ),
                remediation=(
                    '1. Set NODE_ENV=production or DEBUG=False\n'
                    '2. Use custom error handlers that log internally but return generic messages\n'
                    '3. Return structured errors: {"error": {"code": "ERR_001", "message": "..."}}'
                ),
                evidence='\n'.join(sample_traces) if sample_traces else None,
                cwe_id='CWE-209',
                references=['https://cwe.mitre.org/data/definitions/209.html'],
            ))
        return issues

    def check_security_headers(self, transport: 'SSHTransport') -> list[SecurityIssue]:
        """Check for presence of security headers on main service endpoint."""
        issues: list[SecurityIssue] = []

        # Check if any service is responding
        result = transport.execute(
            "curl -sI -o /dev/null -w '%{http_code}' http://localhost:80 2>/dev/null || "
            "curl -sI -o /dev/null -w '%{http_code}' http://localhost:3000 2>/dev/null || "
            "curl -sI -o /dev/null -w '%{http_code}' http://localhost:8080 2>/dev/null"
        )
        if not result.success or result.stdout.strip() not in ('200', '301', '302', '404'):
            return issues

        # Get headers
        headers_result = transport.execute(
            "curl -sI http://localhost:80 2>/dev/null || "
            "curl -sI http://localhost:3000 2>/dev/null || "
            "curl -sI http://localhost:8080 2>/dev/null"
        )
        if not headers_result.success:
            return issues

        headers_content = headers_result.stdout.lower()
        missing_headers: list[str] = []
        misconfigured: list[tuple[str, str]] = []

        for header, expected in REQUIRED_SECURITY_HEADERS.items():
            header_lower = header.lower()
            if header_lower not in headers_content:
                missing_headers.append(header)
            elif expected:
                match = re.search(rf'{re.escape(header_lower)}:\s*(.+)', headers_content)
                if match and expected.lower() not in match.group(1):
                    misconfigured.append((header, match.group(1).strip()))

        if missing_headers:
            issues.append(SecurityIssue(
                level=AlertLevel.WARNING,
                category=SecurityCategory.API_HARDENING,
                title=f'Missing security headers ({len(missing_headers)})',
                description=f'Missing: {", ".join(missing_headers)}. Protects against clickjacking, MIME sniffing, XSS.',
                remediation=(
                    'Nginx: add_header X-Frame-Options "DENY" always;\n'
                    'Express: app.use(require("helmet")());\n'
                    'Traefik: Use headers middleware'
                ),
                evidence=f'Missing: {", ".join(missing_headers)}',
                cwe_id='CWE-693',
                references=['https://owasp.org/www-project-secure-headers/'],
            ))

        if misconfigured:
            details = '\n'.join(f'  {h}: {v}' for h, v in misconfigured)
            issues.append(SecurityIssue(
                level=AlertLevel.INFO,
                category=SecurityCategory.API_HARDENING,
                title='Security headers may be misconfigured',
                description=f'Headers with unexpected values:\n{details}',
                remediation='Review and update header values to match security best practices.',
                evidence=details,
            ))
        return issues

    def check_path_traversal(self, transport: 'SSHTransport') -> list[SecurityIssue]:
        """Check for path traversal vulnerability patterns in source code."""
        issues: list[SecurityIssue] = []
        search_pattern = '|'.join(PATH_TRAVERSAL_PATTERNS)

        result = transport.execute(
            f"grep -rn --include='*.py' --include='*.js' --include='*.ts' "
            f"-E '{search_pattern}' . 2>/dev/null | "
            f"grep -v node_modules | grep -v '.git' | grep -v test | grep -v spec | head -20"
        )
        if not result.success or not result.stdout.strip():
            return issues

        files: list[str] = []
        for line in result.stdout.split('\n'):
            if line.strip():
                match = re.match(r'^([^:]+):', line)
                if match and match.group(1) not in files:
                    files.append(match.group(1))

        if files:
            issues.append(SecurityIssue(
                level=AlertLevel.WARNING,
                category=SecurityCategory.API_HARDENING,
                title='Potential path traversal patterns in code',
                description=f'Found {len(files)} file(s) with ../ patterns. May be intentional or vulnerability.',
                remediation=(
                    'Validate paths: full_path = os.path.normpath(os.path.join(base, user_input))\n'
                    'Check: if not full_path.startswith(base): raise ValueError("Invalid path")'
                ),
                evidence=f'Files: {", ".join(files[:5])}',
                cwe_id='CWE-22',
                references=['https://owasp.org/www-community/attacks/Path_Traversal'],
            ))
        return issues

    def check_sql_injection(self, transport: 'SSHTransport') -> list[SecurityIssue]:
        """Check for SQL injection patterns in source code."""
        issues: list[SecurityIssue] = []
        search_pattern = '|'.join(SQL_INJECTION_PATTERNS)

        result = transport.execute(
            f"grep -rn --include='*.py' --include='*.js' --include='*.ts' "
            f"-E '{search_pattern}' . 2>/dev/null | "
            f"grep -v node_modules | grep -v '.git' | grep -v test | grep -v spec | grep -v migration | head -20"
        )
        if not result.success or not result.stdout.strip():
            return issues

        locations: list[str] = []
        for line in result.stdout.split('\n'):
            if line.strip():
                locations.append(line[:100] + '...' if len(line) > 100 else line)

        if locations:
            issues.append(SecurityIssue(
                level=AlertLevel.CRITICAL,
                category=SecurityCategory.API_HARDENING,
                title='Potential SQL injection patterns detected',
                description=(
                    f'Found {len(locations)} location(s) with f-string/concat SQL patterns. '
                    'Allows data extraction, modification, and potentially shell access.'
                ),
                remediation=(
                    'Use parameterized queries: cursor.execute("SELECT * FROM users WHERE id = %s", (id,))\n'
                    'Or ORM: User.query.filter_by(id=user_id).first()'
                ),
                evidence='\n'.join(locations[:3]),
                cwe_id='CWE-89',
                references=['https://owasp.org/www-community/attacks/SQL_Injection'],
            ))
        return issues
