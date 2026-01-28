"""
MCP tool definitions for server agent.
"""

TOOLS = [
    {
        "name": "server_diagnose",
        "description": """Run comprehensive server diagnostics.

Checks all configured services for:
- Container status (running/stopped/restarting)
- Error patterns in logs
- Resource usage (CPU/memory)
- Known issues with suggested fixes

Returns an AI-friendly report with alerts if issues are found.
Use this as the first step when investigating server problems.""",
        "inputSchema": {
            "type": "object",
            "properties": {
                "services": {
                    "type": "array",
                    "items": {"type": "string"},
                    "description": "Optional list of services to check. If not provided, checks all configured services.",
                },
            },
        },
    },
    {
        "name": "server_status",
        "description": """Get status of a specific service.

Returns:
- Container state (running/exited/restarting)
- Uptime
- Restart count
- Recent errors
- Resource usage

Use this for detailed investigation of a single service.""",
        "inputSchema": {
            "type": "object",
            "properties": {
                "service": {
                    "type": "string",
                    "description": "Service name (e.g., 'n8n', 'postgres', 'hasura')",
                },
            },
            "required": ["service"],
        },
    },
    {
        "name": "server_logs",
        "description": """Get logs for a service.

Retrieves container logs with options to filter for errors only.
Logs are parsed and categorized by severity level.""",
        "inputSchema": {
            "type": "object",
            "properties": {
                "service": {
                    "type": "string",
                    "description": "Service name",
                },
                "lines": {
                    "type": "integer",
                    "description": "Number of log lines to retrieve (default: 100)",
                    "default": 100,
                },
                "errors_only": {
                    "type": "boolean",
                    "description": "Only return error-level logs",
                    "default": False,
                },
            },
            "required": ["service"],
        },
    },
    {
        "name": "server_restart",
        "description": """Restart a service container.

Use this to recover from errors after investigating the root cause.
The service will be gracefully stopped and started again.""",
        "inputSchema": {
            "type": "object",
            "properties": {
                "service": {
                    "type": "string",
                    "description": "Service name to restart",
                },
            },
            "required": ["service"],
        },
    },
    {
        "name": "server_exec",
        "description": """Execute a read-only diagnostic command on the server.

SECURITY: Only allowlisted commands are permitted:
- docker ps/logs/inspect/stats
- docker compose ps/logs
- System info: df, free, top, ps, netstat, ss
- Log viewing: cat/tail/head /var/log/*
- systemctl status, journalctl

Examples:
- 'df -h' - Check disk space
- 'docker ps' - List containers
- 'free -h' - Check memory usage""",
        "inputSchema": {
            "type": "object",
            "properties": {
                "command": {
                    "type": "string",
                    "description": "Read-only diagnostic command to execute",
                },
            },
            "required": ["command"],
        },
    },
    {
        "name": "server_analyze_logs",
        "description": """Analyze service logs for known error patterns.

Returns a detailed analysis with:
- Error patterns detected
- Suggested actions for each issue
- Error counts by category

Use this for deep investigation of service issues.""",
        "inputSchema": {
            "type": "object",
            "properties": {
                "service": {
                    "type": "string",
                    "description": "Service name",
                },
            },
            "required": ["service"],
        },
    },
    {
        "name": "server_health",
        "description": """Quick health check of all services.

Returns a simple healthy/unhealthy status for each service.
Use this for quick status checks before running full diagnostics.""",
        "inputSchema": {
            "type": "object",
            "properties": {},
        },
    },
    {
        "name": "server_security",
        "description": """Run security checks on the server.

Checks for:
- Publicly exposed ports (services on 0.0.0.0 that should be internal)
- Firewall status (UFW enabled/disabled)
- Bot scanner activity (malicious probing attempts)
- Weak configurations

Returns security issues with severity levels and remediation steps.
Run this periodically to ensure server security posture.""",
        "inputSchema": {
            "type": "object",
            "properties": {},
        },
    },
    {
        "name": "server_prune",
        "description": """Clean up Docker resources (images, build cache, volumes).

Removes:
- Unused images (dangling and unreferenced)
- Build cache older than specified age
- Unused volumes (optional, dangerous!)

Use this to free disk space after deployments.
WARNING: volume pruning can delete data. Use with caution.""",
        "inputSchema": {
            "type": "object",
            "properties": {
                "images": {
                    "type": "boolean",
                    "description": "Prune unused images (default: true)",
                    "default": True,
                },
                "build_cache": {
                    "type": "boolean",
                    "description": "Prune build cache (default: true)",
                    "default": True,
                },
                "volumes": {
                    "type": "boolean",
                    "description": "Prune unused volumes (default: false, DANGEROUS!)",
                    "default": False,
                },
                "age": {
                    "type": "string",
                    "description": "Only prune items older than this (e.g., '24h', '7d'). Default: '24h'",
                    "default": "24h",
                },
            },
        },
    },
]
