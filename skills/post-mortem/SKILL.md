---
name: post-mortem
description: "Post-incident analysis: build timeline, identify root cause, document resolution, and extract prevention measures. AUTO-CONSULT when: incident is resolved, user asks for incident summary or root cause analysis, or after any P0 incident resolution."
---

# Post-Mortem

After every significant incident, build a structured record for future reference.

## When to Run

- After any P0 incident is resolved
- After any P1 incident that required 3+ tool calls to resolve
- After any cascading failure (2+ services affected)
- When user asks "what happened?" about a past event
- After a failed deployment that needed rollback or manual intervention

## Data Collection

Gather these before writing the post-mortem:

### 1. Timeline
```
server_inspect(mode: "logs", service: NAME, lines: 200)
```
Look for: first error timestamp, escalation points, recovery timestamp.

### 2. Current state
```
server_triage()
server_inspect(mode: "health")
```
Confirm the incident is actually resolved.

### 3. Impact scope
```
server_inspect(mode: "health")
```
Which services were affected? Which stayed healthy?

### 4. Root cause evidence
```
server_inspect(mode: "analyze", service: NAME)
server_inspect(mode: "overview")
```
Error patterns, resource state at time of incident.

## Post-Mortem Template

Write to memory using `update_memory`:

```
Title: [P0/P1] [Service] [Issue] — [YYYY-MM-DD]

## Timeline
- [HH:MM] First error detected: [what]
- [HH:MM] Triage triggered / alert received
- [HH:MM] Diagnosis: [finding]
- [HH:MM] Fix applied: [action]
- [HH:MM] Verified: [confirmation]
- Duration: [X min from detection to resolution]

## Impact
- Services affected: [list]
- Services unaffected: [list]
- User-facing impact: [yes/no, what]

## Root Cause
[One paragraph: what actually went wrong and why]

## Resolution
[What was done to fix it]

## What Worked
- [Things that helped: quick detection, right tool, etc.]

## What Didn't Work
- [Things that slowed down: wrong diagnosis, unnecessary restarts, etc.]

## Prevention
- [ ] [Concrete action to prevent recurrence]
- [ ] [Monitoring improvement]
- [ ] [Configuration change]
```

## Root Cause Categories

| Category | Example | Prevention |
|----------|---------|------------|
| Resource exhaustion | Disk full, OOM | Capacity monitoring, cleanup cron |
| Configuration | Wrong env var, bad port | Config validation in deploy skill |
| Dependency | Upstream service down | Health checks, circuit breakers |
| Code bug | Crash loop, memory leak | Escalate to devops for code fix |
| Infrastructure | Host reboot, network | Redundancy, monitoring |
| Human error | Wrong deploy, deleted data | Approval gates, backups |
| Development collision | Rebuild during watch | Dev mode exclusions |

## Pattern Recognition

After writing the post-mortem, check if this is a recurring issue:

```
read_memory
```

Look for similar past incidents. If the same service/issue appears 3+ times:
- Flag it in the post-mortem as **recurring**
- Recommend a structural fix (not just restart)
- Escalate to devops if it requires code changes

## Rules

- Always confirm resolution before writing post-mortem (re-check health)
- Be factual — record what happened, not what should have happened
- Include timestamps whenever possible
- Keep root cause to one paragraph — no speculation
- Prevention items must be concrete and actionable
- If root cause is unclear, say so — "Root cause: unknown, investigation needed"
