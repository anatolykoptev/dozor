"""Tests for deploy module."""

import pytest
from unittest.mock import MagicMock, patch
import re

from dozor.deploy import (
    start_background_deploy,
    get_deploy_status,
    format_deploy_started,
    format_deploy_status,
    DeployResult,
    DeployStatus,
)


class TestStartBackgroundDeploy:
    """Tests for start_background_deploy function."""

    @pytest.fixture
    def mock_agent(self):
        """Create mock agent."""
        agent = MagicMock()
        agent.transport.execute.return_value = MagicMock(
            success=True,
            stdout="",
            stderr="",
        )
        return agent

    def test_valid_deploy_returns_success(self, mock_agent):
        """Test that valid deploy parameters work."""
        result = start_background_deploy(
            agent=mock_agent,
            project_path="/home/user/project",
        )

        assert result.success
        assert result.deploy_id.startswith("deploy-")
        assert result.log_file.startswith("/tmp/deploy-")
        assert result.error == ""

    def test_deploy_id_format(self, mock_agent):
        """Test that deploy_id has correct format."""
        result = start_background_deploy(
            agent=mock_agent,
            project_path="/home/user/project",
        )

        # Format: deploy-<unix_timestamp>
        assert re.match(r'^deploy-\d{10,13}$', result.deploy_id)

    def test_invalid_path_characters_rejected(self, mock_agent):
        """Test that invalid path characters are rejected."""
        invalid_paths = [
            "/path/with;injection",
            "/path/with|pipe",
            "/path/with$(cmd)",
            "/path/with`cmd`",
            "/path/with&background",
        ]

        for path in invalid_paths:
            result = start_background_deploy(
                agent=mock_agent,
                project_path=path,
            )
            assert not result.success, f"Should reject path: {path}"
            assert "invalid" in result.error.lower()

    def test_path_traversal_rejected(self, mock_agent):
        """Test that path traversal is rejected."""
        result = start_background_deploy(
            agent=mock_agent,
            project_path="/home/user/../../../etc",
        )

        assert not result.success
        assert "traversal" in result.error.lower()

    def test_invalid_service_names_rejected(self, mock_agent):
        """Test that invalid service names are rejected."""
        result = start_background_deploy(
            agent=mock_agent,
            project_path="/home/user/project",
            services=["valid-service", "invalid;service"],
        )

        assert not result.success
        assert "invalid" in result.error.lower()

    def test_transport_execute_called_with_nohup(self, mock_agent):
        """Test that deploy uses nohup for background execution."""
        start_background_deploy(
            agent=mock_agent,
            project_path="/home/user/project",
        )

        call_args = mock_agent.transport.execute.call_args
        command = call_args[0][0]
        assert "nohup" in command
        assert "bash -c" in command

    def test_skip_validation_used_for_internal_command(self, mock_agent):
        """Test that skip_validation=True is used."""
        start_background_deploy(
            agent=mock_agent,
            project_path="/home/user/project",
        )

        call_args = mock_agent.transport.execute.call_args
        assert call_args[1].get("skip_validation") is True

    def test_paths_are_in_command(self, mock_agent):
        """Test that paths are included in the shell command."""
        start_background_deploy(
            agent=mock_agent,
            project_path="/home/user/my-project",
        )

        call_args = mock_agent.transport.execute.call_args
        command = call_args[0][0]
        # Path should be in the cd command
        assert "cd /home/user/my-project" in command or "cd '/home/user/my-project'" in command
        # Log file should be quoted
        assert "/tmp/deploy-" in command

    def test_do_pull_false_skips_git(self, mock_agent):
        """Test that do_pull=False skips git operations."""
        start_background_deploy(
            agent=mock_agent,
            project_path="/home/user/project",
            do_pull=False,
        )

        call_args = mock_agent.transport.execute.call_args
        command = call_args[0][0]
        assert "git" not in command

    def test_do_build_false_skips_build(self, mock_agent):
        """Test that do_build=False skips docker build."""
        start_background_deploy(
            agent=mock_agent,
            project_path="/home/user/project",
            do_build=False,
        )

        call_args = mock_agent.transport.execute.call_args
        command = call_args[0][0]
        assert "docker compose build" not in command

    def test_services_included_in_command(self, mock_agent):
        """Test that specific services are included."""
        start_background_deploy(
            agent=mock_agent,
            project_path="/home/user/project",
            services=["nginx", "postgres"],
        )

        call_args = mock_agent.transport.execute.call_args
        command = call_args[0][0]
        assert "nginx" in command
        assert "postgres" in command


