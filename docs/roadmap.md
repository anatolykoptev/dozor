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

Leverage the pluggable KB system for persistent DevOps memory.

- `KBSearcher` — programmatic (non-tool) KB client reusing `ClientManager` and `KBConfig`
- Triage enrichment: `ExtractIssues()` parses triage report, searches KB for similar past incidents, prepends results to agent prompt
- Auto-save: after resolving watch-originated incidents, saves triage+resolution pair to KB (fire-and-forget goroutine)
- Registry `Get()` method for extension lookup; `MCPClientExtension.KBSearcher()` getter
- Scoped to watch messages only — manual Telegram chats are not auto-saved

### Phase 5: Open-Source Hardening

Remove server-specific hardcoding to make Dozor a portable open-source tool.

**5.1 — Russian to English (Go code)**
- All user-facing strings translated to English
- Approval messages, processing ack, escalation prompt, max-iterations message
- Russian "da"/"net" preserved alongside English "yes"/"no" in approval parsing

**5.2 — Generic skill examples**
- 9 skill files updated: replaced all server-specific references (service names, paths, URLs) with generic placeholders

**5.3 — Pluggable knowledge base**
- User-facing tool names are `memdb_search` / `memdb_save` (internal Go types keep the generic `KB*` prefix)
- MCP backend is configurable via `DOZOR_KB_SERVER`, `DOZOR_KB_SEARCH_TOOL`, `DOZOR_KB_SAVE_TOOL`
- Backward compat with `DOZOR_MEMDB_USER` / `DOZOR_MEMDB_CUBE`
- Watch prompt conditionally references `memdb_search` only when KB is configured

**5.4 — Workspace cleanup**
- `workspace/MEMORY.md` shipped as empty template
- `workspace/AGENTS.md` uses generic topology, no hardcoded URLs
- `.env.bak` removed from repo, added to `.gitignore`
- `CLAUDE.md` already gitignored

**5.5 — Configuration documentation**
- `docs/CONFIGURATION.md` — comprehensive reference for all 50+ env vars
- `docs/INSTALL.md` — setup guide (binary, source, systemd, Docker)
- `.env.example` updated with all sections

**5.6 — Anti-hallucination fixes in analyze/dmesg (commit `bd6134e`)**

Three data-level bugs were letting consuming agents fabricate incident narratives from harmless signals:

- **`dmesg` OOM events gained timestamps.** `ParseDmesgOOM` now parses the `[Day Mon DD HH:MM:SS YYYY]` prefix from `dmesg --ctime`. `FormatOOMReport` emits a STATUS banner (`ACTIVE | RECENT | HISTORICAL | STALE | UNKNOWN-AGE`) based on the age of the most recent event, plus a per-event `Xh ago` marker. Agents can no longer mistake an 11-hour-old chrome OOM for a current incident.
- **Rate-limit regex tightened.** The previous `(?i)(rate.?limit|too many requests|429)` matched the substring `429` inside nanosecond timestamps like `073474290Z`, firing "Rate limiting triggered" warnings on benign 200 OK `gin_logger` lines. Now requires word-bounded `rate.?limit` phrases or `429` in explicit HTTP-status context (`HTTP/1.1 429`, `status: 429`, `"status": 429`). Regression test guards the nanosecond case.
- **Per-service noise rules.** New `noiseRule` type with per-service scoping and a curated `noiseRules` list. Entries are surfaced in `AnalyzeResult.NoiseHits` with a human-readable reason plus a criterion for when the pattern WOULD be a real incident. Current rules: cliproxyapi round-robin LLM key rotation (502/500 sub-second), cloakbrowser ARM Chromium hardware probing (`SharedImageManager::ProduceSkia`, `IPH_ExtensionsZeroStatePromo`, `gles2_cmd_decoder_passthrough`), go-code background re-index transient embed-jina failure. `FormatAnalysis` shows a dedicated `Known noise (NOT incidents)` section so suppressed lines are visible, not silently dropped.
- `FormatAnalysisEnriched` now filters noise before passing entries to timeline and clustering — the old version showed "Errors: 0" in the header while the timeline below reported 13, a contradiction that pushed agents toward the louder wrong signal.
- 13 new tests: ctime parsing, single-digit day padding, all freshness classifications, the 429 regression, per-service noise scoping.

