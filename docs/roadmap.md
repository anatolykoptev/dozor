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

---

## Planned

### Phase 2: Enhanced Labels

Extend Docker label support for per-service monitoring configuration.

- `dozor.healthcheck.url` — custom HTTP health endpoint per container
- `dozor.logs.pattern` — custom error pattern for log analysis
- `dozor.alert.channel` — per-service alert routing (Telegram group, webhook, etc.)

### Phase 3: Service Groups & Dependencies

- `dozor.depends_on` — dependency graph for restart ordering
- `dozor.group` — aggregate health by group
- Dashboard view per group

### Phase 4: Knowledge Base Integration

Leverage the pluggable KB system (Phase 5.3) for persistent DevOps memory.

- Before auto-fix: search KB for similar past incidents and proven solutions
- After fix: save symptom-solution pair automatically
- Triage report enriched with "similar incidents" section
- Shared knowledge across agents (Dozor writes incidents, others write deploy outcomes and architecture decisions)

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
