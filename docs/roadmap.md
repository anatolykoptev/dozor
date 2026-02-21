# Dozor Roadmap

## Phase 0: Dev Mode (done)

Runtime-toggleable dev mode to prevent auto-fixing during active development.

### Delivered
- `server_dev_mode` MCP tool — toggle mode, manage exclusions, show status
- Global observe-only watch (atomic bool on `ServerAgent`)
- Per-service triage exclusions with auto-expiring TTL (default 4h)
- Triage filters excluded services, adds DEV MODE banner
- Watch prompt switches from "take corrective action" to "observe only"
- Unit tests for toggle, exclusions, and auto-expiry

### Files
- `internal/engine/agent.go` — devMode + devExclusions state and methods
- `internal/engine/triage.go` — exclusion filter + banner
- `internal/engine/inputs.go` — DevModeInput struct
- `internal/engine/dev_mode_test.go` — 3 tests
- `internal/tools/dev_mode.go` — MCP tool handler
- `internal/tools/register.go` — wired up
- `cmd/dozor/gateway.go` — watch prompt switch

## Phase 1: Docker Go SDK (done)

Replace CLI-based discovery with Docker Go SDK for local mode.

### Changes
- **discovery.go** — SDK-based `DiscoverContainers()` with caching (30s TTL)
- **watcher.go** — `ContainerWatcher` listens to Docker events, invalidates cache
- **status.go** — SDK-based `GetContainerStatus()` with label parsing
- **agent.go** — Initialize SDK client + watcher in `NewAgent()`
- **systemd.go** — `DiscoverUserServices()` auto-scans active user units
- **tools/services.go** — Use discovery when `DOZOR_USER_SERVICES` is empty

### Label support
- `dozor.enable` — `true`/`false` opt-in/opt-out
- `dozor.group` — grouping in health output
- `dozor.name` — display name override

### Fallback chain
1. `DOZOR_SERVICES` env var (explicit override)
2. Docker SDK discovery (local) / `docker compose ps` CLI (remote/SSH)
3. All running containers via SDK (if no compose project)

## Phase 2: Enhanced Labels

- `dozor.healthcheck.url` — custom HTTP health endpoint
- `dozor.logs.pattern` — custom error pattern for log analysis
- `dozor.alert.channel` — per-service alert routing

## Phase 3: Service Groups & Dependencies

- `dozor.depends_on` — dependency graph for restart ordering
- `dozor.group` — aggregate health by group
- Dashboard view per group

## Phase 4: MemDB Integration (shared DevOps knowledge)

Connect Dozor to MemDB for persistent memory across incidents.

### Core
- Dedicated `devops` user/cube in MemDB — shared with Claude Code and Vaelor
- Before auto-fix: search MemDB for similar past incidents and proven solutions
- After fix: save symptom→solution pair automatically
- Triage report enriched with "similar incidents" section

### Shared knowledge base
- Dozor writes: incidents, resolutions, error patterns, capacity trends
- Vaelor DevOps writes: deploy outcomes, config changes, infra decisions
- Claude Code writes: architecture decisions, debugging insights
- All three read from the same `devops` cube

### Config
- `DOZOR_MCP_SERVERS=go_search=...,memdb=http://127.0.0.1:8001/mcp`
- `DOZOR_MEMDB_USER=devops`
- `DOZOR_MEMDB_CUBE=devops`

## Phase 5: Open-Source Hardening

Remove server-specific hardcoding to make Dozor a truly portable open-source tool.

### 5.1 Russian → English (Go code)
All user-facing strings in Go code are in Russian. Translate or add i18n.
- `cmd/dozor/gateway.go` — approval messages ("Команда одобрена"), processing ack ("Принял, обрабатываю"), escalation prompt, max-iterations message
- `internal/tools/exec.go` — command approval request, rejection message

### 5.2 Generic skill examples
Skills reference specific services (memdb-api, searxng, go-hully, vaelor, piteronline). Replace with generic placeholders.
- `skills/service-dependencies/SKILL.md` — full Docker stack listing
- `skills/capacity-planning/SKILL.md` — memory baselines for specific services
- `skills/incident-response/SKILL.md` — "piteronline" as user-facing service
- `skills/deployment/SKILL.md` — `/home/krolik/krolik-server` path
- `skills/escalation/SKILL.md` — memdb-api, postgres examples
- `skills/dev-mode/SKILL.md` — memdb-api, go-hully exclusion examples
- `skills/claude-escalation/SKILL.md` — Russian template, memdb-api reference
- `skills/remote-server/SKILL.md` — "Piteronline" → generic "Remote Server"

### 5.3 MemDB → generic knowledge base
MemDB integration works but assumes a specific product. Make it pluggable.
- Rename `memdb_search`/`memdb_save` → `kb_search`/`kb_save` (knowledge base)
- Abstract the MCP call layer so any MCP server with search/add tools can be a knowledge backend
- Config: `DOZOR_KB_SERVER`, `DOZOR_KB_SEARCH_TOOL`, `DOZOR_KB_SAVE_TOOL` (defaults to MemDB tool names)
- Watch prompt: reference `kb_search` only if KB server is configured
- Change defaults from `"devops"` to `"default"` or empty

### 5.4 Workspace cleanup
- `workspace/MEMORY.md` — ship as empty template, not pre-populated
- `workspace/AGENTS.md` — remove hardcoded URLs/ports, use env-based examples
- Remove `.env.bak` from repo (keep `.env.example` only)
- `CLAUDE.md` — add to `.gitignore` (project-specific, not for distribution)

### 5.5 Config documentation
- Add comprehensive `CONFIGURATION.md` listing all `DOZOR_*` env vars with descriptions
- Ensure every hardcoded default in `config.go` is documented
- Add `INSTALL.md` with generic setup instructions (Docker, systemd, binary)

## Phase 6: Runbooks (executable playbooks)


Structured multi-step procedures for known scenarios.

### Format
```yaml
name: postgres-slow
trigger: service=postgres, error_pattern="slow query|lock timeout"
steps:
  - tool: server_container_exec
    args: {container: postgres, command: "pg_stat_activity active queries"}
  - tool: server_inspect
    args: {mode: logs, service: postgres, filter: "slow query"}
  - decide:
      if: active_queries > 10
      then: kill_long_queries
      else: check_connection_pool
```

### Capabilities
- Auto-triggered from triage when error patterns match
- LLM can deviate from runbook if situation requires it
- Runbook outcomes saved to MemDB for learning
- Skills system loads runbooks as structured prompts

## Phase 7: Investigation Chains & Confidence

Deep diagnosis before acting.

### Investigation chains
- Composite `deep_investigate(service)` tool
- Runs: logs → error patterns → dependency check → recent deploys → resource trends
- Returns enriched context for LLM to make better decisions

### Confidence-based escalation
- Triage output includes confidence score per issue
- High confidence (OOM, restart loop): auto-fix
- Medium (unknown errors): investigate deeper, then decide
- Low (unclear pattern): report only, ask human via Telegram

## Phase 8: Smart Watch

- Adaptive watch intervals — increase frequency after detecting issues, back off when stable
- Watch history — store last N triage results, detect flapping services
- Cooldown per service — don't report the same issue within a configurable window
- Dev mode auto-activate — detect active `docker compose build` / `go build` and suppress

## Phase 9: Incident Timeline

- Correlate events across services (restart → error spike → OOM)
- Timeline view: what happened in the last N minutes across all services
- Root cause suggestions based on event ordering

## Phase 10: Multi-Server

- Unified triage across local + remote servers
- Cross-server dependency tracking (e.g. DB on remote, app on local)
- Aggregate health view
