# Dozor Roadmap

## Completed

### Phase 0: Dev Mode

Runtime-toggleable dev mode to prevent auto-fixing during active development.

- `server_dev_mode` MCP tool — toggle mode, manage exclusions, show status
- Global observe-only watch (atomic bool on `ServerAgent`)
- Per-service triage exclusions with auto-expiring TTL (default 4h)
- Triage filters excluded services, adds DEV MODE banner
- Watch prompt switches from "take corrective action" to "observe only"

### Phase 1: Docker Go SDK

Replace CLI-based discovery with Docker Go SDK for local mode.

- SDK-based `DiscoverContainers()` with caching (30s TTL)
- `ContainerWatcher` listens to Docker events, invalidates cache
- SDK-based `GetContainerStatus()` with label parsing
- `DiscoverUserServices()` auto-scans active user units
- Label support: `dozor.enable`, `dozor.group`, `dozor.name`
- Fallback chain: env var > SDK discovery > all running containers

### Phase 5: Open-Source Hardening

Remove server-specific hardcoding to make Dozor a portable open-source tool.

**5.1 — Russian to English (Go code)**
- All user-facing strings translated to English
- Approval messages, processing ack, escalation prompt, max-iterations message
- Russian "da"/"net" preserved alongside English "yes"/"no" in approval parsing

**5.2 — Generic skill examples**
- 9 skill files updated: replaced all server-specific references (service names, paths, URLs) with generic placeholders

**5.3 — Pluggable knowledge base**
- Renamed `memdb_search`/`memdb_save` to `kb_search`/`kb_save`
- MCP backend is now configurable via `DOZOR_KB_SERVER`, `DOZOR_KB_SEARCH_TOOL`, `DOZOR_KB_SAVE_TOOL`
- Backward compat with `DOZOR_MEMDB_USER`/`DOZOR_MEMDB_CUBE`
- Watch prompt conditionally references `kb_search` only when KB is configured

**5.4 — Workspace cleanup**
- `workspace/MEMORY.md` shipped as empty template
- `workspace/AGENTS.md` uses generic topology, no hardcoded URLs
- `.env.bak` removed from repo, added to `.gitignore`
- `CLAUDE.md` already gitignored

**5.5 — Configuration documentation**
- `docs/CONFIGURATION.md` — comprehensive reference for all 50+ env vars
- `docs/INSTALL.md` — setup guide (binary, source, systemd, Docker)
- `.env.example` updated with all sections

### Phase 2: Enhanced Labels

Extend Docker label support for per-service monitoring configuration.

- `dozor.healthcheck.url` — custom HTTP health endpoint per container, probed during triage/diagnose
- `dozor.logs.pattern` — custom error regex for log analysis (appended to built-in patterns)
- `dozor.alert.channel` — per-service alert routing hint, propagated to all alerts
- Labels flow: Docker → `DiscoveredContainer.Labels` → `ServiceStatus.Labels` → triage/analysis
- `ServiceStatus.DozorLabel()` helper mirrors `DiscoveredContainer.DozorLabel()`
- `IsHealthy()` and `GetAlertLevel()` updated to account for healthcheck state
- Healthcheck results shown in `FormatStatus`, `FormatReport`, and triage output
- SDK-only: CLI/SSH fallback gracefully ignores labels

### Phase 3: Service Groups & Dependencies

Label-driven service grouping and dependency management.

- `dozor.group` label — group services, aggregate health per group, grouped triage output
- `dozor.depends_on` label — dependency graph for automatic cascade restart ordering
- `server_inspect({mode: "groups"})` — dashboard view of all groups with health
- `GroupServices()` / `BuildDependencyGraph()` pure functions in `groups.go`
- `FormatGroups()` for dashboard rendering; `FormatReport()` renders by group when labels present
- `GenerateGroupAlerts()` — group-level alerts for degraded/critical groups
- BFS-based `Dependents()` with cycle safety and dangling-ref warnings
- Backward compatible: no labels = current flat behavior unchanged

### Phase 4: Knowledge Base Integration

Leverage the pluggable KB system (Phase 5.3) for persistent DevOps memory.

- `KBSearcher` — programmatic (non-tool) KB client reusing `ClientManager` and `KBConfig`
- Triage enrichment: `ExtractIssues()` parses triage report, searches KB for similar past incidents, prepends results to agent prompt
- Auto-save: after resolving watch-originated incidents, saves triage+resolution pair to KB (fire-and-forget goroutine)
- Registry `Get()` method for extension lookup; `MCPClientExtension.KBSearcher()` getter
- Scoped to watch messages only — manual Telegram chats are not auto-saved

---

## Planned

### Phase 6: Runbooks (executable playbooks)

Structured multi-step procedures for known scenarios.

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

- Auto-triggered from triage when error patterns match
- LLM can deviate from runbook if situation requires it
- Runbook outcomes saved to KB for learning
- Skills system loads runbooks as structured prompts

### Phase 7: Investigation Chains & Confidence

Deep diagnosis before acting.

- Composite `deep_investigate(service)` tool — runs: logs, error patterns, dependency check, recent deploys, resource trends
- Confidence-based escalation:
  - High (OOM, restart loop): auto-fix
  - Medium (unknown errors): investigate deeper, then decide
  - Low (unclear pattern): report only, ask human via Telegram

### Phase 8: Smart Watch

- Adaptive intervals — increase frequency after issues, back off when stable
- Watch history — store last N triage results, detect flapping services
- Cooldown per service — don't report the same issue within a configurable window
- Dev mode auto-activate — detect active `docker compose build` / `go build` and suppress

### Phase 9: Incident Timeline

- Correlate events across services (restart, error spike, OOM)
- Timeline view: what happened in the last N minutes across all services
- Root cause suggestions based on event ordering

### Phase 10: Multi-Server

- Unified triage across local + remote servers
- Cross-server dependency tracking (e.g. DB on remote, app on local)
- Aggregate health view
