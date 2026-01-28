"""
MCP tool handlers for server agent.

Security: All inputs are validated before execution.
"""

import json
import logging
import re
import shlex
from functools import lru_cache
from typing import Any

logger = logging.getLogger(__name__)

from .agent import ServerAgent
from .decorators import require_valid_service
from .deploy import (
    start_background_deploy,
    format_deploy_started,
    get_deploy_status,
    format_deploy_status,
)
from .models import ContainerState
from .validation import (
    is_command_allowed,
    validate_lines_count,
)


@lru_cache(maxsize=1)
def get_agent() -> ServerAgent:
    """Get or create the agent instance (thread-safe singleton via lru_cache)."""
    return ServerAgent.from_env()


def reset_agent() -> None:
    """Reset agent instance. Use for testing only."""
    get_agent.cache_clear()


def handle_server_diagnose(arguments: dict[str, Any]) -> str:
    """Handle server_diagnose tool call."""
    agent = get_agent()

    # Pass services override (immutable - no state mutation)
    services_override = arguments.get("services")
    report = agent.diagnose(services_override=services_override)

    # Return AI-friendly format
    if report.needs_attention:
        result = report.to_ai_prompt()
        result += "\n\n--- DETAILED JSON ---\n"
        result += json.dumps(report.to_dict(), indent=2, default=str)
        return result
    else:
        return f"All services healthy on {report.server}. " \
               f"{len(report.services)} services checked, no issues found."


@require_valid_service
def handle_server_status(arguments: dict[str, Any]) -> str:
    """Handle server_status tool call."""
    service = arguments["service"]

    agent = get_agent()
    status = agent.get_service_status(service)

    lines = [
        f"Service: {status.name}",
        f"State: {status.state.value}",
        f"Health: {status.health or 'N/A'}",
        f"Uptime: {status.uptime or 'N/A'}",
        f"Restarts: {status.restart_count}",
        f"CPU: {status.cpu_percent or 'N/A'}%",
        f"Memory: {status.memory_mb or 'N/A'}MB / {status.memory_limit_mb or 'N/A'}MB",
        f"Errors: {status.error_count}",
    ]

    if status.recent_errors:
        lines.append("\nRecent errors:")
        for err in status.recent_errors[-5:]:
            lines.append(f"  - {err.message[:100]}")

    return "\n".join(lines)


@require_valid_service
def handle_server_logs(arguments: dict[str, Any]) -> str:
    """Handle server_logs tool call."""
    service = arguments["service"]
    lines = arguments.get("lines", 100)
    errors_only = arguments.get("errors_only", False)

    # Validate lines count
    valid, reason = validate_lines_count(lines)
    if not valid:
        return f"Invalid lines count: {reason}"

    agent = get_agent()
    logs = agent.get_logs(service, lines, errors_only)

    if not logs:
        return f"No {'error ' if errors_only else ''}logs found for {service}"

    result = [f"{'Error logs' if errors_only else 'Logs'} for {service} ({len(logs)} entries):"]

    for entry in logs[-50:]:  # Limit output
        ts = entry.timestamp.strftime("%H:%M:%S") if entry.timestamp else "??:??:??"
        result.append(f"[{ts}] [{entry.level}] {entry.message[:200]}")

    return "\n".join(result)


@require_valid_service
def handle_server_restart(arguments: dict[str, Any]) -> str:
    """Handle server_restart tool call."""
    service = arguments["service"]

    agent = get_agent()
    success, output = agent.restart_service(service)

    if success:
        return f"Service {service} restarted successfully.\n{output}"
    else:
        return f"Failed to restart {service}.\nError: {output}"


def handle_server_exec(arguments: dict[str, Any]) -> str:
    """Handle server_exec tool call.

    SECURITY: Uses allowlist-based validation.
    Only read-only diagnostic commands are permitted.
    """
    command = arguments["command"]

    # Validate command against allowlist
    allowed, reason = is_command_allowed(command)
    if not allowed:
        return f"Command blocked: {reason}\n\nAllowed commands:\n" \
               "- docker ps/logs/inspect/stats\n" \
               "- docker compose ps/logs\n" \
               "- System info: df, free, top, ps, netstat\n" \
               "- Log viewing: cat/tail/head /var/log/*\n" \
               "- systemctl status, journalctl"

    agent = get_agent()
    result = agent.transport.execute(command)

    if result.success:
        return result.stdout or "(no output)"
    else:
        return f"Command failed (exit code {result.return_code}):\n{result.stderr}"


@require_valid_service
def handle_server_analyze_logs(arguments: dict[str, Any]) -> str:
    """Handle server_analyze_logs tool call."""
    service = arguments["service"]

    agent = get_agent()
    return agent.get_service_logs_summary(service)


