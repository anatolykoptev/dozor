# Dozor - AI-First Server Monitoring

> The only MCP server designed for AI agents to monitor Docker infrastructure.

AI-native server monitoring agent with MCP integration. Unlike traditional monitoring tools (Prometheus, Datadog) built for humans with dashboards, Dozor outputs are optimized for LLM consumption.

## Why Dozor?

| Traditional Tools | Dozor |
|-------------------|-------|
| Dashboards & graphs | Text optimized for LLMs |
| Manual alert triage | AI-ready diagnostics |
| Human-in-the-loop | Autonomous agent actions |
| Generic metrics | Context-aware analysis |

## Features

- **Container Monitoring** - Status, health, resource usage
- **Log Analysis** - Pattern matching for known errors
- **Security Audit** - Exposed ports, bot scanners, misconfigurations
- **Background Deploy** - Non-blocking deploys with status tracking
- **Command Validation** - Allowlist/blocklist for safe execution
- **MCP Integration** - Full MCP server for Claude Code

## Quick Start

### 1. Configure Environment

```bash
cp .env.example .env
```

**Required variables:**
```env
SERVER_HOST=your-server.com    # or "local" for direct execution
SERVER_USER=ubuntu
SERVER_COMPOSE_PATH=~/your-project
SERVER_SERVICES=nginx,postgres,redis
```

### 2. MCP Server (Claude Code)

Add to your MCP config:

```json
{
  "mcpServers": {
    "dozor": {
      "command": "ssh",
      "args": ["your-server", "cd ~/dozor && python -m dozor.mcp_server"]
    }
  }
}
```

### Available MCP Tools

| Tool | Description |
|------|-------------|
| `server_diagnose` | Full diagnostics with AI-friendly output |
| `server_status` | Single service status |
| `server_logs` | Get logs with filtering |
| `server_restart` | Restart a service |
| `server_exec` | Execute read-only commands (validated) |
| `server_analyze_logs` | Deep log analysis with patterns |
| `server_health` | Quick health check |
| `server_security` | Security audit |
| `server_deploy` | Background deploy |
| `server_deploy_status` | Check deploy progress |
| `server_prune` | Clean Docker resources |

### 3. CLI Usage

```bash
dozor diagnose          # Full diagnostics
dozor health            # Quick health check
dozor status nginx      # Service status
dozor logs nginx -e     # Errors only
dozor analyze postgres  # Log pattern analysis
```

### 4. Python API

```python
from dozor import ServerAgent

agent = ServerAgent.from_env()
report = agent.diagnose()

if report.needs_attention:
    print(report.to_ai_prompt())  # AI-optimized output
```

## Error Pattern Detection

| Service | Patterns |
|---------|----------|
| PostgreSQL | Auth failures, connection limits, collation issues |
| Redis | Memory limits, connection refused |
| Nginx | Upstream failures, SSL errors |
| Docker | OOM kills, health check failures |
| General | Disk full, timeouts, rate limits |

## Security

Dozor implements defense-in-depth security:

- **Command Validation** - Allowlist for safe commands, blocklist for dangerous patterns
- **Input Sanitization** - All inputs validated with strict regex
- **Path Traversal Protection** - Paths validated against traversal attacks
- **Injection Prevention** - `shlex.quote()` for all shell interpolations
- **Audit Logging** - Security-sensitive operations logged
- **No Shell=True** - Commands built as lists, no shell injection possible

### Blocked Patterns
- `rm -rf`, `mkfs`, `dd` - Destructive commands
- `$()`, backticks - Command substitution
- `${VAR}`, `$VAR` - Variable expansion
- `..` - Path traversal
- `;`, `|`, `&&` - Command chaining

## Architecture

```
dozor/
├── agent.py           # Main orchestrator
├── config.py          # Configuration (from env)
├── transport.py       # SSH/local transport
├── validation.py      # Security validation
├── decorators.py      # @require_valid_service
├── deploy.py          # Background deploy
├── collectors/
│   ├── status.py      # Container status
│   ├── logs.py        # Log collection
│   ├── resources.py   # CPU/memory/disk
│   └── security/      # Security checks
├── analyzers/
│   ├── log_analyzer.py
│   └── alert_generator.py
├── mcp_server.py      # MCP stdio server
├── mcp_server_sse.py  # MCP SSE server
├── mcp_tools.py       # Tool definitions
└── mcp_handlers.py    # Tool implementations
```

## Testing

```bash
# Run all tests (169 tests)
pytest tests/ -v

# Test categories:
# - Security validation (92 tests)
# - Decorators (7 tests)
# - Deploy (17 tests)
# - MCP handlers (23 tests)
```

## Deployment Options

### Option A: Direct SSH (Recommended)

MCP runs over SSH stdio - simplest setup:

```json
{
  "mcpServers": {
    "dozor": {
      "command": "ssh",
      "args": ["server-alias", "cd ~/dozor && python -m dozor.mcp_server"]
    }
  }
}
```

### Option B: SSE with SSH Tunnel

For persistent service:

```bash
# Server: Start MCP service
python -m dozor.mcp_server_sse --port 8765

# Local: SSH tunnel
ssh -L 8765:localhost:8765 server-alias -N &
```

```json
{
  "mcpServers": {
    "dozor": {
      "transport": "sse",
      "url": "http://localhost:8765/sse"
    }
  }
}
```

## License

MIT
