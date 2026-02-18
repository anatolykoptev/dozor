---
name: incident-response
description: "Severity classification and response procedures for server incidents. AUTO-CONSULT when: receiving alerts, webhook notifications, watch-mode detects issues, or multiple services are unhealthy simultaneously."
---

# Incident Response

Classify severity, respond accordingly, and track resolution.

## Severity Levels

| Level | Meaning | Response Time | Auto-fix? |
|-------|---------|---------------|-----------|
| **P0 CRITICAL** | Service outage, data at risk, user-facing down | Immediate | Yes — restart, then verify |
| **P1 WARNING** | Degraded performance, threshold breach, unhealthy | Within minutes | Yes — targeted fix |
| **P2 INFO** | Minor anomaly, cosmetic, scheduled maintenance | Report only | No — inform user |

## Classification Rules

**P0 CRITICAL:**
- Any container in `exited` or `restarting` state
- Disk usage > 95%
- HTTP endpoint unreachable (piteronline down)
- Database (postgres/mariadb) down
- OOM kill detected in logs

**P1 WARNING:**
- Disk usage 80-95%
- Memory usage > 90%
- CPU load > 2x core count for > 5 min
- Error rate > 5 errors/min in any service
- SSL certificate expires in < 14 days
- Container restart count > 3

**P2 INFO:**
- Disk usage 60-80%
- Service uptime < 1 hour (recent restart)
- Non-critical container unhealthy (searxng, openlist)
- Pending system updates

## Response Flow

```
1. CLASSIFY — determine severity from triage/alert data
2. ACT (P0/P1):
   a. For P0: restart affected service immediately
   b. For P1: diagnose first (read_skill: diagnostics), then fix
   c. Verify fix: re-run server_inspect(mode: "health")
3. ESCALATE if:
   - Same service fails 3+ times
   - Fix requires code changes
   - Multiple P0 issues simultaneously (read_skill: escalation)
4. RECORD — update_memory with incident details
5. NOTIFY — response goes to Telegram via bus
```

## Auto-Fix Actions (Safe)

These actions are safe to take without human approval:

| Issue | Auto-fix |
|-------|----------|
| Container down | `server_restart(service: NAME)` |
| Systemd service down | `server_services(action: "restart", service: NAME)` |
| Piteronline service down | `server_remote(action: "restart", service: NAME)` |
| Disk > 90% | `server_cleanup(targets: ["journal", "docker", "tmp"], report: false, min_age: "7d")` |
| Docker build cache bloat | `server_prune(age: "48h")` |

## Actions Requiring Confirmation

Do NOT auto-execute:

- `server_cleanup(report: false)` with `targets: ["all"]`
- `server_prune(volumes: true)` — may delete data
- Full stack restart
- Any `server_exec` command that modifies data
- Deleting or stopping containers (only restart)

## Incident Record Format

After resolving, call `update_memory`:
```
Title: [Service] [Issue type] — [date]
Content:
- Symptoms: what was observed
- Root cause: why it happened
- Fix applied: what was done
- Verification: confirmed working
- Prevention: suggested change to avoid recurrence
```

## Multi-Incident Priority

When multiple issues arrive simultaneously:
1. P0 before P1 before P2
2. Data stores (postgres, mariadb) before application services
3. User-facing (piteronline, memdb-go) before internal tools
4. If overwhelmed (3+ P0): escalate to orchestrator with full triage
