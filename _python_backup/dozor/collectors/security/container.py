"""
Container security checks.

Verifies Docker container security configuration:
- Non-root user execution
- Security options (no-new-privileges, cap_drop)
- Dangerous host mounts
- Internal network isolation
"""

from __future__ import annotations

from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from ...transport import SSHTransport

from .base import SecurityCategory, SecurityIssue
from .constants import (
    DANGEROUS_HOST_MOUNTS,
    EXPECTED_CONTAINER_USERS,
    ROOT_ALLOWED_CONTAINERS,
)
from ...models import AlertLevel


class ContainerSecurityChecker:
    """
    Security checker for Docker container configuration.

    Performs checks for:
    - Container user privileges (non-root)
    - Security options (no-new-privileges, cap_drop)
    - Dangerous host mounts (credentials, SSH keys)
    - Internal network isolation for backend services
    """

    def check(self, transport: 'SSHTransport') -> list[SecurityIssue]:
        """
        Run all container security checks.

        Args:
            transport: SSH transport for executing commands

        Returns:
            List of detected security issues
        """
        issues: list[SecurityIssue] = []

        issues.extend(self.check_container_users(transport))
        issues.extend(self.check_container_security_opts(transport))
        issues.extend(self.check_dangerous_mounts(transport))
        issues.extend(self.check_internal_networks(transport))

        return issues

    def check_container_users(self, transport: 'SSHTransport') -> list[SecurityIssue]:
        """
        Check that containers run as non-root users.

        Containers running as root pose a security risk - if an attacker
        gains code execution, they have root privileges and may escape to host.

        Args:
            transport: SSH transport for executing commands

        Returns:
            List of security issues for containers running as root
        """
        issues: list[SecurityIssue] = []

        result = transport.execute(
            "docker ps --format '{{.Names}}' 2>/dev/null"
        )

        if not result.success:
            return issues

        containers = [c.strip() for c in result.stdout.split('\n') if c.strip()]

        for container in containers:
            if any(allowed in container.lower() for allowed in ROOT_ALLOWED_CONTAINERS):
                continue

            user_result = transport.execute(
                f"docker exec {container} whoami 2>/dev/null || "
                f"docker exec {container} id -un 2>/dev/null"
            )

            if not user_result.success:
                continue

            user = user_result.stdout.strip()

            if user == 'root':
                expected = self._get_expected_user(container)

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

    def check_container_security_opts(
        self, transport: 'SSHTransport'
    ) -> list[SecurityIssue]:
        """
        Check container security options (no-new-privileges, cap_drop).

        Verifies that containers have proper security hardening:
        - no-new-privileges prevents privilege escalation via setuid
        - cap_drop: ALL removes unnecessary Linux capabilities

        Args:
            transport: SSH transport for executing commands

        Returns:
            List of security issues for missing security options
        """
        issues: list[SecurityIssue] = []

        result = transport.execute(
            "cat docker-compose.yml docker-compose.yaml 2>/dev/null | head -200"
        )

        if not result.success or not result.stdout.strip():
            return issues

        compose_content = result.stdout

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

    def check_dangerous_mounts(self, transport: 'SSHTransport') -> list[SecurityIssue]:
        """
        Check for dangerous host mounts that expose sensitive data.

        Detects mounts of directories containing credentials:
        - ~/.claude (Claude credentials)
        - ~/.ssh (SSH keys)
        - ~/.aws (AWS credentials)
        - /var/run/docker.sock (container escape)

        Args:
            transport: SSH transport for executing commands

        Returns:
            List of security issues for dangerous mounts
        """
        issues: list[SecurityIssue] = []

        result = transport.execute(
            "docker inspect --format '{{.Name}} {{range .Mounts}}{{.Source}}:{{.Destination}} {{end}}' "
            "$(docker ps -q) 2>/dev/null"
        )

        if not result.success or not result.stdout.strip():
            return issues

        for line in result.stdout.split('\n'):
            if not line.strip():
                continue

            parts = line.strip().split()
            if len(parts) < 2:
                continue

            container_name = parts[0].lstrip('/')
            mounts = parts[1:]

            for mount in mounts:
                if ':' not in mount:
                    continue

                source_path = mount.split(':')[0]

                for dangerous_mount in DANGEROUS_HOST_MOUNTS:
                    if dangerous_mount in source_path:
                        severity = self._get_mount_severity(dangerous_mount)

                        issues.append(SecurityIssue(
                            level=severity,
                            category=SecurityCategory.CONTAINER,
                            title=f'Dangerous host mount in "{container_name}"',
                            description=(
                                f'Container has access to sensitive host path "{source_path}". '
                                f'This could expose credentials, keys, or enable container escape.'
                            ),
                            remediation=(
                                f'1. Remove the volume mount from docker-compose.yml:\n'
                                f'   volumes:\n'
                                f'     - {mount}  # REMOVE THIS\n\n'
                                f'2. If access is required, use secrets management:\n'
                                f'   secrets:\n'
                                f'     my_secret:\n'
                                f'       external: true\n\n'
                                f'3. For Docker socket, use docker:dind or socket proxy'
                            ),
                            evidence=f'Mount: {mount}',
                            cwe_id='CWE-538',
                        ))
                        break

        return issues

    def check_internal_networks(
        self, transport: 'SSHTransport'
    ) -> list[SecurityIssue]:
        """
        Check if backend services use internal: true networks.

        Backend services (databases, caches, internal APIs) should use
        Docker networks with internal: true to prevent external access.

        Args:
            transport: SSH transport for executing commands

        Returns:
            List of security issues for non-internal backend networks
        """
        issues: list[SecurityIssue] = []

        backend_services = {'postgres', 'redis', 'mongodb', 'mysql', 'elasticsearch'}

        result = transport.execute(
            "cat docker-compose.yml docker-compose.yaml 2>/dev/null"
        )

        if not result.success or not result.stdout.strip():
            return issues

        compose_content = result.stdout

        has_backend = any(
            service in compose_content.lower() for service in backend_services
        )

        if not has_backend:
            return issues

        networks_result = transport.execute(
            "docker network ls --format '{{.Name}}' 2>/dev/null"
        )

        if not networks_result.success:
            return issues

        networks = [n.strip() for n in networks_result.stdout.split('\n') if n.strip()]

        for network in networks:
            if network in ('bridge', 'host', 'none'):
                continue

            inspect_result = transport.execute(
                f"docker network inspect {network} --format '{{{{.Internal}}}}' 2>/dev/null"
            )

            if not inspect_result.success:
                continue

            is_internal = inspect_result.stdout.strip().lower() == 'true'

            if not is_internal:
                containers_result = transport.execute(
                    f"docker network inspect {network} "
                    f"--format '{{{{range .Containers}}}}{{{{.Name}}}} {{{{end}}}}' 2>/dev/null"
                )

                if containers_result.success:
                    containers = containers_result.stdout.strip().split()
                    backend_containers = [
                        c for c in containers
                        if any(svc in c.lower() for svc in backend_services)
                    ]

                    if backend_containers:
                        issues.append(SecurityIssue(
                            level=AlertLevel.WARNING,
                            category=SecurityCategory.CONTAINER,
                            title=f'Network "{network}" not marked as internal',
                            description=(
                                f'Backend services ({", ".join(backend_containers)}) are on '
                                f'network "{network}" which is not marked as internal. '
                                f'This may allow external access to backend services.'
                            ),
                            remediation=(
                                f'1. Update docker-compose.yml networks section:\n'
                                f'   networks:\n'
                                f'     {network}:\n'
                                f'       internal: true\n\n'
                                f'2. Ensure only frontend/proxy services have external network access\n\n'
                                f'3. Use separate networks for frontend and backend tiers'
                            ),
                            evidence=f'Backend services: {", ".join(backend_containers)}',
                            cwe_id='CWE-284',
                        ))

        return issues

    def _get_expected_user(self, container: str) -> str:
        """
        Get expected non-root user for a container.

        Args:
            container: Container name

        Returns:
            Expected username or 'non-root' if unknown
        """
        for pattern, expected_user in EXPECTED_CONTAINER_USERS.items():
            if pattern in container.lower():
                return expected_user
        return 'non-root'

    def _get_mount_severity(self, mount_path: str) -> AlertLevel:
        """
        Determine severity level based on the type of dangerous mount.

        Args:
            mount_path: The dangerous mount path pattern

        Returns:
            Alert severity level
        """
        critical_mounts = {'/var/run/docker.sock', '/.ssh', '/.aws', '/.kube'}

        if any(critical in mount_path for critical in critical_mounts):
            return AlertLevel.CRITICAL

        return AlertLevel.WARNING
