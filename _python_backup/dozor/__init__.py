"""
Server Agent - AI-first server monitoring and diagnostics.

This agent monitors Docker containers, analyzes logs, and automatically
alerts AI agents when issues are detected.
"""

from .agent import ServerAgent
from .config import ServerConfig
from .models import DiagnosticReport, ServiceStatus, LogEntry, AlertLevel

__all__ = [
    "ServerAgent",
    "ServerConfig",
    "DiagnosticReport",
    "ServiceStatus",
    "LogEntry",
    "AlertLevel",
]
