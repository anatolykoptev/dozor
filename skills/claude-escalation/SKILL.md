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
## Задача
<original task from the user>

## Что уже сделано
<list tools called and their results>
<what worked, what failed>

## Текущая ошибка
<exact error text from logs>

## Логи (последние записи)
<paste relevant log lines>

## Что нужно сделать
<specific ask: fix the bug / investigate / change config>
```

## Example

```
## Задача
Задеплоить memdb-api, но деплой падает на билде.

## Что уже сделано
- server_deploy(action: deploy) → сбой
- server_inspect(mode: logs, service: memdb-api) → ошибка компиляции

## Текущая ошибка
Error: cannot find module github.com/memdb/core

## Логи
2026-02-18 18:20:05 ERROR build failed: missing import
2026-02-18 18:20:05 ERROR go: github.com/memdb/core@v1.2: not found

## Что нужно сделать
Найди причину и исправь go.mod или добавь зависимость.
```

## After Escalating

1. Tell the user: "Задача передана Claude Code — результат придёт отдельным сообщением."
2. Do NOT retry the same failing operations
3. When Claude Code responds via Telegram, relay the result to the user
