# Configuration

All configuration is done via environment variables. Place them in a `.env` file in the dozor working directory or export them in your shell/systemd unit.

All settings are optional unless noted. Dozor works zero-config for local Docker setups.

## Core

| Variable | Default | Description |
|----------|---------|-------------|
| `DOZOR_HOST` | `local` | Host mode: `local`, `localhost`, `127.0.0.1`, or `user@host` for SSH |
| `DOZOR_SSH_PORT` | `22` | SSH port (only used when host is remote) |
| `DOZOR_COMPOSE_PATH` | _(auto-detected)_ | Docker Compose project path. Searches cwd, home if not set |
| `DOZOR_SERVICES` | _(auto-discovered)_ | Comma-separated Docker services to monitor. Auto-discovered via Docker SDK if not set |
| `DOZOR_TIMEOUT` | `30` | Command execution timeout in seconds |
| `DOZOR_MCP_PORT` | `8765` | HTTP port for the MCP server |
| `DOZOR_WORKSPACE` | `~/.dozor` | Path to workspace directory (IDENTITY.md, MEMORY.md, AGENTS.md) |

## Watch Mode

| Variable | Default | Description |
|----------|---------|-------------|
| `DOZOR_WATCH_INTERVAL` | `4h` | Periodic health check interval (Go duration: `30m`, `1h`, `4h`) |
| `DOZOR_WEBHOOK_URL` | _(empty)_ | Webhook URL for alert notifications (POST with JSON body) |

## Alert Thresholds

| Variable | Default | Description |
|----------|---------|-------------|
| `DOZOR_CPU_THRESHOLD` | `90` | CPU usage alert threshold (%) |
| `DOZOR_MEMORY_THRESHOLD` | `90` | Memory usage alert threshold (%) |
| `DOZOR_DISK_THRESHOLD` | `80` | Disk usage warning threshold (%) |
| `DOZOR_DISK_CRITICAL` | `95` | Disk usage critical threshold (%) |
| `DOZOR_ERROR_THRESHOLD` | `5` | Error count per service to trigger alert |
| `DOZOR_RESTART_THRESHOLD` | `1` | Restart count to trigger alert |
| `DOZOR_LOG_LINES` | `100` | Default number of log lines to analyze |

## Remote Server

| Variable | Default | Description |
|----------|---------|-------------|
| `DOZOR_REMOTE_HOST` | _(empty)_ | SSH target for remote server (`user@host`) |
| `DOZOR_REMOTE_URL` | _(empty)_ | HTTP(S) URL to check availability and SSL expiry |
| `DOZOR_REMOTE_SERVICES` | _(empty)_ | Comma-separated remote systemd services to monitor |
| `DOZOR_REMOTE_SSH_PORT` | `22` | SSH port for remote server |

> Remote server requires sudoers on the target: `user ALL=(ALL) NOPASSWD: /usr/bin/systemctl restart nginx, ...`

## Systemd Services

| Variable | Default | Description |
|----------|---------|-------------|
| `DOZOR_SYSTEMD_SERVICES` | _(auto-discovered)_ | Comma-separated local systemd services to monitor |
| `DOZOR_USER_SERVICES` | _(auto-discovered)_ | User-level systemd services. Format: `name:port,name:port` |
| `DOZOR_USER_SERVICES_USER` | _(empty)_ | Linux user for user-level systemd commands |

## Command Execution Security

| Variable | Default | Description |
|----------|---------|-------------|
| `DOZOR_EXEC_SECURITY` | `ask` | Default security mode for `server_exec`: `safe` (blocklist), `ask` (Telegram approval), `full` (unrestricted) |
| `DOZOR_REQUIRED_AUTH_VARS` | _(empty)_ | Auth env vars to verify in compose config (comma-separated) |

## LLM Provider

Required for `gateway` mode (agent loop with Telegram).

| Variable | Default | Description |
|----------|---------|-------------|
| `DOZOR_LLM_URL` | `http://127.0.0.1:8787/v1` | OpenAI-compatible API base URL |
| `DOZOR_LLM_MODEL` | `gemini-2.5-flash` | Model name |
| `DOZOR_LLM_API_KEY` | _(empty)_ | API key for LLM provider |
| `DOZOR_MAX_TOOL_ITERATIONS` | `10` | Max tool call iterations per agent request |