**5.9 — Memory Architecture v2 (commits `adeb065`, `77d2e19`, `2bb64fa`, `02014e3`, `e140d5a`)**

Five sub-phases, each landed as its own commit and reviewed two-stage (spec + code quality) via subagent-driven execution. Plan: `~/docs/superpowers/plans/2026-04-11-dozor-phase6-memory-architecture.md`.

- **5.9.1 Schema validator for `memdb_save`** (`adeb065`). New `ValidateSavePayload` gates both `kbSaveTool.Execute` and `KBSearcher.Save`. Rejects: empty content, raw `user:/assistant:` dialog transcripts, numeric vital claims (swap/ram/load/cpu/disk/gpu %) lacking a tool output citation, and incident-structured content (Incident/Symptom/Root cause/Fix/Resolution/Prevention markers) without a non-empty `Evidence:` field. Schema rejections bypass the circuit breaker — they are caller-side bugs, not transient backend failures. 7 new tests; regex uses `\r?\n` for CRLF tolerance.
- **5.9.2 `memdb_delete` MCP tool** (`77d2e19`). New user-facing tool with required `memory_ids` + `reason` parameters. Forwards to MemDB's native `delete_memory` with the cube-scoped `user_id` so cross-tenant deletes are impossible. Every call is logged via `slog.Info` with IDs and reason for audit. 7 new tests cover name/description/schema + all argument-validation edge cases.
- **5.9.3 Retry wrap for `KBSearcher.Save`** (`2bb64fa`). Up to 3 attempts with exponential backoff (200ms → 400ms → 800ms, capped at 2s) and ±20% jitter via `math/rand`. Schema rejections bypass retry entirely. Circuit breaker failure counter is only bumped on the final attempt so a single backend hiccup no longer trips the breaker. Context cancellation is honored between attempts.
- **5.9.4 60s TTL cache for `memdb_search`** (`02014e3`). New `searchCache` wraps `kbSearchTool.Execute`. Cache key is SHA-256 of `(query, user_id, cube_id, top_k)`. 60-second TTL, 256-entry FIFO eviction. Session-local — never bleeds into system prompt or across restarts. Regression test `TestSearchCache_OrderSyncAfterExpiry` guards a subtle bug where expired `get()` removals would have left stale keys in the `order` slice and broken FIFO eviction. 6 tests total.
- **5.9.5 Startup memory snapshot** (`e140d5a`). `BuildSystemPrompt` accepts an optional `*mcpclient.KBSearcher`. When non-nil, runs a single `memdb_search("infrastructure state services configuration architecture", top_k=5)` at agent startup and injects the result as a fourth prompt section wrapped in `<startup_snapshot source="memdb_search">` tags with a disambiguation comment. Replaces the raw `MEMORY.md` bootstrap load removed in Phase 5.7 — semantic filtering surfaces only the top-5 most relevant facts, and every entry has passed the Phase 5.9.1 validator. Required a restructure of `buildAgentStack` so loop construction is deferred until after extensions load (chicken-and-egg: the searcher only exists once the `mcpclient` extension is up). Watch mode passes `nil` — the per-tick snapshot cost would add network latency to every health probe. 3 new tests.

Full test suite: 440/440 across 19 packages. Phase 6.2 (cube separation: `dozor-facts` / `dozor-incidents` / `dozor-anti-patterns`) is deferred because it requires fixing the `user_name`/`cube_id` column mismatch in `/home/krolik/src/MemDB` — separate repo, separate plan.

**5.8 — Self-Process Awareness (commit `515822b`)**

Prevents the agent from mistaking its own footprint or live user sessions for foreign system load. Motivated by the 2026-04-11 incident where the agent saw `load 51` and proposed killing `claude`/`windsurf`/`code-review-graph` processes to "free memory" — these were the user's active interactive sessions.