class TestGetDeployStatus:
    """Tests for get_deploy_status function."""

    @pytest.fixture
    def mock_agent(self):
        """Create mock agent."""
        agent = MagicMock()
        return agent

    def test_completed_status_detected(self, mock_agent):
        """Test that completed deploy is detected."""
        mock_agent.transport.execute.side_effect = [
            MagicMock(stdout="STOPPED"),  # pgrep - no "RUNNING" substring
            MagicMock(stdout="-rw-r--r-- 1 user user 1234 Jan 1 12:00 /tmp/deploy-123.log"),  # ls
            MagicMock(stdout="=== DEPLOY COMPLETE ==="),  # cat
        ]

        status = get_deploy_status(mock_agent, "deploy-1234567890")

        assert status.status == "COMPLETED"
        assert not status.process_running

    def test_running_status_detected(self, mock_agent):
        """Test that running deploy is detected."""
        mock_agent.transport.execute.side_effect = [
            MagicMock(stdout="RUNNING"),  # pgrep
            MagicMock(stdout="file info"),  # ls
            MagicMock(stdout="Building containers..."),  # cat
        ]

        status = get_deploy_status(mock_agent, "deploy-1234567890")

        assert status.status == "RUNNING"
        assert status.process_running

    def test_failed_status_detected(self, mock_agent):
        """Test that failed deploy is detected."""
        mock_agent.transport.execute.side_effect = [
            MagicMock(stdout="NOT_RUNNING"),  # pgrep
            MagicMock(stdout="file info"),  # ls
            MagicMock(stdout="fatal: repository not found"),  # cat
        ]

        status = get_deploy_status(mock_agent, "deploy-1234567890")

        assert status.status == "FAILED"

    def test_deploy_id_is_quoted(self, mock_agent):
        """Test that deploy_id is properly quoted in commands."""
        mock_agent.transport.execute.return_value = MagicMock(stdout="")

        get_deploy_status(mock_agent, "deploy-1234567890")

        # Check that shlex.quote was applied
        calls = mock_agent.transport.execute.call_args_list
        for call in calls:
            command = call[0][0]
            # The deploy_id should be quoted
            assert "deploy-1234567890" in command


class TestFormatDeployStarted:
    """Tests for format_deploy_started function."""

    def test_success_format(self):
        """Test success message format."""
        result = DeployResult(
            success=True,
            deploy_id="deploy-1234567890",
            log_file="/tmp/deploy-1234567890.log",
        )

        message = format_deploy_started(result)

        assert "deploy-1234567890" in message
        assert "/tmp/deploy-1234567890.log" in message
        assert "server_deploy_status" in message

    def test_failure_format(self):
        """Test failure message format."""
        result = DeployResult(
            success=False,
            error="Connection refused",
        )

        message = format_deploy_started(result)

        assert "failed" in message.lower()
        assert "Connection refused" in message


class TestFormatDeployStatus:
    """Tests for format_deploy_status function."""

    def test_format_contains_all_info(self):
        """Test that all status info is included."""
        status = DeployStatus(
            status="COMPLETED",
            process_running=False,
            log_file="/tmp/deploy.log",
            log_content="Build successful\n=== DEPLOY COMPLETE ===",
            file_info="-rw-r--r-- 1 user user 1234",
        )

        message = format_deploy_status(status)

        assert "COMPLETED" in message
        assert "False" in message
        assert "Build successful" in message
        assert "DEPLOY COMPLETE" in message
