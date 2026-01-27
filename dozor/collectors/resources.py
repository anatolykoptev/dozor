"""
Container resource usage collector.
"""

import re
from typing import Optional

from ..transport import SSHTransport


class ResourceCollector:
    """Collects resource usage (CPU, memory) for containers."""

    def __init__(self, transport: SSHTransport):
        self.transport = transport

    def get_resource_usage(self, services: list[str]) -> dict[str, dict]:
        """
        Get resource usage for specified services.

        Returns dict with service name as key and resource data as value:
        {
            "service": {
                "cpu_percent": 5.2,
                "memory_mb": 256.0,
                "memory_limit_mb": 512.0,
                "memory_percent": 50.0,
                "net_io": "1.2MB / 500KB",
                "block_io": "10MB / 5MB",
            }
        }
        """
        # Use docker stats with no-stream for single snapshot
        result = self.transport.docker_command(
            'stats --no-stream --format "{{.Name}},{{.CPUPerc}},{{.MemUsage}},{{.NetIO}},{{.BlockIO}}"'
        )

        if not result.success:
            return {s: {} for s in services}

        # Parse stats output
        stats_data = {}
        for line in result.stdout.strip().split('\n'):
            if not line.strip():
                continue

            parts = line.split(',')
            if len(parts) < 5:
                continue

            name = parts[0]
            stats_data[name] = {
                'cpu_percent': self._parse_percent(parts[1]),
                **self._parse_memory(parts[2]),
                'net_io': parts[3],
                'block_io': parts[4],
            }

        # Map container names to service names
        result_map = {}
        for service in services:
            # Docker compose names containers as: project_service_1
            # Try to find matching container
            for container_name, data in stats_data.items():
                if service in container_name.lower():
                    result_map[service] = data
                    break
            else:
                result_map[service] = {}

        return result_map

    def _parse_percent(self, value: str) -> Optional[float]:
        """Parse percentage string like '5.23%' to float."""
        try:
            return float(value.rstrip('%'))
        except (ValueError, AttributeError):
            return None

    def _parse_memory(self, mem_str: str) -> dict:
        """
        Parse memory usage string like '256MiB / 512MiB'.

        Returns:
            {
                'memory_mb': 256.0,
                'memory_limit_mb': 512.0,
                'memory_percent': 50.0
            }
        """
        result = {
            'memory_mb': None,
            'memory_limit_mb': None,
            'memory_percent': None,
        }

        try:
            # Split usage / limit
            parts = mem_str.split('/')
            if len(parts) != 2:
                return result

            usage = self._parse_memory_value(parts[0].strip())
            limit = self._parse_memory_value(parts[1].strip())

            result['memory_mb'] = usage
            result['memory_limit_mb'] = limit

            if usage is not None and limit is not None and limit > 0:
                result['memory_percent'] = round((usage / limit) * 100, 2)

        except Exception:
            pass

        return result

    def _parse_memory_value(self, value: str) -> Optional[float]:
        """Convert memory string like '256MiB' or '1.5GiB' to MB."""
        match = re.match(r'([\d.]+)\s*([KMGT]i?B)?', value, re.IGNORECASE)
        if not match:
            return None

        try:
            num = float(match.group(1))
            unit = (match.group(2) or 'B').upper()

            # Convert to MB
            multipliers = {
                'B': 1 / (1024 * 1024),
                'KB': 1 / 1024,
                'KIB': 1 / 1024,
                'MB': 1,
                'MIB': 1,
                'GB': 1024,
                'GIB': 1024,
                'TB': 1024 * 1024,
                'TIB': 1024 * 1024,
            }

            return round(num * multipliers.get(unit, 1), 2)
        except (ValueError, KeyError):
            return None

    def get_disk_usage(self) -> dict:
        """Get disk usage on the server."""
        result = self.transport.execute('df -h / | tail -1')

        if not result.success:
            return {}

        # Parse df output: Filesystem  Size  Used  Avail  Use%  Mounted
        parts = result.stdout.split()
        if len(parts) >= 5:
            return {
                'total': parts[1],
                'used': parts[2],
                'available': parts[3],
                'percent': self._parse_percent(parts[4]),
            }

        return {}

    def get_system_load(self) -> dict:
        """Get system load averages."""
        result = self.transport.execute('cat /proc/loadavg')

        if not result.success:
            return {}

        parts = result.stdout.split()
        if len(parts) >= 3:
            return {
                'load_1m': float(parts[0]),
                'load_5m': float(parts[1]),
                'load_15m': float(parts[2]),
            }

        return {}
