"""
Alert generator for AI agents.
"""

from datetime import datetime
from typing import Optional

from ..config import AlertConfig
from ..models import Alert, AlertLevel, ServiceStatus, DiagnosticReport
from .log_analyzer import LogAnalyzer


class AlertGenerator:
    """Generates alerts for AI agents based on service status and logs."""

    def __init__(self, config: Optional[AlertConfig] = None):
        self.config = config or AlertConfig()
        self.log_analyzer = LogAnalyzer()

    def generate_alerts(self, services: list[ServiceStatus]) -> list[Alert]:
        """Generate alerts from service statuses."""
        alerts = []

        for service in services:
            # Check container state
            alerts.extend(self._check_container_state(service))

            # Check restart count
            alerts.extend(self._check_restarts(service))

            # Check resources
            alerts.extend(self._check_resources(service))

            # Analyze logs for patterns
            alerts.extend(self._analyze_service_logs(service))

        return alerts

    def _check_container_state(self, service: ServiceStatus) -> list[Alert]:
        """Check container state and generate alerts."""
        from ..models import ContainerState

        alerts = []

        if service.state == ContainerState.EXITED:
            alerts.append(Alert(
                level=AlertLevel.CRITICAL,
                service=service.name,
                title=f"Container {service.name} has exited",
                description=f"The container for {service.name} is not running. "
                           f"This indicates a crash or intentional stop.",
                suggested_action=f"1. Check logs: docker compose logs {service.name}\n"
                                f"2. Restart: docker compose up -d {service.name}\n"
                                f"3. If persistent, check configuration",
                context={
                    'state': service.state.value,
                    'last_known_uptime': service.uptime,
                },
            ))

        elif service.state == ContainerState.RESTARTING:
            alerts.append(Alert(
                level=AlertLevel.CRITICAL,
                service=service.name,
                title=f"Container {service.name} is in restart loop",
                description=f"The container keeps restarting, indicating a crash loop. "
                           f"Restart count: {service.restart_count}",
                suggested_action=f"1. View logs: docker compose logs --tail 100 {service.name}\n"
                                f"2. Check exit codes: docker inspect {service.name}\n"
                                f"3. Review recent configuration changes",
                context={
                    'state': service.state.value,
                    'restart_count': service.restart_count,
                },
            ))

        elif service.state == ContainerState.DEAD:
            alerts.append(Alert(
                level=AlertLevel.CRITICAL,
                service=service.name,
                title=f"Container {service.name} is dead",
                description="Container is in dead state and requires manual intervention.",
                suggested_action=f"1. Remove and recreate: docker compose up -d --force-recreate {service.name}\n"
                                f"2. Check system resources (disk, memory)",
                context={'state': service.state.value},
            ))

        return alerts

    def _check_restarts(self, service: ServiceStatus) -> list[Alert]:
        """Check restart count and generate alerts."""
        alerts = []

        if service.restart_count >= self.config.restart_threshold:
            alerts.append(Alert(
                level=AlertLevel.ERROR if service.restart_count > 3 else AlertLevel.WARNING,
                service=service.name,
                title=f"Container {service.name} has restarted {service.restart_count} times",
                description=f"Elevated restart count indicates instability. "
                           f"Container may be experiencing intermittent failures.",
                suggested_action=f"1. Analyze logs around restart times\n"
                                f"2. Check resource limits (memory/CPU)\n"
                                f"3. Review healthcheck configuration",
                context={
                    'restart_count': service.restart_count,
                    'current_uptime': service.uptime,
                },
            ))

        return alerts

    def _check_resources(self, service: ServiceStatus) -> list[Alert]:
        """Check resource usage and generate alerts."""
        alerts = []

        # Memory check
        if (service.memory_mb and service.memory_limit_mb and
            service.memory_limit_mb > 0):
            usage_percent = (service.memory_mb / service.memory_limit_mb) * 100

            if usage_percent >= self.config.memory_threshold_percent:
                alerts.append(Alert(
                    level=AlertLevel.WARNING if usage_percent < 95 else AlertLevel.ERROR,
                    service=service.name,
                    title=f"High memory usage on {service.name}: {usage_percent:.1f}%",
                    description=f"Memory usage: {service.memory_mb:.0f}MB / {service.memory_limit_mb:.0f}MB. "
                               f"Container may run out of memory soon.",
                    suggested_action=f"1. Increase memory limit in docker-compose.yml\n"
                                    f"2. Investigate memory leaks\n"
                                    f"3. Restart container to clear memory",
                    context={
                        'memory_mb': service.memory_mb,
                        'memory_limit_mb': service.memory_limit_mb,
                        'usage_percent': usage_percent,
                    },
                ))

        # CPU check
        if service.cpu_percent and service.cpu_percent >= self.config.cpu_threshold_percent:
            alerts.append(Alert(
                level=AlertLevel.WARNING,
                service=service.name,
                title=f"High CPU usage on {service.name}: {service.cpu_percent:.1f}%",
                description="Sustained high CPU may indicate performance issues.",
                suggested_action="1. Check for runaway processes\n"
                                "2. Review recent workload changes\n"
                                "3. Consider horizontal scaling",
                context={'cpu_percent': service.cpu_percent},
            ))

        return alerts

    def _analyze_service_logs(self, service: ServiceStatus) -> list[Alert]:
        """Analyze service logs for known error patterns."""
        alerts = []

        if not service.recent_errors:
            return alerts

        # Analyze errors
        analysis = self.log_analyzer.analyze_logs(service.recent_errors)

        for pattern_match in analysis['patterns_matched']:
            pattern = pattern_match['pattern']
            count = pattern_match['count']
            samples = pattern_match['sample_entries']

            alerts.append(Alert(
                level=pattern.level,
                service=service.name,
                title=f"[{pattern.category.upper()}] {pattern.description}",
                description=f"Detected {count} occurrence(s) of this error pattern in recent logs.",
                suggested_action=pattern.suggested_action,
                context={
                    'category': pattern.category,
                    'occurrence_count': count,
                    'sample_messages': [e.message for e in samples],
                },
            ))

        # Add generic error count alert if many errors without specific patterns
        unmatched_errors = service.error_count - analysis['errors_found']
        if unmatched_errors >= self.config.error_threshold:
            alerts.append(Alert(
                level=AlertLevel.WARNING,
                service=service.name,
                title=f"Multiple unclassified errors in {service.name}",
                description=f"Found {unmatched_errors} errors that don't match known patterns.",
                suggested_action=f"Review logs manually: docker compose logs --tail 100 {service.name}",
                context={
                    'error_count': unmatched_errors,
                    'sample_errors': [e.message for e in service.recent_errors[:3]],
                },
            ))

        return alerts

    def create_report(
        self,
        server: str,
        services: list[ServiceStatus],
    ) -> DiagnosticReport:
        """Create a full diagnostic report with alerts."""
        alerts = self.generate_alerts(services)

        report = DiagnosticReport(
            timestamp=datetime.now(),
            server=server,
            services=services,
            alerts=alerts,
        )

        return report
