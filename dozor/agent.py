"""
Main ServerAgent - orchestrates diagnostics and alerts.
"""

import json
import sys
import os
from typing import Optional

# Add parent directory for shared imports
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from shared.logging import get_safe_logger as get_logger

from .config import ServerConfig, AlertConfig
from .transport import SSHTransport
from .collectors import LogCollector, StatusCollector, ResourceCollector, SecurityCollector
from .analyzers import LogAnalyzer, AlertGenerator
from .analyzers.log_analyzer import is_bot_scanner_request
from .models import DiagnosticReport, ServiceStatus, Alert, LogEntry


logger = get_logger(__name__)


class ServerAgent:
    """
    AI-first server monitoring agent.

    Monitors Docker containers, analyzes logs, and generates
    AI-friendly alerts when issues are detected.
    """

    def __init__(
        self,
        server_config: ServerConfig,
        alert_config: Optional[AlertConfig] = None,
    ):
        self.server_config = server_config
        self.alert_config = alert_config or AlertConfig()

        # Initialize components
        self.transport = SSHTransport(server_config)
        self.log_collector = LogCollector(self.transport)
        self.status_collector = StatusCollector(self.transport)
        self.resource_collector = ResourceCollector(self.transport)
        self.security_collector = SecurityCollector(self.transport)
        self.alert_generator = AlertGenerator(self.alert_config)

        self._connected = False

    def connect(self) -> tuple[bool, str]:
        """Test connection to server."""
        success, message = self.transport.test_connection()
        self._connected = success
        return success, message

    def _filter_error_logs(self, logs: list[LogEntry]) -> list[LogEntry]:
        """Filter logs to only include errors, excluding bot scanner noise."""
        return [
            log for log in logs
            if log.level in ('ERROR', 'CRITICAL', 'FATAL')
            and not is_bot_scanner_request(log.raw)
        ]

    def diagnose(
        self,
        include_security: bool = True,
        services_override: list[str] | None = None,
    ) -> DiagnosticReport:
        """
        Run full diagnostics and return report.

        This is the main entry point for AI agents.

        Args:
            include_security: Also run security checks (default: True)
            services_override: Override default services list (immutable pattern)
        """
        logger.info(f"Running diagnostics on {self.server_config.host}")

        # Collect status for all services (use override if provided)
        target_services = services_override or self.server_config.services
        service_statuses = self._collect_all_data(target_services)

        # Generate report with alerts
        report = self.alert_generator.create_report(
            server=self.server_config.host,
            services=service_statuses,
        )

        # Add security alerts
        if include_security:
            security_alerts = self.check_security()
            report.alerts.extend(security_alerts)
            # Recalculate overall health
            report._recalculate_health()

        logger.info(f"Diagnostics complete: {report.overall_health}")
        return report

    def _collect_all_data(self, services: list[str] | None = None) -> list[ServiceStatus]:
        """Collect all data for services."""
        services = services or self.server_config.services

        # Get container statuses
        statuses = self.status_collector.get_all_statuses(services)

        # Get restart counts
        restart_counts = self.status_collector.get_restart_counts(services)

        # Get resource usage
        resources = self.resource_collector.get_resource_usage(services)

        # Enrich statuses
        for status in statuses:
            # Add restart count
            status.restart_count = restart_counts.get(status.name, 0)

            # Add resources
            res = resources.get(status.name, {})
            status.cpu_percent = res.get('cpu_percent')
            status.memory_mb = res.get('memory_mb')
            status.memory_limit_mb = res.get('memory_limit_mb')

            # Collect logs and errors (exclude bot scanner noise)
            logs = self.log_collector.get_logs(
                status.name,
                lines=self.alert_config.log_lines,
            )
            error_logs = self._filter_error_logs(logs)

            status.error_count = len(error_logs)
            status.recent_errors = error_logs[-10:]  # Keep last 10 errors

        return statuses

    def get_service_status(self, service: str) -> ServiceStatus:
        """Get status for a single service."""
        statuses = self.status_collector.get_all_statuses([service])
        if statuses:
            status = statuses[0]

            # Enrich with logs (exclude bot scanner noise)
            logs = self.log_collector.get_logs(service, lines=50)
            error_logs = self._filter_error_logs(logs)
            status.error_count = len(error_logs)
            status.recent_errors = error_logs[-5:]

            return status

        return ServiceStatus(name=service, state='unknown')

    def get_logs(
        self,
        service: str,
        lines: int = 100,
        errors_only: bool = False,
    ) -> list[LogEntry]:
        """Get logs for a service."""
        if errors_only:
            return self.log_collector.get_error_logs(service, lines)
        return self.log_collector.get_logs(service, lines)

    def restart_service(self, service: str) -> tuple[bool, str]:
        """Restart a service container."""
        logger.info(f"Restarting service: {service}")
        result = self.transport.docker_compose_command(f'restart {service}')
        return result.success, result.output

    def get_service_logs_summary(self, service: str) -> str:
        """Get AI-friendly summary of service logs."""
        logs = self.log_collector.get_logs(service, lines=100)

        analyzer = LogAnalyzer()
        analysis = analyzer.analyze_logs(logs)

        if analysis['errors_found'] == 0:
            return f"Service {service}: No errors in last 100 log lines."

        summary = [f"Service {service} Log Analysis:"]
        summary.append(f"  Total entries: {analysis['total_entries']}")
        summary.append(f"  Errors found: {analysis['errors_found']}")

        if analysis['by_category']:
            summary.append("  By category:")
            for cat, count in analysis['by_category'].items():
                summary.append(f"    - {cat}: {count}")

        if analysis['patterns_matched']:
            summary.append("  Issues detected:")
            for match in analysis['patterns_matched'][:5]:
                p = match['pattern']
                summary.append(f"    [{p.level.value.upper()}] {p.description}")
                summary.append(f"      Action: {p.suggested_action}")

        return "\n".join(summary)

    def to_ai_report(self) -> str:
        """Generate AI-friendly diagnostic report."""
        report = self.diagnose()
        return report.to_ai_prompt()

    def to_json(self) -> str:
        """Generate JSON diagnostic report."""
        report = self.diagnose()
        return json.dumps(report.to_dict(), indent=2, default=str)

    def check_security(self) -> list[Alert]:
        """
        Run security checks and return alerts.

        Checks for:
        - Publicly exposed ports (0.0.0.0)
        - Disabled firewall
        - Bot scanner activity
        """
        logger.info(f"Running security checks on {self.server_config.host}")

        issues = self.security_collector.check_all()

        # Convert security issues to alerts
        alerts = []
        for issue in issues:
            alerts.append(Alert(
                level=issue.level,
                service='server',
                title=f"[SECURITY] {issue.title}",
                description=issue.description,
                suggested_action=issue.remediation,
                context={
                    'category': issue.category,
                    'evidence': issue.evidence,
                },
            ))

        return alerts

    def get_security_report(self) -> str:
        """Get AI-friendly security report."""
        alerts = self.check_security()

        if not alerts:
            return f"Security check passed for {self.server_config.host}. No issues found."

        lines = [
            f"[SECURITY ALERT] {self.server_config.host}",
            f"Found {len(alerts)} security issue(s):",
            "",
        ]

        for alert in alerts:
            lines.append(f"[{alert.level.value.upper()}] {alert.title}")
            lines.append(f"  {alert.description}")
            lines.append(f"  Remediation:")
            for step in alert.suggested_action.split('\n'):
                lines.append(f"    {step}")
            if alert.context.get('evidence'):
                lines.append(f"  Evidence: {alert.context['evidence']}")
            lines.append("")

        return "\n".join(lines)

    @classmethod
    def from_env(cls) -> "ServerAgent":
        """Create agent from environment variables."""
        server_config = ServerConfig.from_env()
        alert_config = AlertConfig.from_env()
        return cls(server_config, alert_config)


# Convenience function for quick diagnostics
def diagnose_server(
    host: Optional[str] = None,
    services: Optional[list[str]] = None,
) -> DiagnosticReport:
    """
    Quick diagnostic function.

    Args:
        host: Server host (uses env if not provided)
        services: Services to check (uses env if not provided)

    Returns:
        DiagnosticReport
    """
    if host:
        config = ServerConfig(
            host=host,
            services=services or [],
        )
    else:
        config = ServerConfig.from_env()
        if services:
            config.services = services

    agent = ServerAgent(config)
    return agent.diagnose()