### LLM Fallback

Optional fallback provider when primary LLM fails.

| Variable | Default | Description |
|----------|---------|-------------|
| `DOZOR_LLM_FALLBACK_URL` | _(falls back to primary)_ | Fallback API base URL |
| `DOZOR_LLM_FALLBACK_MODEL` | _(falls back to primary)_ | Fallback model name |
| `DOZOR_LLM_FALLBACK_API_KEY` | _(falls back to primary)_ | Fallback API key |

## Telegram

| Variable | Default | Description |
|----------|---------|-------------|
| `DOZOR_TELEGRAM_TOKEN` | _(empty)_ | Bot token from @BotFather |
| `DOZOR_TELEGRAM_ALLOWED` | _(empty)_ | Comma-separated allowed Telegram user IDs |
| `DOZOR_TELEGRAM_ADMIN` | _(first from ALLOWED)_ | Admin chat ID for internal notifications |

## A2A Protocol

Agent-to-agent communication for multi-agent setups.

| Variable | Default | Description |
|----------|---------|-------------|
| `DOZOR_A2A_SECRET` | _(empty)_ | Bearer token for A2A endpoint authentication |
| `DOZOR_A2A_AGENTS` | _(empty)_ | Remote A2A agents. Format: `name=http://host:port,name=http://host:port` |

## Remote MCP Servers

| Variable | Default | Description |
|----------|---------|-------------|
| `DOZOR_MCP_SERVERS` | _(empty)_ | Remote MCP servers. Format: `id=http://host:port/mcp,id=url` |

## Knowledge Base

Pluggable knowledge base via any MCP server with search/save tools.

| Variable | Default | Description |
|----------|---------|-------------|
| `DOZOR_KB_SERVER` | `memdb` | MCP server ID to use as knowledge base backend |
| `DOZOR_KB_USER` | `default` | User ID for KB queries |
| `DOZOR_KB_CUBE` | `default` | Cube/namespace for KB queries |
| `DOZOR_KB_SEARCH_TOOL` | `search_memories` | MCP tool name for search operations |
| `DOZOR_KB_SAVE_TOOL` | `add_memory` | MCP tool name for save operations |

> Legacy: `DOZOR_MEMDB_USER` and `DOZOR_MEMDB_CUBE` are supported as fallbacks.

## Claude Code Escalation

Optional integration with Claude Code CLI for deep analysis.

| Variable | Default | Description |
|----------|---------|-------------|
| `DOZOR_CLAUDE_BINARY` | `claude` | Path to Claude Code CLI binary |
| `DOZOR_CLAUDE_TIMEOUT_SECONDS` | `300` | Timeout for Claude Code sessions (max 900) |
| `DOZOR_CLAUDE_MCP_ENABLED` | `true` | Enable MCP self-connect (passes Dozor tools to Claude) |
| `DOZOR_CLAUDE_ALLOWED_TOOLS` | `mcp__dozor__*,Read,Bash(git*),Bash(ls*),Bash(cat*),Bash(find*),Bash(grep*)` | Tools Claude Code is allowed to use |

## Web Search

| Variable | Default | Description |
|----------|---------|-------------|
| `DOZOR_DDG_ENABLED` | `true` | Enable DuckDuckGo search (no API key needed) |
| `DOZOR_DDG_MAX_RESULTS` | `5` | Max DuckDuckGo results |
| `DOZOR_BRAVE_ENABLED` | `false` | Enable Brave Search |
| `DOZOR_BRAVE_API_KEY` | _(empty)_ | Brave Search API key |
| `DOZOR_BRAVE_MAX_RESULTS` | `5` | Max Brave results |
| `DOZOR_PERPLEXITY_ENABLED` | `false` | Enable Perplexity Search |
| `DOZOR_PERPLEXITY_API_KEY` | _(empty)_ | Perplexity API key |
| `DOZOR_PERPLEXITY_MAX_RESULTS` | `5` | Max Perplexity results |

## Binary Updates

| Variable | Default | Description |
|----------|---------|-------------|
| `DOZOR_TRACKED_BINARIES` | _(empty)_ | GitHub binaries to track. Format: `owner/repo:binary,owner/repo` |
| `DOZOR_GITHUB_TOKEN` | _(empty)_ | GitHub token for higher API rate limits (60/hr → 5000/hr) |
