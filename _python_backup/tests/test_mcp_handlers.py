"""Tests for MCP handlers module."""

import pytest
from unittest.mock import MagicMock, patch
import re

from dozor.mcp_handlers import (
    get_agent,
    reset_agent,
    handle_server_diagnose,
    handle_server_status,
    handle_server_logs,
    handle_server_restart,
    handle_server_exec,
    handle_server_analyze_logs,
    handle_server_health,
    handle_server_prune,
    handle_server_deploy,
    handle_server_deploy_status,
    handle_tool,
    HANDLERS,
)


class TestGetAgent:
    """Tests for agent singleton."""

    def teardown_method(self):
        """Reset agent after each test."""
        reset_agent()

    @patch("dozor.mcp_handlers.ServerAgent")
    def test_get_agent_creates_singleton(self, mock_agent_class):
        """Test that get_agent returns same instance."""
        mock_instance = MagicMock()
        mock_agent_class.from_env.return_value = mock_instance

        agent1 = get_agent()
        agent2 = get_agent()

        assert agent1 is agent2
        mock_agent_class.from_env.assert_called_once()

    @patch("dozor.mcp_handlers.ServerAgent")
    def test_reset_agent_clears_cache(self, mock_agent_class):
        """Test that reset_agent allows new instance."""
        mock_agent_class.from_env.return_value = MagicMock()

        agent1 = get_agent()
        reset_agent()
        agent2 = get_agent()

        assert mock_agent_class.from_env.call_count == 2


class TestHandleServerDiagnose:
    """Tests for handle_server_diagnose."""

    def teardown_method(self):
        reset_agent()

    @patch("dozor.mcp_handlers.get_agent")
    def test_returns_healthy_message_when_ok(self, mock_get_agent):
        """Test healthy response."""
        mock_agent = MagicMock()
        mock_report = MagicMock()
        mock_report.needs_attention = False
        mock_report.server = "test-server"
        mock_report.services = ["s1", "s2"]
        mock_agent.diagnose.return_value = mock_report
        mock_get_agent.return_value = mock_agent

        result = handle_server_diagnose({})

        assert "healthy" in result.lower()
        assert "test-server" in result

    @patch("dozor.mcp_handlers.get_agent")
    def test_passes_services_override(self, mock_get_agent):
        """Test that services_override is passed to diagnose."""
        mock_agent = MagicMock()
        mock_report = MagicMock()
        mock_report.needs_attention = False
        mock_report.server = "test"
        mock_report.services = []
        mock_agent.diagnose.return_value = mock_report
        mock_get_agent.return_value = mock_agent

        handle_server_diagnose({"services": ["nginx", "postgres"]})

        mock_agent.diagnose.assert_called_with(services_override=["nginx", "postgres"])


class TestHandleServerStatus:
    """Tests for handle_server_status."""

    def teardown_method(self):
        reset_agent()

    @patch("dozor.mcp_handlers.get_agent")
    def test_returns_service_info(self, mock_get_agent):
        """Test that service info is returned."""
        mock_agent = MagicMock()
        mock_status = MagicMock()
        mock_status.name = "nginx"
        mock_status.state.value = "running"
        mock_status.health = "healthy"
        mock_status.uptime = "2 days"
        mock_status.restart_count = 0
        mock_status.cpu_percent = 10.5
        mock_status.memory_mb = 256
        mock_status.memory_limit_mb = 1024
        mock_status.error_count = 0
        mock_status.recent_errors = []
        mock_agent.get_service_status.return_value = mock_status
        mock_get_agent.return_value = mock_agent

        result = handle_server_status({"service": "nginx"})

        assert "nginx" in result
        assert "running" in result
        assert "healthy" in result

    def test_missing_service_returns_error(self):
        """Test that missing service returns error."""
        result = handle_server_status({})

        assert "required" in result.lower()

    def test_invalid_service_returns_error(self):
        """Test that invalid service name returns error."""
        result = handle_server_status({"service": "123invalid"})

        assert "invalid" in result.lower()


class TestHandleServerLogs:
    """Tests for handle_server_logs."""

    def teardown_method(self):
        reset_agent()

    @patch("dozor.mcp_handlers.get_agent")
    def test_returns_logs(self, mock_get_agent):
        """Test that logs are returned."""
        mock_agent = MagicMock()
        mock_entry = MagicMock()
        mock_entry.timestamp = None
        mock_entry.level = "INFO"
        mock_entry.message = "Test log message"
        mock_agent.get_logs.return_value = [mock_entry]
        mock_get_agent.return_value = mock_agent

        result = handle_server_logs({"service": "nginx"})

        assert "Test log message" in result

    def test_invalid_lines_count_rejected(self):
        """Test that invalid lines count is rejected."""
        result = handle_server_logs({"service": "nginx", "lines": -1})

        assert "invalid" in result.lower()

    def test_large_lines_count_rejected(self):
        """Test that too large lines count is rejected."""
        result = handle_server_logs({"service": "nginx", "lines": 100000})

        assert "invalid" in result.lower()


