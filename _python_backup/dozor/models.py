"""
Type definitions for server diagnostics.
"""

from dataclasses import dataclass, field
from datetime import datetime
from enum import Enum
from typing import Optional


class AlertLevel(str, Enum):
    """Alert severity levels for AI agents."""
    CRITICAL = "critical"  # Immediate action required
    ERROR = "error"        # Service degraded
    WARNING = "warning"    # Potential issue
    INFO = "info"          # Informational


class ContainerState(str, Enum):
    """Docker container states."""
    RUNNING = "running"
    EXITED = "exited"
    RESTARTING = "restarting"
    PAUSED = "paused"
    DEAD = "dead"
    UNKNOWN = "unknown"


@dataclass
class LogEntry:
    """Parsed log entry."""
    timestamp: Optional[datetime]
    level: str
    message: str
    service: str
    raw: str

    def to_dict(self) -> dict:
        return {
            "timestamp": self.timestamp.isoformat() if self.timestamp else None,
            "level": self.level,
            "message": self.message,
            "service": self.service,
        }


@dataclass
class ServiceStatus:
    """Status of a single service/container."""
    name: str
    state: ContainerState
    health: Optional[str] = None
    uptime: Optional[str] = None
    restart_count: int = 0
    cpu_percent: Optional[float] = None
    memory_mb: Optional[float] = None
    memory_limit_mb: Optional[float] = None
    error_count: int = 0
    recent_errors: list[LogEntry] = field(default_factory=list)

    @property
    def is_healthy(self) -> bool:
        return (
            self.state == ContainerState.RUNNING
            and self.restart_count == 0
            and self.error_count == 0
        )

    @property
    def alert_level(self) -> AlertLevel:
        if self.state != ContainerState.RUNNING:
            return AlertLevel.CRITICAL
        if self.restart_count > 0 or self.error_count > 5:
            return AlertLevel.ERROR
        if self.error_count > 0:
            return AlertLevel.WARNING
        return AlertLevel.INFO

    def to_dict(self) -> dict:
        return {
            "name": self.name,
            "state": self.state.value,
            "health": self.health,
            "uptime": self.uptime,
            "restart_count": self.restart_count,
            "cpu_percent": self.cpu_percent,
            "memory_mb": self.memory_mb,
            "memory_limit_mb": self.memory_limit_mb,
            "error_count": self.error_count,
            "recent_errors": [e.to_dict() for e in self.recent_errors],
            "is_healthy": self.is_healthy,
            "alert_level": self.alert_level.value,
        }


@dataclass
class Alert:
    """Alert for AI agent."""
    level: AlertLevel
    service: str
    title: str
    description: str
    suggested_action: str
    context: dict = field(default_factory=dict)
    timestamp: datetime = field(default_factory=datetime.now)

    def to_dict(self) -> dict:
        return {
            "level": self.level.value,
            "service": self.service,
            "title": self.title,
            "description": self.description,
            "suggested_action": self.suggested_action,
            "context": self.context,
            "timestamp": self.timestamp.isoformat(),
        }

    def to_ai_prompt(self) -> str:
        """Format alert as prompt for AI agent."""
        return f"""[{self.level.value.upper()}] {self.title}

Service: {self.service}
Description: {self.description}

Suggested Action: {self.suggested_action}

Context:
{self._format_context()}
"""

    def _format_context(self) -> str:
        lines = []
        for key, value in self.context.items():
            if isinstance(value, list):
                lines.append(f"  {key}:")
                for item in value[:5]:  # Limit to 5 items
                    lines.append(f"    - {item}")
            else:
                lines.append(f"  {key}: {value}")
        return "\n".join(lines)


@dataclass
class DiagnosticReport:
    """Complete diagnostic report."""
    timestamp: datetime
    server: str
    services: list[ServiceStatus]
    alerts: list[Alert] = field(default_factory=list)
    overall_health: str = "unknown"

    def __post_init__(self):
        self._calculate_overall_health()

    def _calculate_overall_health(self):
        """Calculate health based on services and alerts."""
        if not self.services and not self.alerts:
            self.overall_health = "unknown"
            return

        # Check services
        service_critical = any(s.alert_level == AlertLevel.CRITICAL for s in self.services)
        service_errors = any(s.alert_level == AlertLevel.ERROR for s in self.services)
        service_warnings = any(s.alert_level == AlertLevel.WARNING for s in self.services)

        # Check alerts (including security alerts)
        alert_critical = any(a.level == AlertLevel.CRITICAL for a in self.alerts)
        alert_errors = any(a.level == AlertLevel.ERROR for a in self.alerts)
        alert_warnings = any(a.level == AlertLevel.WARNING for a in self.alerts)

        if service_critical or alert_critical:
            self.overall_health = "critical"
        elif service_errors or alert_errors:
            self.overall_health = "degraded"
        elif service_warnings or alert_warnings:
            self.overall_health = "warning"
        else:
            self.overall_health = "healthy"

    def _recalculate_health(self):
        """Recalculate health after alerts are modified."""
        self._calculate_overall_health()

    @property
    def needs_attention(self) -> bool:
        return self.overall_health in ("critical", "degraded")

    @property
    def critical_alerts(self) -> list[Alert]:
        return [a for a in self.alerts if a.level == AlertLevel.CRITICAL]

    def to_dict(self) -> dict:
        return {
            "timestamp": self.timestamp.isoformat(),
            "server": self.server,
            "overall_health": self.overall_health,
            "needs_attention": self.needs_attention,
            "services": [s.to_dict() for s in self.services],
            "alerts": [a.to_dict() for a in self.alerts],
            "summary": self._generate_summary(),
        }

    def _generate_summary(self) -> dict:
        return {
            "total_services": len(self.services),
            "healthy": sum(1 for s in self.services if s.is_healthy),
            "unhealthy": sum(1 for s in self.services if not s.is_healthy),
            "total_errors": sum(s.error_count for s in self.services),
            "critical_alerts": len(self.critical_alerts),
        }

    def to_ai_prompt(self) -> str:
        """Format report as prompt for AI agent."""
        # Check if there are any issues (services or alerts)
        unhealthy_services = [s for s in self.services if not s.is_healthy]
        has_issues = unhealthy_services or self.alerts

        if not has_issues:
            return f"Server {self.server} is healthy. All {len(self.services)} services running normally."

        lines = [
            f"[SERVER DIAGNOSTIC ALERT] {self.server}",
            f"Overall Status: {self.overall_health.upper()}",
            "",
        ]

        # Show service issues
        if unhealthy_services:
            lines.append("Services requiring attention:")
            for service in unhealthy_services:
                lines.append(f"  - {service.name}: {service.state.value} "
                           f"(errors: {service.error_count}, restarts: {service.restart_count})")
            lines.append("")

        # Show all alerts (including security)
        if self.alerts:
            # Group alerts by category
            security_alerts = [a for a in self.alerts if '[SECURITY]' in a.title]
            other_alerts = [a for a in self.alerts if '[SECURITY]' not in a.title]

            if security_alerts:
                lines.append("Security Issues:")
                for alert in security_alerts:
                    lines.append(f"  [{alert.level.value.upper()}] {alert.title}")
                    lines.append(f"    {alert.description[:100]}...")
                    lines.append(f"    Remediation: {alert.suggested_action.split(chr(10))[0]}")
                lines.append("")

            if other_alerts:
                lines.append("Service Alerts:")
                for alert in other_alerts:
                    lines.append(f"  [{alert.level.value.upper()}] {alert.title}")

        return "\n".join(lines)
