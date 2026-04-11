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

### Phase 6: Memory Architecture v2

The top priority. Phase 5.6 / 5.7 stopped the loop but the memory subsystem is still missing structure. The goal of Phase 6 is to turn MemDB from an ad-hoc chat log into a disciplined, queryable incident knowledge base that can't re-contaminate itself.

**6.1 — Structured incident schema for `memdb_save`**

The current save format is `"user: <q>\nassistant: <r>"` — unstructured chat. Replace with a typed payload:

```
Incident: <service> <symptom>
Evidence: <quoted tool output — commands + their results>
Root cause: <why it happened>
Fix: <exact commands/actions taken>
Prevention: <how to avoid recurrence>
Service: <canonical service name>
Timestamp: <RFC3339>
Session: <session id>
```

- Reject saves that lack `Evidence:` or have it empty.
- Regex guard on numeric claims: any mention of `swap X%`, `RAM X%`, `load X` must be accompanied by a quoted tool line (`free -h`, `uptime`, `top`) — otherwise the save is rejected.
- Tool description updated with an example and an explicit anti-pattern list.

**6.2 — Cube separation: facts / incidents / anti-patterns**

Today everything lives in one `cube=devops` bucket. Split into three:

- `dozor-facts` — permanent, rarely-changing infra truths (port assignments, schema names, user quirks). Hand-curated, no auto-save.
- `dozor-incidents` — resolved incidents with the Phase 6.1 schema. **Auto-expire after 90 days** via a MemDB cron or a `valid_until` field — stale incidents stop influencing context.
- `dozor-anti-patterns` — permanent gravestones: confirmed false-positive patterns, narratives that turned out to be wrong, "do not act on this" warnings. Populated by the user or by explicit post-mortem.

`memdb_search` default query reads from facts + incidents + anti-patterns; an explicit `historical=true` flag is required to search expired incidents. Requires fixing the `user_name` vs `cube_id` bug first: the write path stores `cube_id` as the `user_name` property while the read path filters by `user_id`; writes and reads only agree when the two IDs are equal, which blocks the split.

**6.3 — Startup memory snapshot**

Replace the removed `MEMORY.md` bootstrap load with a single semantic query at agent-loop start:

- `memdb_search("infrastructure state", top_k=5)` against `dozor-facts` cube.
- Inject the result as a `<startup_snapshot>` section in the system prompt.
- The file-based `MEMORY.md` stays as a local-only optional override for environments without MemDB.
- This restores "agent knows something at boot" without the stale-file-growing-forever problem.

**6.4 — `memdb_delete` MCP tool**

Expose MemDB's native `delete_memory` as a registered tool so the agent (or an operator via Telegram) can remove bad entries without direct DB access. Signature: `memdb_delete(memory_ids: [string], reason: string)`. The reason is required and logged. Used for the "this memory turned out to be wrong" workflow, which feeds Phase 6.2's anti-pattern cube.

**6.5 — go-kit/cache for working memory**

`go-kit/cache` (L1 in-memory + L2 Redis, S3-FIFO eviction) used to cache short-lived intermediate results within a session:

- Triage snapshots (so `server_triage` isn't re-run on every tick of the same incident).
- `memdb_search` results per query for 60 seconds (so repeated identical searches during one investigation hit cache).
- `server_probe` results per URL for 30 seconds.

All cached data is session-scoped — never loaded into the system prompt. This is working memory, not long-term memory.

**6.6 — `memdb_save` reliability**

- Wrap the Save call in `go-kit/retry` with exponential backoff + jitter.
- Surface `ErrKBUnavailable` distinctly in Telegram/log output so the user sees when writes were dropped by the circuit breaker.
- Log every `memdb_save` call to `journalctl --user -u dozor` with the content hash — when the KB goes bad, we can audit who wrote what.

### Phase 7: Self-Process Awareness

The 2026-04-11 episode also revealed a second class of false alarm: the agent mistook its own command footprint for "high system load" and proposed killing user processes to "free memory". Fix at the source — the overview report should flag who is generating the load.

- **Process tagging in `server_inspect mode=overview`**: top CPU / memory consumers get a `[agent-self]`, `[user-session]`, or `[build]` tag when they match one of:
  - `docker compose build | logs --tail | stats` — own telemetry gathering
  - `go compile` under `/tmp/go-build*` — ongoing Go build
  - `cargo build`, `rustc` under workspace — ongoing Rust build
  - `du -h -d`, `tar`, `find .` over the home directory
  - `claude --dangerously-skip-permissions`, `windsurf-server`, `code-review-graph update` — live user-driven sessions, **never kill**
- **Overview banner**: when >50% of the top CPU slots are tagged, prepend `LOAD SOURCE: own activity + user sessions — not an external incident`.
- **Hard block in `server_exec`**: any command matching `kill.*(-9)?.*(claude|windsurf|code-review-graph)` or `docker stop (claude|…)` is rejected with an explanatory error.
- **Swap reality check**: before emitting any "swap X%" claim, the formatter must see the actual `Swap:` line from `free`. If `Swap: 0B / 0B / 0B`, the report includes "no swap configured on this host" and refuses to interpolate a percentage.

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

1. **Phase 6.1 + 6.2 + 6.3 + 6.4** — the memory architecture is the bottleneck for everything else. Without schema, separation, and startup snapshot, every later phase feeds the same broken memory pool.
2. **Phase 7** — cheap, fast, eliminates a whole class of self-induced false alarms.
3. **Phase 6.5 + 6.6** — reliability polish on top of the new architecture.
4. **Phase 8** — turns webhook alerts from an LLM gamble into a deterministic pipeline.
5. Phase 9 → 13 — features on top of a stable foundation.
