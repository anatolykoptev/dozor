# Dozor — AI-First Server Monitoring

> MCP server for AI agents to monitor any Linux server: Docker, systemd, remote hosts.

AI-native server monitoring agent. Unlike traditional monitoring tools (Prometheus, Datadog) built for humans with dashboards, Dozor outputs are optimized for LLM consumption via [Model Context Protocol](https://modelcontextprotocol.io/).

## Why Dozor?

| Traditional Tools | Dozor |
|-------------------|-------|
| Dashboards & graphs | Text optimized for LLMs |
| Manual alert triage | AI-ready diagnostics |
| Complex setup | Zero-config auto-discovery |
| Docker-only or agent-based | Docker + systemd + remote + system resources |

## Features

- **Auto-Triage** — One-call full diagnosis: health check, error analysis, disk pressure
- **System Overview** — Memory, disk, load, top processes in one view
- **Docker Monitoring** — Auto-discovers compose services, status, CPU/memory
- **Systemd Services** — User and system service monitoring with memory/uptime
- **Remote Servers** — HTTP availability, SSL expiry, systemd via SSH
- **Log Analysis** — Pattern matching for common errors (DB, auth, OOM, timeouts)
- **System Cleanup** — Scan/clean docker, go, npm, pip, journals, caches with dry-run
- **Security Audit** — Exposed ports, dangerous mounts, bot scanner detection
- **Background Deploy** — Non-blocking deploys with status tracking
- **Binary Updates** — Check and install updates for ~60 popular CLI tools from GitHub releases
- **Command Execution** — Allowlist/blocklist validated shell commands
- **Zero Config** — Auto-detects compose path and services, works out of the box

## Quick Start

```bash
git clone https://github.com/anatolykoptev/dozor.git
cd dozor
make install        # builds and copies to ~/.local/bin/
cp .env.example .env  # optional — works without it
```

### MCP Configuration

**Option A: Stdio over SSH** (recommended for remote servers)

```json
{
  "mcpServers": {
    "dozor": {
      "command": "ssh",
      "args": ["your-server", "dozor serve --stdio"]
    }
  }
}
```

**Option B: HTTP** (for local or network access)

```json
{
  "mcpServers": {
    "dozor": {
      "type": "streamable-http",
      "url": "http://localhost:8765/mcp"
    }
  }
}
```

## MCP Tools

| Tool | Description |
|------|-------------|
| `server_inspect` | Inspect server state (10 modes — see below) |
| `server_triage` | Full auto-diagnosis: discover, check health, analyze errors, disk pressure |
| `server_exec` | Execute validated shell commands |
| `server_restart` | Restart a docker compose service |
| `server_deploy` | Background deploy with status tracking |
| `server_prune` | Clean up Docker resources |
| `server_cleanup` | Scan/clean system resources (docker, go, npm, pip, caches, journals) |
| `server_services` | Manage user-level systemd services (status, restart, logs) |
| `server_updates` | Check and install updates for CLI binaries from GitHub releases |

### Inspection Modes

| Mode | Description |
|------|-------------|
| `overview` | System dashboard: memory, disk, load, top processes, docker + systemd summary |
| `health` | Quick OK/!! status of all Docker services |
| `status` | Detailed status for one service (CPU, memory, uptime, restarts) |
| `diagnose` | Full diagnostics with alerts and health assessment |
| `logs` | Recent logs for a service (with line count) |
| `analyze` | Error pattern analysis with remediation (single service or all) |
| `errors` | ERROR/FATAL log lines from all services in one call |
| `security` | Security audit: exposed ports, containers, bot scanners |
| `systemd` | Local systemd service monitoring (user + system) |
| `remote` | Remote server: HTTP check, SSL expiry, systemd services via SSH |

## CLI Usage

```bash
dozor serve [--port 8765] [--stdio]    # MCP server
dozor check [--json] [--services s1,s2]  # One-shot diagnostics
dozor watch [--interval 4h] [--webhook URL]  # Periodic monitoring
```

## Configuration

All settings are optional. Dozor auto-detects Docker Compose projects and services.

```env
# Docker (auto-detected if not set)
DOZOR_COMPOSE_PATH=~/myproject
DOZOR_SERVICES=web,api,postgres

# Systemd services to monitor
DOZOR_SYSTEMD_SERVICES=nginx,myapp

# Remote server monitoring
DOZOR_REMOTE_HOST=user@server.com
DOZOR_REMOTE_URL=https://example.com
DOZOR_REMOTE_SERVICES=nginx,php-fpm

# Alert thresholds
DOZOR_CPU_THRESHOLD=90
DOZOR_MEMORY_THRESHOLD=90
DOZOR_ERROR_THRESHOLD=5
DOZOR_RESTART_THRESHOLD=1
```

See [.env.example](.env.example) for all options.

## Error Pattern Detection

Dozor detects common error patterns across any service:

| Category | Patterns |
|----------|----------|
| Database | Auth failures, connection limits, schema errors |
| Auth | Token errors, permission denied |
| Network | Connection refused |
| Resources | OOM, disk full |
| Process | SIGTERM, SIGKILL |
| Performance | Timeouts, deadline exceeded, rate limiting |

## Security

Defense-in-depth command validation:

- **Allowlist** — Only safe commands (docker, ps, df, free, systemctl, etc.)
- **Blocklist** — Destructive commands, injection patterns, sensitive files
- **Input Sanitization** — Service names, deploy IDs, durations validated via regex
- **Shell Escaping** — Single-quote wrapping for all interpolated values
- **No Shell Injection** — Process groups with proper signal handling

## Architecture

```
dozor/
├── main.go                     # Entry point: serve/check/watch
├── register.go                 # MCP tool registration
├── tool_inspect.go             # server_inspect (10 modes)
├── tool_triage.go              # server_triage
├── tool_exec.go                # server_exec
├── tool_restart.go             # server_restart
├── tool_deploy.go              # server_deploy
├── tool_prune.go               # server_prune
├── tool_cleanup.go             # server_cleanup
├── tool_services.go            # server_services
├── tool_updates.go             # server_updates
└── internal/engine/
    ├── agent.go                # Orchestrator
    ├── config.go               # Environment config
    ├── types.go                # Data structures
    ├── transport.go            # Local/SSH execution + compose auto-detect
    ├── status.go               # Docker status + auto-discovery
    ├── resources.go            # CPU/memory/disk
    ├── logs.go                 # Log collection & parsing
    ├── log_analyzer.go         # Error pattern analysis
    ├── alerts.go               # Alert generation
    ├── cleanup.go              # System cleanup (docker, go, npm, pip, caches)
    ├── security.go             # Security audit
    ├── remote.go               # Remote server monitoring
    ├── watch.go                # Periodic monitoring + webhooks
    ├── deploy.go               # Background deployments
    ├── updates.go              # Binary update checking + install
    └── validation.go           # Command validation
```

## License

MIT
