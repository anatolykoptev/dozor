---
name: escalation
description: "Escalate problems to other agents in the network. AUTO-CONSULT when: you cannot resolve an issue after diagnosis, need human decision, encounter cross-domain problems, or need code-level investigation."
---

# Escalation Skill

When you encounter a situation you cannot fully resolve, escalate to the right agent.

## Agent Network

| Agent | When to escalate |
|-------|-----------------|
| **orchestrator** | Cross-domain issues, need human decision, status reports, coordination between multiple agents |
| **devops** | Code bugs, build failures, config changes, complex debugging beyond log analysis |

## Process

1. **DIAGNOSE FIRST** — always try to understand the problem yourself (server_triage, server_inspect).
2. **ATTEMPT FIX** — if the fix is safe (restart, cleanup), try it and verify.
3. **CHOOSE AGENT** — pick the right agent based on the table above.
4. **PROVIDE CONTEXT** — include in your a2a_call message:
   - What you found (triage results, error logs, metrics)
   - What you already tried (restarts, cleanups, their outcomes)
   - What you need from them (investigation, decision, fix)
5. **FOLLOW UP** — include the agent's response in your answer to the user.

## Examples

**Service crash loop → devops:**
```
memdb-api crashes in a restart loop. Logs show OOM at 512MB.
Already restarted 2x, same result.
Need: memory limit increase or code-level fix.
```

**Disk critical → orchestrator:**
```
Disk at 95%. Ran cleanup, freed 2GB, still at 91%.
Options: prune docker images (~8GB) or expand disk.
Need: decision on which approach.
```

**Cascading failure → orchestrator:**
```
postgres, memdb-api, memdb-go all unhealthy.
Root cause: postgres connection limit reached.
Need: coordinated restart plan, may affect other agents.
```

**Build error → devops:**
```
Deploy of krolik-server failed at build stage.
Error: go build — missing module github.com/foo/bar.
Need: code fix or go.mod update.
```

## When NOT to Escalate

- Simple restarts that fix the issue
- Routine health checks with all services healthy
- Cleanup/maintenance tasks within your capabilities
- Single service issues you can diagnose and resolve yourself
