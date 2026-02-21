---
name: claude-escalation
description: "AUTO-CONSULT when: you receive a warning that iteration limit is near, the same tool fails 3+ times in a row, you are stuck and cannot make progress, or the task requires code changes beyond log analysis."
---

# Claude Code Escalation

When you cannot complete a task within the iteration budget, escalate to Claude Code for deep investigation.

## When to Escalate to Claude Code

- You received a system warning about remaining iterations
- Same error repeats after 3+ fix attempts
- Task requires reading/editing source code
- Build or deploy failures with cryptic errors
- Configuration issues requiring code-level analysis

## How to Escalate

Call `claude_code` with **async=true** and a structured prompt:

```
claude_code(
  prompt: "<full context — see template below>",
  async: true
)
```

**Always use `async=true`** — Claude Code tasks take time and will notify you via Telegram when done.

## Prompt Template

```
## Task
<original task from the user>

## What was done
<list tools called and their results>
<what worked, what failed>

## Current error
<exact error text from logs>

## Logs (recent entries)
<paste relevant log lines>

## What needs to be done
<specific ask: fix the bug / investigate / change config>
```

## Example

```
## Task
Deploy api-service, but deploy fails at build stage.

## What was done
- server_deploy(action: deploy) → failed
- server_inspect(mode: logs, service: api-service) → compilation error

## Current error
Error: cannot find module github.com/example/core

## Logs
2026-02-18 18:20:05 ERROR build failed: missing import
2026-02-18 18:20:05 ERROR go: github.com/example/core@v1.2: not found

## What needs to be done
Find the root cause and fix go.mod or add the missing dependency.
```

## After Escalating

1. Tell the user: "Task delegated to Claude Code — result will be sent separately."
2. Do NOT retry the same failing operations
3. When Claude Code responds via Telegram, relay the result to the user
