"""
Configuration for server agent.
"""

import os
from dataclasses import dataclass, field
from pathlib import Path
from typing import Optional

from dotenv import load_dotenv


@dataclass
class ServerConfig:
    """Server connection configuration."""
    host: str
    user: str = "ubuntu"
    port: int = 22
    compose_path: str = "~/docker-project"  # Override with SERVER_COMPOSE_PATH
    ssh_key: Optional[str] = None
    timeout: int = 30
    services: list[str] = field(default_factory=list)

    @classmethod
    def from_env(cls, env_file: Optional[Path] = None) -> "ServerConfig":
        """Load configuration from environment variables."""
        if env_file:
            load_dotenv(env_file)
        else:
            # Try to find .env in parent directory
            load_dotenv(Path(__file__).parent.parent / ".env")

        host = os.getenv("SERVER_HOST")
        if not host:
            raise ValueError("SERVER_HOST environment variable is required")

        services_str = os.getenv("SERVER_SERVICES", "")
        services = [s.strip() for s in services_str.split(",") if s.strip()]

        compose_path = os.getenv("SERVER_COMPOSE_PATH")
        if not compose_path:
            raise ValueError("SERVER_COMPOSE_PATH environment variable is required")

        if not services:
            raise ValueError(
                "SERVER_SERVICES environment variable is required. "
                "Example: SERVER_SERVICES=nginx,postgres,redis"
            )

        return cls(
            host=host,
            user=os.getenv("SERVER_USER", "ubuntu"),
            port=int(os.getenv("SERVER_PORT", "22")),
            compose_path=compose_path,
            ssh_key=os.getenv("SERVER_SSH_KEY"),
            timeout=int(os.getenv("SERVER_TIMEOUT", "30")),
            services=services,
        )

    def validate(self) -> list[str]:
        """Validate configuration and return list of errors."""
        errors = []
        if not self.host:
            errors.append("host is required")
        if not self.user:
            errors.append("user is required")
        if self.port < 1 or self.port > 65535:
            errors.append("port must be between 1 and 65535")
        if self.timeout < 1:
            errors.append("timeout must be positive")
        return errors


@dataclass
class AlertConfig:
    """Configuration for alerting behavior."""
    # Thresholds
    error_threshold: int = 5          # Errors in logs to trigger warning
    restart_threshold: int = 1        # Restarts to trigger error
    memory_threshold_percent: float = 90.0  # Memory usage warning
    cpu_threshold_percent: float = 90.0     # CPU usage warning

    # Alert destinations
    mcp_notify: bool = True           # Notify via MCP
    webhook_url: Optional[str] = None  # Optional webhook for alerts

    # Polling
    poll_interval_seconds: int = 60   # How often to check (for daemon mode)
    log_lines: int = 100              # Number of log lines to analyze

    @classmethod
    def from_env(cls) -> "AlertConfig":
        return cls(
            error_threshold=int(os.getenv("ALERT_ERROR_THRESHOLD", "5")),
            restart_threshold=int(os.getenv("ALERT_RESTART_THRESHOLD", "1")),
            memory_threshold_percent=float(os.getenv("ALERT_MEMORY_THRESHOLD", "90.0")),
            cpu_threshold_percent=float(os.getenv("ALERT_CPU_THRESHOLD", "90.0")),
            webhook_url=os.getenv("ALERT_WEBHOOK_URL"),
            poll_interval_seconds=int(os.getenv("ALERT_POLL_INTERVAL", "60")),
            log_lines=int(os.getenv("ALERT_LOG_LINES", "100")),
        )
