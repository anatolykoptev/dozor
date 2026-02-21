---
name: memory
description: "DevOps knowledge base: store and retrieve operational knowledge using MemDB. AUTO-CONSULT when: resolving an incident (search for similar past incidents first), after fixing a non-trivial issue (save the solution), during capacity reviews (check historical trends), or when user asks about past events."
---

# Memory (DevOps Knowledge Base)

Use `memdb_search` and `memdb_save` to build institutional knowledge about server operations.

## Tools

- **memdb_search(query)** — search past incidents, solutions, patterns
- **memdb_save(content)** — save new knowledge after resolving issues

## When to Search

### BEFORE acting on incidents (MANDATORY)
```
memdb_search(query: "<service name> <error pattern>")
```

Look for:
- Same service + same error → use the proven fix
- Same error pattern on different service → adapt the solution
- Recent fixes that might have caused this (regression)

### Before restarting services
```
memdb_search(query: "restart <service> reason")
```
Avoid repeating ineffective restarts.

### During capacity reviews
```
memdb_search(query: "disk growth" or "memory leak <service>")
```

## When to Save

### After resolving non-trivial incidents (MANDATORY)
```
memdb_save(content: "Incident: [service] [description]\nSymptom: [what was observed]\nRoot cause: [why it happened]\nFix: [exact commands/actions]\nPrevention: [how to prevent recurrence]")
```

### After discovering useful patterns
- New error pattern → solution mapping
- Service-specific configuration that works
- Capacity thresholds that cause problems

### After deploy outcomes
- Which services needed rebuild after what changes
- Deploy failures and their resolutions

## What NOT to Save

- Routine healthy triage results ("all OK")
- Temporary dev mode activations
- Information already in skills or documentation
- Duplicate of something already in the knowledge base

## Rules

1. **Always search before fixing** — past solutions save time and tokens
2. Save with enough context to be useful months later
3. Use structured format for incidents (symptom/cause/fix)
4. Don't duplicate — search first, update existing if needed
5. Keep memories actionable — "restart fixed it" is less useful than "OOM due to connection leak, restart + set pool_size=20 fixed it"
6. Prefer `memdb_save` over `update_memory` — MemDB is shared with Vaelor and Claude Code, MEMORY.md is local only