class TestHandleServerExec:
    """Tests for handle_server_exec."""

    def teardown_method(self):
        reset_agent()

    @patch("dozor.mcp_handlers.get_agent")
    def test_allowed_command_executed(self, mock_get_agent):
        """Test that allowed commands are executed."""
        mock_agent = MagicMock()
        mock_result = MagicMock()
        mock_result.success = True
        mock_result.stdout = "output"
        mock_agent.transport.execute.return_value = mock_result
        mock_get_agent.return_value = mock_agent

        result = handle_server_exec({"command": "docker ps"})

        assert "output" in result
        mock_agent.transport.execute.assert_called_once()

    def test_blocked_command_rejected(self):
        """Test that blocked commands are rejected."""
        result = handle_server_exec({"command": "rm -rf /"})

        assert "blocked" in result.lower()

    def test_unknown_command_rejected(self):
        """Test that unknown commands are rejected."""
        result = handle_server_exec({"command": "custom_script.sh"})

        assert "blocked" in result.lower() or "not in allowlist" in result.lower()


class TestHandleServerPrune:
    """Tests for handle_server_prune."""

    def teardown_method(self):
        reset_agent()

    @patch("dozor.mcp_handlers.get_agent")
    def test_valid_age_format(self, mock_get_agent):
        """Test that valid age formats work."""
        mock_agent = MagicMock()
        mock_agent.transport.execute.return_value = MagicMock(success=True, stdout="OK")
        mock_get_agent.return_value = mock_agent

        result = handle_server_prune({"age": "24h"})

        assert "failed" not in result.lower() or "invalid" not in result.lower()

    def test_invalid_age_format_rejected(self):
        """Test that invalid age formats are rejected."""
        invalid_ages = [
            "24",
            "24hours",
            "1 h",
            "-24h",
            "24h; rm -rf /",
            "$(whoami)",
        ]

        for age in invalid_ages:
            result = handle_server_prune({"age": age})
            assert "invalid" in result.lower(), f"Should reject age: {age}"


class TestHandleServerDeployStatus:
    """Tests for handle_server_deploy_status."""

    def teardown_method(self):
        reset_agent()

    def test_missing_deploy_id_returns_error(self):
        """Test that missing deploy_id returns error."""
        result = handle_server_deploy_status({})

        assert "required" in result.lower()

    def test_invalid_deploy_id_format_rejected(self):
        """Test that invalid deploy_id formats are rejected."""
        invalid_ids = [
            "invalid",
            "deploy-",
            "deploy-abc",
            "deploy-123",  # Too short
            "deploy-12345678901234567890",  # Too long
            "deploy-1234567890; rm -rf /",
            "../../../etc/passwd",
        ]

        for deploy_id in invalid_ids:
            result = handle_server_deploy_status({"deploy_id": deploy_id})
            assert "invalid" in result.lower(), f"Should reject: {deploy_id}"

    @patch("dozor.mcp_handlers.get_agent")
    @patch("dozor.mcp_handlers.get_deploy_status")
    def test_valid_deploy_id_accepted(self, mock_get_status, mock_get_agent):
        """Test that valid deploy_id works."""
        mock_status = MagicMock()
        mock_status.status = "COMPLETED"
        mock_status.process_running = False
        mock_status.log_file = "/tmp/test.log"
        mock_status.log_content = "Done"
        mock_status.file_info = "file info"
        mock_get_status.return_value = mock_status
        mock_get_agent.return_value = MagicMock()

        result = handle_server_deploy_status({"deploy_id": "deploy-1234567890"})

        assert "invalid" not in result.lower()


class TestHandleTool:
    """Tests for handle_tool dispatcher."""

    def test_unknown_tool_returns_error(self):
        """Test that unknown tool names return error."""
        result = handle_tool("nonexistent_tool", {})

        assert "unknown" in result.lower()

    def test_all_handlers_registered(self):
        """Test that all handlers are registered."""
        expected_handlers = [
            "server_diagnose",
            "server_status",
            "server_logs",
            "server_restart",
            "server_exec",
            "server_analyze_logs",
            "server_health",
            "server_security",
            "server_prune",
            "server_deploy",
            "server_deploy_status",
        ]

        for name in expected_handlers:
            assert name in HANDLERS, f"Handler missing: {name}"

    @patch("dozor.mcp_handlers.get_agent")
    def test_exception_caught_and_logged(self, mock_get_agent):
        """Test that exceptions are caught and logged."""
        mock_get_agent.side_effect = Exception("Test error")

        result = handle_tool("server_diagnose", {})

        assert "error" in result.lower()
        assert "Test error" in result
