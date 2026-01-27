# Dozor - AI-First Server Monitoring

AI-native server monitoring agent with MCP integration. Automatically detects issues and provides AI-friendly diagnostics for resolution.

## Features

- **Container Status Monitoring** - Tracks running/stopped/restarting states
- **Log Analysis** - Pattern matching for known errors (PostgreSQL, Hasura, n8n, etc.)
- **Resource Monitoring** - CPU, memory, disk usage tracking
- **Alert Generation** - AI-friendly alerts with suggested actions
- **MCP Integration** - Full MCP server for Claude Code integration

## Quick Start

### 1. Configure Environment

```bash
# Copy and edit .env
cp .env.example .env

# Required variables:
SERVER_HOST=192.9.243.148
SERVER_USER=ubuntu
SERVER_SERVICES=n8n,postgres,hasura
```

### 2. CLI Usage

```bash
# Full diagnostics
dozor diagnose

# Quick health check
dozor health

# Service status
dozor status postgres

# View logs (errors only)
dozor logs n8n --errors

# Analyze logs for patterns
dozor analyze postgres

# Restart service
dozor restart n8n --yes
```

### 3. MCP Server (for Claude Code)

Add to `.mcp.json` in your project root:

```json
{
  "mcpServers": {
    "dozor": {
      "type": "stdio",
      "command": "ssh",
      "args": ["<ssh-alias>", "~/dozor/run-mcp.sh"]
    }
  }
}
```

Available MCP tools:
- `server_diagnose` - Full diagnostics with AI-friendly output
- `server_status` - Single service status
- `server_logs` - Get logs with filtering
- `server_restart` - Restart a service
- `server_exec` - Execute commands
- `server_analyze_logs` - Deep log analysis
- `server_health` - Quick health check

### 4. Python API

```python
from dozor import ServerAgent, ServerConfig

# From environment
agent = ServerAgent.from_env()

# Or explicit config
config = ServerConfig(
    host="192.9.243.148",
    services=["n8n", "postgres", "hasura"],
)
agent = ServerAgent(config)

# Run diagnostics
report = agent.diagnose()

# Check if attention needed
if report.needs_attention:
    print(report.to_ai_prompt())

# Get service status
status = agent.get_service_status("postgres")
print(f"Errors: {status.error_count}")

# Restart service
success, msg = agent.restart_service("n8n")
```

## Error Pattern Detection

The analyzer recognizes patterns for:

| Service | Patterns |
|---------|----------|
| PostgreSQL | Auth failures, connection limits, collation issues, schema errors |
| Hasura | Metadata inconsistency, JWT errors |
| n8n | Workflow failures, connection refused, credential errors |
| Supabase Auth | GoTrue errors, OAuth failures |
| General | OOM, disk full, timeouts, rate limits |

## Alert Levels

- **CRITICAL** - Service down, OOM, disk full
- **ERROR** - Service degraded, multiple errors
- **WARNING** - Potential issues, elevated metrics
- **INFO** - Informational

## Architecture

```
dozor/
├── agent.py          # Main orchestrator
├── config.py         # Configuration handling
├── transport.py      # Secure SSH transport
├── types.py          # Data models
├── collectors/
│   ├── logs.py       # Log collection & parsing
│   ├── status.py     # Container status
│   └── resources.py  # CPU/memory/disk
├── analyzers/
│   ├── log_analyzer.py    # Pattern matching
│   └── alert_generator.py # Alert creation
├── mcp_server.py     # MCP server
├── mcp_tools.py      # Tool definitions
├── mcp_handlers.py   # Tool implementations
└── cli.py            # CLI interface
```

## Remote Deployment (MCP over SSH)

Deploy the agent on the server and connect from local machine:

### 1. Deploy to Server

```bash
# Deploy agent and start MCP service
./scripts/deploy-remote.sh krolik
```

This will:
- Sync code to `~/dozor` on remote
- Install dependencies with SSE transport
- Create systemd service `dozor-mcp`
- Start the service on port 8765

### 2. Connect from Local

```bash
# Start SSH tunnel
./scripts/connect-remote.sh krolik

# Test connection
./scripts/connect-remote.sh krolik test
```

### 3. Configure Claude Code

**Option A: SSH Tunnel + SSE (recommended for persistent service)**

Add to `~/.config/claude-code/settings.json`:

```json
{
  "mcpServers": {
    "dozor-remote": {
      "transport": "sse",
      "url": "http://localhost:8765/sse"
    }
  }
}
```

**Option B: Direct SSH (simpler, no service needed)**

```json
{
  "mcpServers": {
    "dozor-remote": {
      "command": "ssh",
      "args": ["krolik", "cd ~/dozor && python -m dozor.mcp_server"]
    }
  }
}
```

This runs MCP over SSH stdio directly - no systemd service or tunnel needed.

### Architecture

```
┌─────────────┐    SSH Tunnel    ┌─────────────┐
│ Local Mac   │  (port 8765)     │   Server    │
│ Claude Code │─────────────────▶│ MCP Server  │
│   (client)  │                  │ dozor│
└─────────────┘                  └─────────────┘
```

### Manual Setup

```bash
# On server: Start MCP server
python -m dozor.mcp_server_sse --transport sse --port 8765

# On local: Create tunnel
ssh -L 8765:localhost:8765 krolik -N &

# On local: Test
curl http://localhost:8765/health
```

## Security

- No `shell=True` in subprocess calls
- SSH commands built as lists (no injection)
- Dangerous commands blocked in `server_exec`
- Credentials from environment only
- Sensitive data redacted in logs
- MCP server binds to 127.0.0.1 by default (not exposed externally)
- Remote access only via SSH tunnel (encrypted)
