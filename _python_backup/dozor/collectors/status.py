"""
Container status collector.
"""

import json
import re
from typing import Optional

from ..transport import SSHTransport
from ..models import ContainerState, ServiceStatus


class StatusCollector:
    """Collects container status information."""

    def __init__(self, transport: SSHTransport):
        self.transport = transport

    def get_container_status(self, service_name: str) -> Optional[ServiceStatus]:
        """Get status for a specific container by service name."""
        # Use docker ps with filter to get container info
        result = self.transport.docker_command(
            f'ps --format json --filter name={service_name}'
        )

        if not result.success:
            return ServiceStatus(
                name=service_name,
                state=ContainerState.UNKNOWN,
            )

        try:
            # Parse JSON output (docker ps --format json returns one JSON per line)
            for line in result.stdout.strip().split('\n'):
                if not line.strip():
                    continue
                data = json.loads(line)
                # Verify exact name match (filter is substring match)
                if data.get('Names', '') == service_name:
                    return self._parse_container_data(service_name, data)
        except json.JSONDecodeError:
            pass

        return ServiceStatus(
            name=service_name,
            state=ContainerState.EXITED,
        )

    def get_all_statuses(self, services: list[str]) -> list[ServiceStatus]:
        """Get status for all specified services."""
        statuses = []

        # Use docker ps directly (works without cd, more reliable)
        result = self.transport.docker_command('ps --format json')

        if not result.success:
            # Return unknown status for all
            return [
                ServiceStatus(name=s, state=ContainerState.UNKNOWN)
                for s in services
            ]

        # Parse all container data
        container_data = {}
        for line in result.stdout.strip().split('\n'):
            if not line.strip():
                continue
            try:
                data = json.loads(line)
                # docker ps uses 'Names', docker compose uses 'Service'
                name = data.get('Names', data.get('Service', data.get('Name', '')))
                container_data[name] = data
            except json.JSONDecodeError:
                continue

        # Map to requested services
        for service in services:
            if service in container_data:
                statuses.append(
                    self._parse_container_data(service, container_data[service])
                )
            else:
                # Service not found in running containers
                statuses.append(ServiceStatus(
                    name=service,
                    state=ContainerState.EXITED,
                ))

        return statuses

    def _parse_container_data(self, service: str, data: dict) -> ServiceStatus:
        """Parse docker ps JSON output into ServiceStatus."""
        # docker ps uses 'State' field with values like 'running'
        state_str = data.get('State', '').lower()

        # Determine container state
        if state_str == 'running':
            state = ContainerState.RUNNING
        elif state_str == 'exited':
            state = ContainerState.EXITED
        elif state_str == 'restarting':
            state = ContainerState.RESTARTING
        elif state_str == 'paused':
            state = ContainerState.PAUSED
        elif state_str == 'dead':
            state = ContainerState.DEAD
        else:
            state = ContainerState.UNKNOWN

        # Extract health from Status field (e.g., "Up About an hour (healthy)")
        status = data.get('Status', '')
        health = None
        if '(healthy)' in status:
            health = 'healthy'
        elif '(unhealthy)' in status:
            health = 'unhealthy'
        elif '(starting)' in status:
            health = 'starting'

        # Extract uptime from Status field (e.g., "Up 2 hours")
        uptime = None
        uptime_match = re.search(r'Up\s+(.+?)(?:\s+\(|$)', status)
        if uptime_match:
            uptime = uptime_match.group(1).strip()

        return ServiceStatus(
            name=service,
            state=state,
            health=health,
            uptime=uptime,
        )

    def get_restart_counts(self, services: list[str]) -> dict[str, int]:
        """Get restart counts for containers."""
        restart_counts = {}

        for service in services:
            # Use docker ps to get container ID by name
            result = self.transport.docker_command(
                f'ps -q --filter name=^{service}$'
            )
            if not result.success or not result.stdout:
                restart_counts[service] = 0
                continue

            container_id = result.stdout.strip().split('\n')[0]

            # Get restart count from docker inspect
            inspect_result = self.transport.docker_command(
                f'inspect --format "{{{{.RestartCount}}}}" {container_id}'
            )

            if inspect_result.success:
                try:
                    restart_counts[service] = int(inspect_result.stdout)
                except ValueError:
                    restart_counts[service] = 0
            else:
                restart_counts[service] = 0

        return restart_counts
