# Dozor vs Alternatives

Competitive analysis based on GitHub research (February 2026). All data verified against actual repositories.

## Category Map

Dozor sits at the intersection of three categories that no single tool covers:

```
                    AI-native (MCP/LLM)
                         │
                    ┌────┼────┐
                    │  DOZOR  │
                    └────┼────┘
                   ╱     │     ╲
        Docker Monitoring │  Server Management
              │           │          │
         Dozzle      DockMon    Portainer
       QuantGeekDev/   mcp-system-
        docker-mcp     monitor
```

## Full Comparison

| Feature | **Dozor** | Portainer | Dozzle | DockMon | docker-mcp | mcp-system-monitor |
|---------|-----------|-----------|--------|---------|------------|-------------------|
| **GitHub** | anatolykoptev/dozor | portainer/portainer | amir20/dozzle | darthnorse/dockmon | QuantGeekDev/docker-mcp | DarkPhilosophy/mcp-system-monitor |
| Stars | — | 36.6k | 11.7k | 1.2k | 448 | 4 |
| Language | Go | TypeScript | Go | Python | Python | Rust |
| License | MIT | Zlib | MIT | MIT | MIT | MIT |
| **Interface** | MCP (AI-native) | Web UI + API | Web UI | Web UI | MCP | MCP + REST |
| **Target user** | AI agents (LLM) | Humans (GUI) | Humans (GUI) | Humans (GUI) | AI agents | AI agents |

### Monitoring Capabilities

| Feature | **Dozor** | Portainer | Dozzle | DockMon | docker-mcp | mcp-system-monitor |
|---------|-----------|-----------|--------|---------|------------|-------------------|
| Docker containers | Yes (SDK + events) | Yes (SDK) | Yes (SDK) | Yes (SDK) | Yes (SDK) | No |
| Auto-discovery | Yes (compose + all) | Yes | Yes | Yes | List only | No |
| Container events | Yes (real-time watcher) | Yes | Yes | Yes | No | No |
| CPU/memory stats | Yes | Yes | Yes (live) | Yes | Yes (`stats`) | Yes (procfs) |
| Container logs | Yes (parsed + filtered) | Yes | Yes (real-time) | Yes | Yes | No |
| Systemd services | Yes (user + system) | No | No | No | No | No |
| Disk/load/overview | Yes | Partial | No | No | No | Yes |
| Network connections | Yes (TCP/UDP by state) | No | No | No | No | Yes (interface stats) |
| Cron/timers | Yes | No | No | No | No | No |
| Remote servers (SSH) | Yes | Yes (agent) | Yes (agent) | Yes (agent) | No | No |
| TLS cert scanning | Yes | No | No | No | No | No |
| Port audit | Yes | No | No | No | No | No |

### Intelligence & Automation

| Feature | **Dozor** | Portainer | Dozzle | DockMon | docker-mcp | mcp-system-monitor |
|---------|-----------|-----------|--------|---------|------------|-------------------|
| Error pattern analysis | Yes (6 categories) | No | No | No | No | No |
| Auto-triage | Yes (one-call diagnosis) | No | No | No | No | No |
| LLM agent loop | Yes (tool-calling) | No | No | No | No | No |
| Auto-remediation | Yes (via LLM) | No | No | Auto-restart only | No | No |
| Security audit | Yes (ports, mounts, bots) | Partial | No | No | No | No |
| Deployment (bg) | Yes (non-blocking) | Yes | No | No | No | No |
| Binary updates | Yes (~60 CLIs) | Self-update | No | No | No | No |
| Dev mode | Yes (observe-only + exclusions) | No | No | Blackout windows | No | No |

### Architecture

| Feature | **Dozor** | Portainer | Dozzle | DockMon | docker-mcp | mcp-system-monitor |
|---------|-----------|-----------|--------|---------|------------|-------------------|
| Protocol | MCP (streamable-http + stdio) | REST API + WebSocket | HTTP + SSE | REST API + WebSocket | MCP (stdio) | MCP + REST |
| Multi-channel | MCP + Telegram + A2A + Webhook | Web only | Web only | Web + Email/Slack | MCP only | MCP + REST |
| Extension system | Yes (pluggable) | Yes (plugins) | No | No | No | No |
| A2A protocol | Yes (agent-to-agent) | No | No | No | No | No |
| Skills system | Yes (10 bundled skills) | No | No | No | No | No |
| Command approval | Yes (Telegram-based) | No | No | No | No | No |
| LLM escalation | Yes (→ Claude Code) | No | No | No | No | No |
| Docker labels | Yes (dozor.enable/name/group) | Yes | No | No | No | No |
| Zero-config | Yes (auto-detect compose) | Partial | Yes | Partial | No | No |

## Key Insights

### Dozor's unique position
No existing tool combines MCP protocol + LLM agent loop + server-wide monitoring (Docker + systemd + remote + system). The closest competitors fall into two camps:

1. **Human-first GUI tools** (Portainer, Dozzle, DockMon) — powerful dashboards, but their output is HTML/WebSocket, not LLM-consumable. An AI agent can't call Portainer to triage a server.

2. **MCP Docker tools** (docker-mcp, mcp_docker) — expose Docker operations via MCP, but only container CRUD. No triage, no log analysis, no systemd, no security audit, no auto-remediation.

3. **MCP system monitors** (mcp-system-monitor) — expose system metrics via MCP, but no Docker awareness, no log analysis, no agent loop, 2-4 stars, minimal adoption.

### What competitors do better
- **Portainer**: GUI, multi-cluster, Kubernetes support, team RBAC, app templates
- **Dozzle**: Real-time log streaming UI, multi-host with agents, Swarm + K8s support
- **DockMon**: Blackout windows UI, mTLS for remote agents, drag-and-drop dashboard

### What dozor should learn from
- **DockMon's blackout windows** → Dozor's dev mode + exclusions already cover this, but could add scheduled windows
- **Dozzle's multi-host agent** → Dozor has SSH remote, but a lightweight agent binary could be better
- **Portainer's app templates** → Dozor could offer one-click service deployment recipes

---

## Dev Mode: Design Decision

| Approach | Mechanism | Persistence | Granularity |
|----------|-----------|-------------|-------------|
| **Dozor dev mode** | In-memory atomic bool + sync.Map | None (intentional) | Global toggle + per-service with TTL |
| DockMon blackout windows | Scheduled time ranges | Config file | All services during window |
| Config-file exclusions | Edit `.env` + restart | Disk | Service list only |
| Docker labels (`dozor.skip`) | Labels on containers | Docker metadata | Per-container, permanent |

### Why in-memory?

Dev mode is inherently temporary. Persisting it creates the problem it solves — forgotten state that causes unexpected behavior. In-memory with auto-expiring TTLs (default 4h) means:

- No stale config files to clean up
- Restart = clean slate (safe default)
- Per-service exclusions auto-expire even if the developer forgets
- Zero disk I/O, zero config format to maintain

---

## Discovery: Implementation Comparison

| Method | Used by | Pros | Cons |
|--------|---------|------|------|
| Docker Go SDK | Dozor, Portainer, Dozzle, DockMon | Native types, events, connection pooling, labels | Local socket only |
| CLI (`docker ps`) | Dozor (SSH fallback) | Works over SSH, no dependencies | Subprocess per call, no events |
| Docker Events (SDK) | Dozor, Dozzle, Portainer | Real-time cache invalidation | Requires persistent connection |
| Agent binary | Dozzle, DockMon, Portainer | Secure remote without exposing Docker socket | Extra deployment |

Dozor uses Docker Go SDK locally with CLI fallback for SSH remote — best of both worlds.