- **Process tagging in `server_inspect mode=overview` top-processes list.** `classifyProcess(line)` tags each `ps aux` row as `[user-session]`, `[agent-self]`, `[build]`, or untagged. `tagTopProcesses(raw)` post-processes the raw `ps aux` output and appends tags; un-tagged lines stay un-tagged. Categories:
  - `[user-session]` — `claude`, `claude-code`, `windsurf(-server)`, `code-review-graph update|build|serve`, `cursor(-server)`. **Never kill these.**
  - `[agent-self]` — `docker compose logs|stats|ps` (including the plugin path `docker-compose compose logs`), `docker stats`, `journalctl --follow`. Own telemetry gathering.
  - `[build]` — `/usr/local/go/pkg/tool/.+/compile`, `/tmp/go-build`, `cargo build|run|test|check`, `rustc`, `docker compose build`, `npm|pnpm|yarn run build`, `du -h -d`, `tar -c`.
- **`LOAD SOURCE` banner** — when strictly more than half of the top slots are tagged, the overview emits `LOAD SOURCE: N of M top processes are agent-self, user-session, or build activity — NOT a foreign incident. Do NOT kill tagged processes.`
- **Swap reality check** — `overviewWriteMemory` now handles three cases explicitly: `Swap: 0B/0B/0B` → "not configured on this host"; configured but zero usage → "N configured, 0 in use"; actual usage → "in use: X of Y total". Previously only the last case was annotated, and silent `Swap: 0B` rows let agents fabricate a percentage from nothing.
- **Hard block on user-session kills** in `validation.go` `IsCommandAllowed`: `(kill|pkill|killall)[^&|;]*(claude|claude-code|windsurf(-server)?|code-review-graph|cursor(-server)?)`. Regular kills (`kill -15 12345`, `pkill -f old-worker`) remain allowed.
- 12 new tests; engine package 181/181; full suite 417/417.

**5.7 — Memory contamination loop break (commit `c7d794c`)**

Root cause of the 2026-04-11 "TLS BLOCKED / hosting lockdown" hallucination episode: `cube=devops` had become a feedback loop, not a knowledge base. Every webhook alert was auto-saved as a "memory", then the next webhook hydrated that as "context", and the agent wove a louder and louder narrative on top of its own prior confabulations.

- **Webhook-sourced messages bypass KB enrichment and KB auto-save.** New `isWebhookSourced(msg)` predicate (`SenderID="webhook"`, `ChatID="webhook"`, or `Channel="internal"`). Raw alert payloads are telemetry, not questions or resolutions.
- **`isWorthSaving` tightened.** Previous fallback saved any response longer than 200 characters, which meant ~every response hit the KB. Replaced with a strong-indicator list requiring explicit resolution language (`✅ Fixed`, `✅ Restarted`, `✅ Deployed`, `Root cause:`, `Resolution:`, `Prevention:`). Generic substrings like `docker` and `systemctl` are no longer sufficient.
- **LLM-facing tool names renamed back to `memdb_search` / `memdb_save`.** The earlier `kb_*` rename created a documentation/code split where `~/.dozor/IDENTITY.md` still referenced the original `memdb_*` names, so the agent tried to call non-existent tools, failed silently, and fell back to raw `MEMORY.md` loading — the contamination vector described above. Internal Go types keep the `KB*` prefix (`KBConfig`, `KBSearcher`, `kbSearchTool`) to avoid churn.
- **`MEMORY.md` removed from `bootstrapFiles`.** Loading the entire file into every session's system prompt encouraged inheritance of stale or fabricated "recurring patterns" from months-old entries, and `update_memory` grew it unboundedly via blind append. Still available on demand via `read_memory`; the canonical incident store is MemDB.
- **`KBSearcher.Save` returns `ErrKBUnavailable` instead of `nil` when the circuit breaker is open.** Previously callers thought a save had succeeded when nothing was persisted — silent data loss.
- **`(*KBConfig).applyDefaults()` helper** deduplicates the defaulting logic between `RegisterKBTools` and `NewKBSearcher`.
- Direct-dependency bumps: `go-kit v0.16.0 → v0.18.0` (adds `503`, `504`, `connection refused` to transient error patterns — fixes 3 failing `TestIsTransientTelegramError` cases), `a2a-go 0.3.6 → 0.3.13`, `go-session 0.3.0 → 0.4.0`, `go-stt pseudo → v0.2.0`, `modelcontextprotocol/go-sdk 1.4.1 → 1.5.0`.
- One-time cleanup: 46 of 47 entries in `cube=devops` were fabricated TLS-block narratives, chat exports, or stale triage snapshots. Deleted via the native `delete_memory` endpoint. Only the Claude Code CLI auth architecture note was kept.

---

