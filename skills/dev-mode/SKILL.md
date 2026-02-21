---
name: dev-mode
description: "Dev mode workflow: when to activate observe-only watch, how to exclude services during development, and how to detect active development. AUTO-CONSULT when: same service restarts repeatedly in short window, triage shows errors typical of rebuilds (connection refused, image not found), user mentions they are developing/deploying, or watch detects issues right after a deploy."
---

# Dev Mode

Prevent auto-fixing services under active development.

## When to Activate

### Automatic signals (suggest to user or self-activate)

| Signal | Confidence | Action |
|--------|-----------|--------|
| Same service restarted 3+ times in 10 min | High | Exclude that service |
| Triage errors: "connection refused", "no such image", "build" | High | Suggest dev mode for that service |
| Deploy just completed (< 5 min ago) | Medium | Suggest excluding deployed services |
| Multiple services unhealthy simultaneously after being healthy | Medium | Suggest global dev mode |
| User says "I'm working on X" / "rebuilding X" | Certain | Exclude X immediately |

### Manual activation via MCP tool

```
server_dev_mode(enable: true)                             — global observe-only
server_dev_mode(exclude: ["my-service"], ttl: "2h")       — skip a service from triage
server_dev_mode(exclude: ["api-service", "worker"])       — skip multiple (default 4h TTL)
server_dev_mode(status: true)                             — check current state
```

## Dev Mode Behaviors

### Global dev mode ON
- Watch still runs triage on schedule
- Triage report includes `DEV MODE ACTIVE` banner
- Watch prompt changes to: "observe only, do NOT take any corrective action"
- Agent MUST NOT restart, deploy, or modify any services
- Agent CAN still diagnose, inspect, and report

### Per-service exclusions
- Excluded services are removed from triage entirely
- They still appear in `server_inspect(mode: "health")` (unfiltered)
- Exclusions auto-expire after TTL (default 4h)
- If all services excluded, triage says "All services dev-excluded"

## Decision Flow

```
Triage detects problems
  │
  ├─ Is dev mode ON?
  │   └─ YES → Report only. Do NOT fix. Done.
  │
  ├─ Is the problematic service excluded?
  │   └─ YES → Skip it. Not in triage output.
  │
  ├─ Does the error look like active development?
  │   │  (connection refused, image missing, build error,
  │   │   repeated restarts in short window)
  │   └─ YES → Suggest excluding the service:
  │        "Service X appears to be under active development
  │         (3 restarts in 5 min). Exclude from triage?"
  │        If via watch (no user): auto-exclude for 1h,
  │        log the decision.
  │
  └─ Normal incident → follow incident-response skill
```

## Development Error Patterns

These errors typically indicate development, not real incidents:

| Pattern | Likely cause |
|---------|-------------|
| `connection refused` on a service that was healthy | Container rebuilding |
| `no such image` or `image not found` | Docker build in progress |
| `bind: address already in use` | Old container not cleaned up during rebuild |
| Container restart loop with < 30s uptime | Code crash during development |
| Multiple services down simultaneously | `docker compose up -d` rebuilding stack |
| `go build` or `npm run` in recent exec logs | Active development session |

## P0 Emergency Override

Dev mode does NOT protect against real outages. The triage system automatically
re-includes excluded services if they are in a critical state (exited, dead, restarting).

### What gets overridden

| Service state | Excluded? | Result |
|--------------|-----------|--------|
| running + errors | Yes | Stays excluded (dev noise) |
| running + healthy | Yes | Stays excluded |
| **exited** | Yes | **Re-included with P0 OVERRIDE tag** |
| **dead** | Yes | **Re-included with P0 OVERRIDE tag** |
| **restarting** | Yes | **Re-included with P0 OVERRIDE tag** |

### Override behavior
- Triage output shows `P0 OVERRIDE — dev-excluded but DOWN: [services]`
- Global dev mode still changes the watch prompt to "observe only"
- But the agent SHOULD fix P0 overridden services even in dev mode:
  1. Check if the service was recently restarted (< 5 min ago) — if yes, likely dev rebuild, skip
  2. If not recently restarted — this is a real outage, apply incident-response skill
  3. After fixing, re-exclude the service to avoid future false positives

### Critical infrastructure (never fully exclude)
These service types should trigger P0 override even with global dev mode:
- Database (postgres, mysql, mariadb)
- Cache (redis — if used for sessions)
- Vector store (qdrant — data loss risk)

## Deactivation

Dev mode should be turned OFF when:
- User says "done developing" / "deploy finished"
- TTL expires (auto-expire, no action needed)
- Service has been stable for 30+ min after exclusion
- User explicitly calls `server_dev_mode(enable: false)`

Do NOT auto-deactivate global dev mode — only the user or TTL should do this.

## Rules

- When in doubt, observe rather than fix
- Never restart a service that was excluded — UNLESS it is P0 overridden (exited/dead)
- Always check dev mode status before executing auto-fixes from watch
- Log dev mode activations in memory for pattern recognition
- If dev mode has been on for > 4h, remind user it's still active
- P0 override trumps dev mode — a dead database is never "just development"
