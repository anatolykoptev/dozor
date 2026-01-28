"""Pytest fixtures for Dozor tests."""

import pytest
from unittest.mock import MagicMock, patch

from dozor.config import ServerConfig, AlertConfig
from dozor.models import ServiceStatus, ContainerState, LogEntry


@pytest.fixture
def server_config():
    """Create test server configuration."""
    return ServerConfig(
        host="test-server.example.com",
        user="testuser",
        port=22,
        compose_path="~/test-project",
        ssh_key=None,
        timeout=30,
        services=["service1", "service2", "service3"],
    )


@pytest.fixture
def local_config():
    """Create local server configuration."""
    return ServerConfig(
        host="localhost",
        user="testuser",
        port=22,
        compose_path="/tmp/test-project",
        ssh_key=None,
        timeout=30,
        services=["test-service"],
    )


@pytest.fixture
def alert_config():
    """Create test alert configuration."""
    return AlertConfig(
        error_threshold=5,
        restart_threshold=1,
        memory_threshold_percent=90.0,
        cpu_threshold_percent=90.0,
    )


@pytest.fixture
def mock_transport():
    """Create mock SSH transport."""
    transport = MagicMock()
    transport.execute.return_value = MagicMock(
        success=True,
        stdout="OK",
        stderr="",
        return_code=0,
    )
    return transport


@pytest.fixture
def healthy_service_status():
    """Create healthy service status."""
    return ServiceStatus(
        name="healthy-service",
        state=ContainerState.RUNNING,
        health="healthy",
        uptime="2 days",
        restart_count=0,
        cpu_percent=15.5,
        memory_mb=256,
        memory_limit_mb=1024,
        error_count=0,
        recent_errors=[],
    )


@pytest.fixture
def unhealthy_service_status():
    """Create unhealthy service status."""
    return ServiceStatus(
        name="unhealthy-service",
        state=ContainerState.EXITED,
        health="unhealthy",
        uptime=None,
        restart_count=5,
        cpu_percent=None,
        memory_mb=None,
        memory_limit_mb=None,
        error_count=10,
        recent_errors=[
            LogEntry(
                timestamp=None,
                level="ERROR",
                message="Connection refused",
                raw="ERROR: Connection refused",
            ),
        ],
    )


@pytest.fixture
def sample_log_entries():
    """Create sample log entries."""
    from datetime import datetime
    return [
        LogEntry(
            timestamp=datetime(2024, 1, 1, 10, 0, 0),
            level="INFO",
            message="Service started",
            raw="INFO: Service started",
        ),
        LogEntry(
            timestamp=datetime(2024, 1, 1, 10, 5, 0),
            level="ERROR",
            message="Connection timeout",
            raw="ERROR: Connection timeout",
        ),
        LogEntry(
            timestamp=datetime(2024, 1, 1, 10, 10, 0),
            level="WARN",
            message="High memory usage",
            raw="WARN: High memory usage",
        ),
    ]