## Planned

### Phase 6.2: Cube separation (deferred — requires MemDB changes)

Split `cube=devops` into three cubes with different persistence semantics:
- `dozor-facts` — permanent, rarely-changing infra truths. Hand-curated, no auto-save.
- `dozor-incidents` — resolved incidents with Phase 5.9.1 schema. Auto-expire after 90 days.
- `dozor-anti-patterns` — permanent gravestones: confirmed false-positive patterns, "do not act on this" warnings.

`memdb_search` default query reads from facts + incidents + anti-patterns; explicit `historical=true` flag is required to search expired incidents. Blocker: the `user_name` vs `cube_id` column mismatch in `/home/krolik/src/MemDB` must be fixed first — the write path stores `cube_id` as the `user_name` property while the read path filters by `user_id`, so writes and reads only agree when the two IDs are equal, which blocks the three-cube split. Tracked separately; belongs to a MemDB-repo plan.

### Phase 8: Webhook Alert Pipeline

The `/webhook` handler today funnels alerts through the general LLM loop, which is the main reason one 502 turned into a "hosting lockdown" narrative. Replace with a deterministic short-circuit for a bounded set of alert shapes.

- **Typed alert payloads**: healthcheck monitors post structured JSON (`service`, `issue`, `severity`, `evidence`, `source`), not free text.
- **Deterministic dispatcher**: for each alert type, a falsifying probe is run first (see the Phase 6.1 evidence requirement). If the probe disproves the alert, the alert is dropped with a logged reason — no LLM invocation, no memory save, no Telegram spam.
- **Escalation budget**: if the same alert fires >N times in a window after being falsified, escalate to the LLM agent for deeper analysis with the full falsification history as context.
- **Raw-text fallback is gated**: free-text webhooks still work but are marked `SenderID="webhook-legacy"` and go through the LLM loop WITHOUT memory auto-save (already true after Phase 5.7). Legacy webhooks should be migrated away.

### Phase 9: Runbooks

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
- LLM can deviate from a runbook if the situation requires it — and must cite the evidence for the deviation
- Runbook outcomes saved to `dozor-incidents` cube with the Phase 6.1 schema
- Skills system loads runbooks as structured prompts

### Phase 10: Investigation Chains & Confidence

Deep diagnosis before acting.

- Composite `deep_investigate(service)` tool — runs: logs, error patterns, dependency check, recent deploys, resource trends, past incidents from `dozor-incidents`.
- Confidence-based escalation:
  - High (OOM with fresh dmesg timestamp, restart loop confirmed via `docker_ps`): auto-fix
  - Medium (unknown error pattern, ambiguous evidence): investigate deeper, then decide
  - Low (unclear pattern, partial evidence): report only, ask human via Telegram, never act
- Every escalation includes a quoted evidence block — the reason for the confidence level.

### Phase 11: Smart Watch

- Adaptive intervals — increase frequency after issues, back off when stable
- Watch history — store last N triage results in working memory (Phase 6.5 cache), detect flapping services
- Cooldown per service — don't report the same issue within a configurable window
- Pairs with Phase 7's self-process tagging: when a build is in progress, watch tick is suppressed entirely rather than just reporting the build as a warning

### Phase 12: Incident Timeline

- Correlate events across services (restart, error spike, OOM)
- Timeline view: what happened in the last N minutes across all services
- Root cause suggestions based on event ordering
- Sourced from `dozor-incidents` cube + live docker events

### Phase 13: Multi-Server

- Unified triage across local + remote servers
- Cross-server dependency tracking (e.g. DB on remote, app on local)
- Aggregate health view
- Per-server MemDB cubes (`dozor-incidents-<host>`) so a remote-host incident doesn't pollute the local reasoning context

---

## Priority order

1. **Phase 8 — Webhook Alert Pipeline.** Largest remaining reliability gap. Webhook handler currently funnels raw alerts through the full LLM loop, and even with Phase 5.7 / 5.9 hardening the alert→narrative surface area is wider than it needs to be. Deterministic dispatcher with per-alert-type falsifying probes eliminates the entire class of "alert triggered an investigation that fabricated a narrative".
2. **Phase 6.2 cube separation** — blocked on MemDB repo changes; track in a separate plan.
3. Phase 9 → 13 — features on top of a stable foundation.