def handle_server_health(arguments: dict[str, Any]) -> str:
    """Handle server_health tool call."""
    agent = get_agent()

    statuses = agent.status_collector.get_all_statuses(agent.server_config.services)

    lines = [f"Health check for {agent.server_config.host}:", ""]

    healthy = 0
    unhealthy = 0

    for status in statuses:
        if status.state == ContainerState.RUNNING:
            lines.append(f"  [OK] {status.name}")
            healthy += 1
        else:
            lines.append(f"  [!!] {status.name}: {status.state.value}")
            unhealthy += 1

    lines.append("")
    lines.append(f"Summary: {healthy} healthy, {unhealthy} unhealthy")

    if unhealthy > 0:
        lines.append("\nRecommendation: Run server_diagnose for detailed analysis.")

    return "\n".join(lines)


def handle_server_security(arguments: dict[str, Any]) -> str:
    """Handle server_security tool call."""
    agent = get_agent()
    return agent.get_security_report()


def handle_server_prune(arguments: dict[str, Any]) -> str:
    """Handle server_prune tool call.

    Cleanup Docker resources (images, build cache, volumes).
    """
    prune_images = arguments.get("images", True)
    prune_build_cache = arguments.get("build_cache", True)
    prune_volumes = arguments.get("volumes", False)
    age = arguments.get("age", "24h")

    # Validate age format (strict: only digits + unit)
    if not re.match(r'^\d+[smhd]$', age):
        return f"Invalid age format: {age}. Use format like '24h', '7d', '30m'"

    # SECURITY: Always quote user input for shell commands
    safe_age = shlex.quote(age)

    agent = get_agent()
    results = []

    # Prune images
    if prune_images:
        cmd = f"docker image prune -af --filter until={safe_age}"
        result = agent.transport.execute(cmd, skip_validation=True)
        if result.success:
            results.append(f"Images pruned: {result.stdout or 'done'}")
        else:
            results.append(f"Image prune failed: {result.stderr}")

    # Prune build cache
    if prune_build_cache:
        cmd = f"docker builder prune -af --filter until={safe_age}"
        result = agent.transport.execute(cmd, skip_validation=True)
        if result.success:
            results.append(f"Build cache pruned: {result.stdout or 'done'}")
        else:
            results.append(f"Build cache prune failed: {result.stderr}")

    # Prune volumes (dangerous!)
    if prune_volumes:
        cmd = "docker volume prune -f"
        result = agent.transport.execute(cmd, skip_validation=True)
        if result.success:
            results.append(f"Volumes pruned: {result.stdout or 'done'}")
        else:
            results.append(f"Volume prune failed: {result.stderr}")

    # Get disk usage after cleanup
    df_result = agent.transport.execute("df -h /var/lib/docker", skip_validation=True)
    if df_result.success:
        results.append(f"\nDisk usage after cleanup:\n{df_result.stdout}")

    return "\n".join(results)


def handle_server_deploy(arguments: dict[str, Any]) -> str:
    """Handle server_deploy tool call.

    Deploy runs in BACKGROUND via nohup. Returns immediately with log path.
    Use server_deploy_status to check progress.
    """
    agent = get_agent()
    project_path = arguments.get("project_path") or agent.server_config.compose_path

    result = start_background_deploy(
        agent=agent,
        project_path=project_path,
        services=arguments.get("services"),
        do_build=arguments.get("build", True),
        do_pull=arguments.get("pull", True),
    )
    return format_deploy_started(result)


def handle_server_deploy_status(arguments: dict[str, Any]) -> str:
    """Check status of background deploy."""
    deploy_id = arguments.get("deploy_id", "")

    if not deploy_id:
        return "Error: deploy_id is required"

    # SECURITY: Strict deploy_id validation (format: deploy-<unix_timestamp>)
    if not re.match(r'^deploy-\d{10,13}$', deploy_id):
        return f"Invalid deploy_id format. Expected: deploy-<timestamp>"

    agent = get_agent()
    status = get_deploy_status(agent, deploy_id)
    return format_deploy_status(status)


# Handler dispatch map
HANDLERS = {
    "server_diagnose": handle_server_diagnose,
    "server_status": handle_server_status,
    "server_logs": handle_server_logs,
    "server_restart": handle_server_restart,
    "server_exec": handle_server_exec,
    "server_analyze_logs": handle_server_analyze_logs,
    "server_health": handle_server_health,
    "server_security": handle_server_security,
    "server_prune": handle_server_prune,
    "server_deploy": handle_server_deploy,
    "server_deploy_status": handle_server_deploy_status,
}


def handle_tool(name: str, arguments: dict[str, Any]) -> str:
    """Dispatch tool call to appropriate handler."""
    handler = HANDLERS.get(name)
    if handler is None:
        return f"Unknown tool: {name}"

    try:
        return handler(arguments)
    except Exception as e:
        logger.exception(f"Error executing tool {name}")
        return f"Error executing {name}: {str(e)}"
