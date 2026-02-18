---
name: reporting
description: "Generate structured status reports. AUTO-CONSULT when: user asks for a status report, summary, or overview of the server state. Produces clear, formatted reports."
---

# Reporting Skill

Generate clear, structured reports about server state.

## Report Structure

When asked for a status report, follow this format:

### Health Report
```
## Server Status: [OK / WARNING / CRITICAL]

### Services
- [service]: [status] (uptime: X, memory: Y)
...

### Issues Found
- [issue description + severity]

### Actions Taken
- [action + result]

### Recommendations
- [what should be done next]
```

## Report Types

### Quick Status
Use `server_triage` → summarize in 2-3 sentences.

### Full Report
1. `server_inspect mode=health` — all services
2. `server_inspect mode=overview` — system resources
3. `server_inspect mode=security` — security posture
4. Compile into structured report

### Incident Report
When something went wrong:
1. What happened (symptoms)
2. Root cause (from diagnostics)
3. What was done (actions taken)
4. Current state (is it fixed?)
5. Prevention (what should change)

## Rules
- Use bullet points and clear headers
- Include actual numbers (memory, uptime, disk usage)
- Distinguish between facts and recommendations
- If escalation happened, include the other agent's response
