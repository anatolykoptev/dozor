---
name: diagnostics
description: "Systematic server diagnostics methodology. AUTO-CONSULT when: investigating service failures, performance issues, or unknown errors. Guides the diagnostic process from triage through root cause analysis."
---

# Diagnostics Skill

Systematic approach to diagnosing server issues.

## Diagnostic Flow

```
1. TRIAGE (server_triage)
   → Quick health assessment of all services
   → Identifies which services need attention

2. INSPECT (server_inspect mode=health)
   → Detailed status of all Docker + systemd services
   → CPU, memory, uptime for each

3. ANALYZE (server_inspect mode=analyze, service=NAME)
   → Error pattern analysis for specific service
   → Groups errors by type, shows frequency

4. LOGS (server_inspect mode=logs, service=NAME)
   → Recent logs for specific service
   → Look for error messages, stack traces, OOM kills

5. SYSTEM (server_inspect mode=overview)
   → Disk, memory, load, top processes
   → Identifies resource constraints
```

## Common Patterns

### Service Won't Start
1. Check logs: `server_inspect mode=logs service=NAME`
2. Look for: missing config, port conflicts, dependency failures
3. Check dependencies: is the database/redis/rabbitmq up?
4. Try restart: `server_restart service=NAME`

### High Memory / OOM
1. Check overview: `server_inspect mode=overview`
2. Identify top consumers
3. Check specific service: `server_inspect mode=status service=NAME`
4. Consider: `server_prune` to free docker resources

### Cascading Failures
1. Triage first to see the full picture
2. Identify root cause (usually database or network)
3. Fix root cause BEFORE restarting dependents
4. Restart in dependency order: database → API → workers

### Disk Full
1. `server_inspect mode=overview` — check disk usage
2. `server_cleanup report=true` — see what can be freed
3. `server_prune` — clean docker build cache and images
4. If still critical → escalate to orchestrator

## Rules

- Always start with `server_triage` for a broad view
- Don't restart blindly — understand the error first
- After any fix, verify with another health check
- If the same service fails 3+ times, escalate — don't loop
