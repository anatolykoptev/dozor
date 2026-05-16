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
| `DOZOR_BIND_HOST` | `127.0.0.1` | Network interface to bind the HTTP server to. Default is loopback-only. Set `0.0.0.0` only if a container ingress or non-Caddy proxy needs direct host-network access. For typical deployments (Caddy reverse proxy on the same host) the default is correct and safest. |
| `DOZOR_WORKSPACE` | `~/.dozor` | Path to workspace directory (IDENTITY.md, MEMORY.md, AGENTS.md) |
| `DOZOR_DEV_MODE` | _(unset)_ | When truthy (`1`/`true`/`yes`/`on`), starts the agent in dev mode: periodic watch runs triage and notifies via Telegram/alertmanager but skips auto-remediation, and the routing prompt instructs the LLM to observe only. Equivalent to calling `server_dev_mode(enable=true)` immediately after startup, but survives restarts. Useful during active development of the server itself — Dozor sees the issues, reports them, doesn't touch anything. Runtime override semantics: calling `server_dev_mode(enable=false)` at runtime turns dev mode OFF for the current process lifetime; on next restart the env var is re-evaluated and dev mode re-enables. To disable permanently, unset the env var. |

## Personal Config: Where to Put It

**This repo is public.** Keep operator-specific text — your real network topology, downstream A2A agent IDs / URLs, infrastructure paths, persona — OUT of the repo and IN your `$DOZOR_WORKSPACE` (default `~/.dozor/`).

| Path | Purpose |
|------|---------|
| `workspace/` in this repo | Open-source baseline templates. Generic only — no real agent IDs, no operator paths. `InitWorkspace` copies these to `$DOZOR_WORKSPACE` on first boot if it is empty. |
| `$DOZOR_WORKSPACE/IDENTITY.md` | Persona — generic baseline acceptable, customize as needed |
| `$DOZOR_WORKSPACE/AGENTS.md` | **Operator-private** — your actual A2A network: real agent IDs, real URLs, real routing rules. Never commit this verbatim back to the repo. |
| `$DOZOR_WORKSPACE/MEMORY.md` | **Operator-private** — accumulated operational notes (services, hosts, incidents) |
| `$DOZOR_WORKSPACE/skills/<name>/SKILL.md` | **Operator-private** — your custom skills (loader tier: workspace > builtin) |
| `$DOZOR_WORKSPACE/sessions/` | **Operator-private** — Claude Code interactive session state |

Skill loader resolution (`internal/skills/loader.go`): workspace tier (`$DOZOR_WORKSPACE/skills/`) overrides the builtin tier (compiled-in `skills/`). Put site-specific skills in the workspace; keep the builtin set generic.

Before opening a PR that touches `workspace/` or `skills/`, grep your diff for: operator usernames, hardcoded `/home/<user>/` paths, real A2A agent IDs (e.g. names like `orchestrator-prod`, internal port numbers like `:18790`). If any are found, move them to `~/.dozor/` instead.

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
| `DOZOR_LLM_MODEL` | `gemini-3.1-flash-lite-preview` | Model name |
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
| `DOZOR_A2A_SECRET` | _(empty)_ | **Required when A2A is enabled.** Bearer token for `/a2a` endpoint authentication. If unset, dozor refuses to start with a fatal error (fail-closed). See rationale below. |
| `DOZOR_A2A_ALLOW_INSECURE` | _(empty)_ | Set to exactly `true` (case-sensitive) to allow the A2A endpoint to start without `DOZOR_A2A_SECRET`. Values like `"True"`, `"1"`, `"yes"` are treated as unset (fail-closed). Emits a WARN on startup. **Dev/test only — never set in production.** |
| `DOZOR_A2A_AGENTS` | _(empty)_ | Remote A2A agents. Format: `name=http://host:port,name=http://host:port` |

### Security rationale — 2026-05-12 incident

Prior to this change, an empty `DOZOR_A2A_SECRET` caused `bearerAuthMiddleware` to silently skip authentication (`if secret == "" { next.ServeHTTP(...) }`). Any process with localhost access could POST to `/a2a` and invoke `claude_code` with the full dozor tool palette — including `server_exec` and `server_remote_exec`.

The fix is **fail-closed**: if `DOZOR_A2A_SECRET` is unset and `DOZOR_A2A_ALLOW_INSECURE` is not explicitly `true`, `Register` returns an error and the process exits before binding the endpoint.

Behavior matrix:

| `DOZOR_A2A_SECRET` | `DOZOR_A2A_ALLOW_INSECURE` | Result |
|--------------------|---------------------------|--------|
| non-empty          | any                        | Endpoint registered, Bearer auth enforced |
| empty              | `true`                     | Endpoint registered, all requests allowed; WARN logged on startup and per-request |
| empty              | unset / other (e.g. `"True"`, `"1"`, `"yes"`) | `Register` returns error → process exits (fail-closed). Only the exact string `"true"` is accepted. |

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
| `DOZOR_SESSION_IDLE_TIMEOUT` | `900` | Idle timeout for interactive Claude Code sessions (seconds). Session is terminated if no input arrives within this window. |

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

