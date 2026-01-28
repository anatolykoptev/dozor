"""
Background deploy functionality.

Provides non-blocking deploy with status tracking.
"""

import re
import shlex
import time
from dataclasses import dataclass

from .agent import ServerAgent


@dataclass
class DeployResult:
    """Result of starting a deploy."""
    success: bool
    deploy_id: str = ""
    log_file: str = ""
    error: str = ""


@dataclass
class DeployStatus:
    """Status of a running deploy."""
    status: str  # RUNNING, COMPLETED, FAILED, UNKNOWN
    process_running: bool
    log_file: str
    log_content: str
    file_info: str


def start_background_deploy(
    agent: ServerAgent,
    project_path: str,
    services: list[str] | None = None,
    do_build: bool = True,
    do_pull: bool = True,
) -> DeployResult:
    """Start deploy in background via nohup.

    Returns immediately with deploy_id and log path.
    """
    # SECURITY: Strict path validation (alphanumeric, dots, underscores, tildes, slashes, hyphens)
    if not re.match(r'^[a-zA-Z0-9._~/-]+$', project_path):
        return DeployResult(success=False, error=f"Invalid project path: {project_path}")

    # Additional safety checks
    if ".." in project_path:
        return DeployResult(success=False, error="Path traversal not allowed")

    # Validate services if provided
    if services:
        from .validation import validate_service_name
        for svc in services:
            valid, reason = validate_service_name(svc)
            if not valid:
                return DeployResult(success=False, error=f"Invalid service '{svc}': {reason}")

    services_arg = " ".join(shlex.quote(s) for s in services) if services else ""

    # Generate unique deploy ID (format: deploy-<unix_timestamp>)
    deploy_id = f"deploy-{int(time.time())}"
    log_file = f"/tmp/{deploy_id}.log"

    # SECURITY: Quote all paths for shell (defense in depth)
    safe_path = shlex.quote(project_path)
    safe_log_file = shlex.quote(log_file)

    # Build deploy script
    steps = [f"cd {safe_path}"]

    if do_pull:
        steps.append(
            'echo "=== Pulling latest changes ===" && '
            "git fetch origin main && git reset --hard origin/main"
        )

    if do_build:
        steps.append(
            f'echo "=== Building containers ===" && '
            f"docker compose build --pull {services_arg}"
        )

    steps.append(
        f'echo "=== Deploying containers ===" && '
        f"docker compose up -d --remove-orphans {services_arg}"
    )
    steps.append('echo "=== Container Status ===" && docker compose ps')
    steps.append('echo "=== DEPLOY COMPLETE ==="')

    script = " && ".join(steps)

    # Run in background with nohup (single quotes outside, double inside)
    cmd = f"nohup bash -c '{script}' > {safe_log_file} 2>&1 &"
    result = agent.transport.execute(cmd, skip_validation=True)

    if not result.success:
        return DeployResult(success=False, error=f"Failed to start: {result.stderr}")

    return DeployResult(success=True, deploy_id=deploy_id, log_file=log_file)


def get_deploy_status(agent: ServerAgent, deploy_id: str) -> DeployStatus:
    """Get status of a background deploy.

    SECURITY: deploy_id must be pre-validated with ^deploy-\d{10,13}$ regex.
    """
    log_file = f"/tmp/{deploy_id}.log"

    # SECURITY: Quote all interpolated values (defense in depth)
    safe_deploy_id = shlex.quote(deploy_id)
    safe_log_file = shlex.quote(log_file)

    # Check if process is still running
    proc_check = agent.transport.execute(
        f"pgrep -f {safe_deploy_id} >/dev/null 2>&1 && echo 'RUNNING' || echo 'NOT_RUNNING'",
        skip_validation=True
    )
    process_running = "RUNNING" in (proc_check.stdout or "")

    # Get file info
    file_info = agent.transport.execute(
        f"ls -la {safe_log_file} 2>/dev/null || echo 'File not found'",
        skip_validation=True
    )
    file_info_str = file_info.stdout.strip() if file_info.stdout else "not found"

    # Get log contents
    log_result = agent.transport.execute(
        f"cat {safe_log_file} 2>/dev/null || echo '[Log file not found]'",
        skip_validation=True
    )
    log_content = log_result.stdout or ""

    # Determine status
    if "=== DEPLOY COMPLETE ===" in log_content:
        status = "COMPLETED"
    elif "fatal:" in log_content.lower() or "error:" in log_content.lower():
        status = "FAILED"
    elif process_running:
        status = "RUNNING"
    elif log_content.strip() and "DEPLOY COMPLETE" not in log_content:
        status = "FAILED (process died)"
    else:
        status = "UNKNOWN"

    return DeployStatus(
        status=status,
        process_running=process_running,
        log_file=log_file,
        log_content=log_content,
        file_info=file_info_str,
    )


def format_deploy_started(result: DeployResult) -> str:
    """Format deploy started message."""
    if not result.success:
        return f"Failed to start deploy: {result.error}"

    return f"""Deploy started in background.

Deploy ID: {result.deploy_id}
Log file: {result.log_file}

Check progress: server_deploy_status deploy_id="{result.deploy_id}"
"""


def format_deploy_status(status: DeployStatus) -> str:
    """Format deploy status message."""
    return f"""Status: {status.status}
Process running: {status.process_running}
Log file: {status.file_info}

--- Full Log ---
{status.log_content if status.log_content else "(empty)"}"""
