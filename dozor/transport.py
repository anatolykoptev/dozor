"""
Secure SSH transport layer.

SECURITY: All command execution goes through this module.
Local mode commands are validated against the same allowlist as remote commands.
"""

import subprocess
import shlex
from dataclasses import dataclass
from typing import Optional
import sys
import os

# Add parent directory to path for shared imports
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from shared.logging import get_safe_logger as get_logger

from .config import ServerConfig
from .validation import is_command_allowed


logger = get_logger(__name__)


@dataclass
class CommandResult:
    """Result of a remote command execution."""
    stdout: str
    stderr: str
    return_code: int
    command: str
    success: bool

    @property
    def output(self) -> str:
        """Get combined output, preferring stdout."""
        return self.stdout if self.stdout else self.stderr


class SSHTransport:
    """Secure SSH transport for remote command execution.

    Supports local mode when host is "local" or "localhost" -
    commands are executed directly without SSH.
    """

    def __init__(self, config: ServerConfig):
        self.config = config
        self._validated = False
        self._is_local = config.host in ("local", "localhost", "127.0.0.1")

    def _build_ssh_command(self, remote_command: str) -> list[str]:
        """Build SSH command as a list (no shell injection possible)."""
        cmd = ["ssh"]

        # Add SSH options
        cmd.extend(["-o", "BatchMode=yes"])  # No password prompts
        cmd.extend(["-o", "StrictHostKeyChecking=accept-new"])
        cmd.extend(["-o", f"ConnectTimeout={self.config.timeout}"])

        # Add key if specified
        if self.config.ssh_key:
            cmd.extend(["-i", self.config.ssh_key])

        # Add port if non-default
        if self.config.port != 22:
            cmd.extend(["-p", str(self.config.port)])

        # Add destination
        cmd.append(f"{self.config.user}@{self.config.host}")

        # Add the remote command (as a single argument)
        cmd.append(remote_command)

        return cmd

    def execute(
        self,
        command: str,
        timeout: Optional[int] = None,
        skip_validation: bool = False,
    ) -> CommandResult:
        """
        Execute a command on the server.

        If host is "local"/"localhost"/"127.0.0.1", runs command directly.
        Otherwise, runs via SSH.

        SECURITY: Local commands are validated against allowlist by default.
        Internal methods can pass skip_validation=True for trusted commands.

        Args:
            command: Command to execute
            timeout: Optional timeout override
            skip_validation: Skip allowlist check (for internal trusted commands)

        Returns:
            CommandResult with stdout, stderr, and return code
        """
        effective_timeout = timeout or self.config.timeout

        # SECURITY: Validate local commands against allowlist
        if self._is_local and not skip_validation:
            allowed, reason = is_command_allowed(command)
            if not allowed:
                logger.warning(f"Blocked local command: {command[:50]}... Reason: {reason}")
                return CommandResult(
                    stdout="",
                    stderr=f"Command blocked by security policy: {reason}",
                    return_code=-1,
                    command=command,
                    success=False,
                )

        if self._is_local:
            # Local execution - run directly via shell
            logger.debug(f"Executing local command: {command[:100]}...")
            cmd = ["bash", "-c", command]
        else:
            # Remote execution - run via SSH
            logger.debug(f"Executing remote command: {command[:100]}...")
            cmd = self._build_ssh_command(command)

        try:
            result = subprocess.run(
                cmd,
                capture_output=True,
                text=True,
                timeout=effective_timeout,
            )

            return CommandResult(
                stdout=result.stdout.strip(),
                stderr=result.stderr.strip(),
                return_code=result.returncode,
                command=command,
                success=result.returncode == 0,
            )

        except subprocess.TimeoutExpired:
            logger.error(f"Command timed out after {effective_timeout}s: {command[:50]}...")
            return CommandResult(
                stdout="",
                stderr=f"Command timed out after {effective_timeout} seconds",
                return_code=-1,
                command=command,
                success=False,
            )
        except FileNotFoundError:
            logger.error("SSH client not found")
            return CommandResult(
                stdout="",
                stderr="SSH client not found. Please install OpenSSH.",
                return_code=-1,
                command=command,
                success=False,
            )
        except Exception as e:
            logger.error(f"SSH execution failed: {e}")
            return CommandResult(
                stdout="",
                stderr=str(e),
                return_code=-1,
                command=command,
                success=False,
            )

    def test_connection(self) -> tuple[bool, str]:
        """Test connection to server (local or SSH)."""
        result = self.execute("echo 'connection_ok'", timeout=10)

        if result.success and "connection_ok" in result.stdout:
            self._validated = True
            mode = "local" if self._is_local else "SSH"
            return True, f"Connection successful ({mode} mode)"
        else:
            return False, result.stderr or "Connection failed"

    def docker_command(self, docker_cmd: str) -> CommandResult:
        """Execute a docker command on the remote server.

        SECURITY: Internal method - commands are pre-validated by callers.
        """
        full_cmd = f"docker {docker_cmd}"
        return self.execute(full_cmd, skip_validation=True)

    def docker_compose_command(self, compose_cmd: str) -> CommandResult:
        """Execute a docker compose command in the configured path.

        SECURITY: Internal method - commands are pre-validated by callers.
        """
        # Expand ~ to $HOME for SSH compatibility
        path = self.config.compose_path
        if path.startswith("~"):
            # Use $HOME without quoting to allow shell expansion
            path = '"$HOME' + path[1:] + '"'
        else:
            path = shlex.quote(path)
        full_cmd = f"cd {path} && docker compose {compose_cmd}"
        return self.execute(full_cmd, skip_validation=True)