## Docker Labels

Per-container configuration via Docker labels. Set them in `docker-compose.yml` under `labels:` or via `docker run --label`. Labels are only available in SDK mode (local). CLI/SSH fallback gracefully ignores them.

| Label | Description |
|-------|-------------|
| `dozor.enable` | Set to `false` to exclude a container from monitoring (default: `true`) |
| `dozor.name` | Custom display name for the container in reports |
| `dozor.group` | Group name for organizing services. Enables grouped triage output and `server_inspect({mode: "groups"})` dashboard |
| `dozor.depends_on` | Comma-separated service names this service depends on. Used for cascade restart ordering |
| `dozor.healthcheck.url` | HTTP endpoint to probe during triage. Returns OK/FAIL status in reports |
| `dozor.logs.pattern` | Custom regex pattern for log analysis. Matched lines are reported as `warning`-level `custom` category issues |
| `dozor.alert.channel` | Alert routing hint (e.g. Telegram group, webhook ID). Propagated to all alerts for this service |

Example in `docker-compose.yml`:

```yaml
services:
  my-api:
    image: my-api:latest
    labels:
      dozor.healthcheck.url: "http://my-api:8080/health"
      dozor.logs.pattern: "(?i)(payment failed|stripe error)"
      dozor.alert.channel: "ops-critical"
```

### Groups & Dependencies

Group services for organized triage output and define dependencies for cascade restarts:

```yaml
services:
  postgres:
    image: postgres:16
    labels:
      dozor.group: "data"

  redis:
    image: redis:7
    labels:
      dozor.group: "data"

  my-api:
    image: my-api:latest
    labels:
      dozor.group: "backend"
      dozor.depends_on: "postgres,redis"

  my-worker:
    image: my-worker:latest
    labels:
      dozor.group: "backend"
      dozor.depends_on: "postgres,my-api"
```

With this config:
- `server_triage` shows services grouped by `data` and `backend` with per-group health
- `server_inspect({mode: "groups"})` shows a dashboard of all groups
- `server_restart({service: "postgres"})` cascades to restart `my-api` then `my-worker` (topological order)

## GitHub Webhook Deploy (`deploy-repos.yaml`)

Dozor triggers repo-specific builds on `push` events from GitHub. Config file: `~/.dozor/deploy-repos.yaml`.

Three deploy kinds are supported:

### `compose` (default)

Docker Compose rebuild + up. Used for Dockerized services.

```yaml
repos:
  anatolykoptev/ox-browser:
    compose_path: /home/krolik/deploy/krolik-server
    source_path: /home/krolik/src/ox-browser
    services: [ox-browser]
    profile: go-cmd
```

### `binary`

`git pull` + custom build command + `systemctl --user restart`. Used for native Go binaries managed by systemd.

```yaml
repos:
  anatolykoptev/dozor:
    kind: binary
    source_path: /home/krolik/src/dozor
    build_cmd: [go, build, -o, /home/krolik/.local/bin/dozor, ./cmd/dozor]
    user_services: [dozor]
    smoke_url: http://localhost:8765/health
    profile: go-cmd
```

### `static`

Executes a custom bash script for static sites (Astro, Vite, Next static export). The script receives:
- `DEPLOY_REPO_PATH` — absolute path to the local git checkout (`source_path`)
- `DEPLOY_SHA` — commit SHA from the webhook

A non-zero exit code marks the deploy as failed.

```yaml
repos:
  anatolykoptev/krolik-tools-site:
    kind: static
    source_path: /home/krolik/sites/krolik-tools-site
    static_deploy_script: /home/krolik/bin/site-deploy-krolik-tools.sh
    branch: master
```

Minimal script example (`/home/krolik/bin/site-deploy-krolik-tools.sh`):

```bash
#!/usr/bin/env bash
set -euo pipefail

cd "$DEPLOY_REPO_PATH"
git fetch origin
git checkout "$DEPLOY_SHA"

npm ci
npm run build

# Atomic swap: move built output to web root
rsync -a --delete dist/ /var/www/krolik-tools/ 
```


### Debounce Window

By default, back-to-back pushes to the same repo are coalesced into a single build using a 3-minute debounce window. Override globally with `DOZOR_DEFAULT_DEBOUNCE` (any Go duration, e.g. `5m`). Per-repo `debounce_seconds: N` always wins over the global default; set `debounce_seconds: -1` to opt out of debouncing entirely for a specific repo (immediate dispatch).
