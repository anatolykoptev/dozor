"""
MCP tool handlers for server agent.

Security: All inputs are validated before execution.
"""

import json
from typing import Any

from .agent import ServerAgent
from .validation import (
    is_command_allowed,
    validate_service_name,
    validate_lines_count,
)


# Global agent instance
_agent: ServerAgent | None = None


def get_agent() -> ServerAgent:
    """Get or create the global agent instance."""
    global _agent
    if _agent is None:
        _agent = ServerAgent.from_env()
    return _agent


def handle_server_diagnose(arguments: dict[str, Any]) -> str:
    """Handle server_diagnose tool call."""
    agent = get_agent()

    # Override services if provided
    if arguments.get("services"):
        agent.server_config.services = arguments["services"]

    report = agent.diagnose()

    # Return AI-friendly format
    if report.needs_attention:
        result = report.to_ai_prompt()
        result += "\n\n--- DETAILED JSON ---\n"
        result += json.dumps(report.to_dict(), indent=2, default=str)
        return result
    else:
        return f"All services healthy on {report.server}. " \
               f"{len(report.services)} services checked, no issues found."


def handle_server_status(arguments: dict[str, Any]) -> str:
    """Handle server_status tool call."""
    service = arguments["service"]

    # Validate service name
    valid, reason = validate_service_name(service)
    if not valid:
        return f"Invalid service name: {reason}"

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


def handle_server_logs(arguments: dict[str, Any]) -> str:
    """Handle server_logs tool call."""
    service = arguments["service"]
    lines = arguments.get("lines", 100)
    errors_only = arguments.get("errors_only", False)

    # Validate inputs
    valid, reason = validate_service_name(service)
    if not valid:
        return f"Invalid service name: {reason}"

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


def handle_server_restart(arguments: dict[str, Any]) -> str:
    """Handle server_restart tool call."""
    service = arguments["service"]

    # Validate service name
    valid, reason = validate_service_name(service)
    if not valid:
        return f"Invalid service name: {reason}"

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


def handle_server_analyze_logs(arguments: dict[str, Any]) -> str:
    """Handle server_analyze_logs tool call."""
    service = arguments["service"]

    # Validate service name
    valid, reason = validate_service_name(service)
    if not valid:
        return f"Invalid service name: {reason}"

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
        from .models import ContainerState
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
    import re

    prune_images = arguments.get("images", True)
    prune_build_cache = arguments.get("build_cache", True)
    prune_volumes = arguments.get("volumes", False)
    age = arguments.get("age", "24h")

    # Validate age format
    if not re.match(r'^\d+[smhd]$', age):
        return f"Invalid age format: {age}. Use format like '24h', '7d', '30m'"

    agent = get_agent()
    results = []

    # Prune images
    if prune_images:
        cmd = f"docker image prune -af --filter 'until={age}'"
        result = agent.transport.execute(cmd, skip_validation=True)
        if result.success:
            results.append(f"Images pruned: {result.stdout or 'done'}")
        else:
            results.append(f"Image prune failed: {result.stderr}")

    # Prune build cache
    if prune_build_cache:
        cmd = f"docker builder prune -af --filter 'until={age}'"
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

    Deploy application directly via SSH - faster than GitHub Actions.
    """
    import shlex

    agent = get_agent()

    # Use SERVER_COMPOSE_PATH from config as default
    project_path = arguments.get("project_path") or agent.server_config.compose_path
    services = arguments.get("services", [])
    do_build = arguments.get("build", True)
    do_pull = arguments.get("pull", True)

    # Validate project_path (basic security check)
    if ".." in project_path or ";" in project_path or "|" in project_path:
        return f"Invalid project path: {project_path}"
    results = []
    services_arg = " ".join(shlex.quote(s) for s in services) if services else ""

    # Step 1: Git pull
    if do_pull:
        results.append("=== Pulling latest changes ===")
        cmd = f"cd {project_path} && git fetch origin main && git reset --hard origin/main"
        result = agent.transport.execute(cmd, skip_validation=True)
        if result.success:
            results.append(result.stdout or "Git pull: done")
        else:
            results.append(f"Git pull failed: {result.stderr}")
            return "\n".join(results)

    # Step 2: Docker build
    if do_build:
        results.append("\n=== Building containers ===")
        cmd = f"cd {project_path} && docker compose build --pull {services_arg}"
        result = agent.transport.execute(cmd, skip_validation=True, timeout=600)
        if result.success:
            results.append(result.stdout or "Build: done")
        else:
            results.append(f"Build failed: {result.stderr}")
            return "\n".join(results)

    # Step 3: Deploy
    results.append("\n=== Deploying containers ===")
    cmd = f"cd {project_path} && docker compose up -d --remove-orphans {services_arg}"
    result = agent.transport.execute(cmd, skip_validation=True, timeout=300)
    if result.success:
        results.append(result.stdout or "Deploy: done")
    else:
        results.append(f"Deploy failed: {result.stderr}")
        return "\n".join(results)

    # Step 4: Show status
    results.append("\n=== Container Status ===")
    cmd = f"cd {project_path} && docker compose ps --format 'table {{{{.Name}}}}\\t{{{{.Status}}}}\\t{{{{.Health}}}}'"
    result = agent.transport.execute(cmd, skip_validation=True)
    if result.success:
        results.append(result.stdout or "(no output)")

    return "\n".join(results)


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
}


def handle_tool(name: str, arguments: dict[str, Any]) -> str:
    """Dispatch tool call to appropriate handler."""
    handler = HANDLERS.get(name)
    if handler is None:
        return f"Unknown tool: {name}"

    try:
        return handler(arguments)
    except Exception as e:
        return f"Error executing {name}: {str(e)}"
