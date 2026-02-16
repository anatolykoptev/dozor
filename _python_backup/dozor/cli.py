"""
CLI interface for Dozor server monitoring.

Usage:
    dozor diagnose
    dozor status n8n
    dozor logs postgres --errors
    dozor health
    dozor restart n8n
"""

import argparse
import json
import sys
import os

# Add parent to path
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from dotenv import load_dotenv


def main():
    load_dotenv()

    parser = argparse.ArgumentParser(
        description="Dozor - AI-first server monitoring",
        prog="dozor",
    )

    subparsers = parser.add_subparsers(dest="command", help="Commands")

    # diagnose command
    diag = subparsers.add_parser("diagnose", help="Run full diagnostics")
    diag.add_argument("--json", action="store_true", help="Output as JSON")
    diag.add_argument("--services", nargs="+", help="Services to check")

    # status command
    status = subparsers.add_parser("status", help="Get service status")
    status.add_argument("service", help="Service name")

    # logs command
    logs = subparsers.add_parser("logs", help="Get service logs")
    logs.add_argument("service", help="Service name")
    logs.add_argument("--lines", "-n", type=int, default=100, help="Number of lines")
    logs.add_argument("--errors", "-e", action="store_true", help="Errors only")

    # health command
    subparsers.add_parser("health", help="Quick health check")

    # restart command
    restart = subparsers.add_parser("restart", help="Restart a service")
    restart.add_argument("service", help="Service name")
    restart.add_argument("--yes", "-y", action="store_true", help="Skip confirmation")

    # analyze command
    analyze = subparsers.add_parser("analyze", help="Analyze service logs")
    analyze.add_argument("service", help="Service name")

    # exec command
    exec_cmd = subparsers.add_parser("exec", help="Execute command on server")
    exec_cmd.add_argument("cmd", nargs="+", help="Command to execute")

    # security command
    subparsers.add_parser("security", help="Run security checks")

    args = parser.parse_args()

    if not args.command:
        parser.print_help()
        return

    # Import agent after parsing to avoid slow startup for --help
    from .agent import ServerAgent
    from .config import ServerConfig

    try:
        config = ServerConfig.from_env()
    except ValueError as e:
        print(f"Configuration error: {e}")
        print("Set SERVER_HOST environment variable or create .env file")
        sys.exit(1)

    if hasattr(args, 'services') and args.services:
        config.services = args.services

    agent = ServerAgent(config)

    # Test connection
    success, msg = agent.connect()
    if not success:
        print(f"Connection failed: {msg}")
        sys.exit(1)

    # Execute command
    if args.command == "diagnose":
        cmd_diagnose(agent, args)
    elif args.command == "status":
        cmd_status(agent, args)
    elif args.command == "logs":
        cmd_logs(agent, args)
    elif args.command == "health":
        cmd_health(agent, args)
    elif args.command == "restart":
        cmd_restart(agent, args)
    elif args.command == "analyze":
        cmd_analyze(agent, args)
    elif args.command == "exec":
        cmd_exec(agent, args)
    elif args.command == "security":
        cmd_security(agent, args)


def cmd_diagnose(agent, args):
    """Run diagnostics."""
    report = agent.diagnose()

    if args.json:
        print(json.dumps(report.to_dict(), indent=2, default=str))
    else:
        print(report.to_ai_prompt())

        if report.alerts:
            print("\n" + "=" * 60)
            print("ALERTS:")
            print("=" * 60)
            for alert in report.alerts:
                print(f"\n[{alert.level.value.upper()}] {alert.title}")
                print(f"  Service: {alert.service}")
                print(f"  Action: {alert.suggested_action}")


def cmd_status(agent, args):
    """Get service status."""
    status = agent.get_service_status(args.service)

    print(f"Service: {status.name}")
    print(f"State: {status.state.value}")
    print(f"Health: {status.health or 'N/A'}")
    print(f"Uptime: {status.uptime or 'N/A'}")
    print(f"Restarts: {status.restart_count}")

    if status.cpu_percent is not None:
        print(f"CPU: {status.cpu_percent}%")
    if status.memory_mb is not None:
        print(f"Memory: {status.memory_mb}MB / {status.memory_limit_mb or '?'}MB")

    print(f"Errors: {status.error_count}")

    if status.recent_errors:
        print("\nRecent errors:")
        for err in status.recent_errors[-5:]:
            print(f"  - {err.message[:100]}")


def cmd_logs(agent, args):
    """Get service logs."""
    logs = agent.get_logs(args.service, args.lines, args.errors)

    if not logs:
        print(f"No logs found for {args.service}")
        return

    for entry in logs:
        ts = entry.timestamp.strftime("%Y-%m-%d %H:%M:%S") if entry.timestamp else "unknown"
        print(f"[{ts}] [{entry.level}] {entry.message}")


def cmd_health(agent, args):
    """Quick health check."""
    from .models import ContainerState

    statuses = agent.status_collector.get_all_statuses(agent.server_config.services)

    print(f"Health check: {agent.server_config.host}")
    print()

    for status in statuses:
        if status.state == ContainerState.RUNNING:
            print(f"  [OK] {status.name}")
        else:
            print(f"  [!!] {status.name}: {status.state.value}")


def cmd_restart(agent, args):
    """Restart service."""
    if not args.yes:
        confirm = input(f"Restart {args.service}? [y/N] ")
        if confirm.lower() != 'y':
            print("Cancelled")
            return

    success, output = agent.restart_service(args.service)
    if success:
        print(f"Restarted {args.service}")
    else:
        print(f"Failed: {output}")


def cmd_analyze(agent, args):
    """Analyze logs."""
    print(agent.get_service_logs_summary(args.service))


def cmd_security(agent, args):
    """Run security checks."""
    print(agent.get_security_report())


def cmd_exec(agent, args):
    """Execute command (with security validation)."""
    from .validation import is_command_allowed

    cmd = " ".join(args.cmd)

    # Validate command against allowlist
    allowed, reason = is_command_allowed(cmd)
    if not allowed:
        print(f"Command blocked: {reason}")
        print("\nAllowed commands:")
        print("  - docker ps/logs/inspect/stats")
        print("  - docker compose ps/logs")
        print("  - System info: df, free, top, ps, netstat")
        print("  - Log viewing: cat/tail/head /var/log/*")
        print("  - systemctl status, journalctl")
        sys.exit(1)

    result = agent.transport.execute(cmd)

    if result.success:
        print(result.stdout)
    else:
        print(f"Error (exit {result.return_code}):")
        print(result.stderr)


if __name__ == "__main__":
    main()
