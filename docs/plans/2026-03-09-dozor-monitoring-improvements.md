# Dozor Monitoring Improvements

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Improve dozor's data quality, remove noise, replace custom code with mature libraries, fix oversized files.

**Architecture:** Incremental improvements to `internal/engine/` — no structural redesign, just targeted fixes.

**Tech Stack:** Go 1.26+, slog, failsafe-go, Docker SDK

---

### Task 1: Fix watcher.go — replace log with slog

**Files:**
- Modify: `internal/engine/watcher.go`

Replace all `log.Printf("[watcher] ...")` calls with `slog.Info(...)` for consistency with the rest of the codebase.
Remove `"log"` import, add `"log/slog"`.

### Task 2: Replace custom CircuitBreaker with failsafe-go

**Files:**
- Delete: `internal/engine/circuitbreaker.go` (145 lines)
- Delete: `internal/engine/circuitbreaker_test.go` (90 lines)
- Modify: `internal/mcpclient/kb.go` — use failsafe-go CB
- Modify: all files importing CircuitBreaker
- Modify: `go.mod` — add `github.com/failsafe-go/failsafe-go`

Replace custom `CircuitBreaker` (Allow/RecordSuccess/RecordFailure) with failsafe-go's `circuitbreaker` package.
Key advantage: built-in retry + fallback + timeout composability.

### Task 3: Improve triage output format for LLM consumption

**Files:**
- Modify: `internal/engine/triage.go` — add structured issue summaries
- Modify: `internal/engine/format.go` — cleaner service detail output

Add a compact structured summary at the top of triage output:
```
Issues: postgres(WARNING, 5 errors), go-code(ERROR, 1 restart)
```
Remove verbose "Services needing attention (N):" header noise.
Trim redundant error lines (keep max 3 per service, not 5).

### Task 4: Split oversized engine files

**Files:**
- Split: `internal/engine/config.go` (616 lines) → config.go + config_env.go + config_llm.go
- Split: `internal/engine/security.go` (617 lines) → security.go + security_audit.go + security_format.go
- Split: `internal/engine/updates.go` (757 lines) → updates.go + updates_parse.go + updates_format.go

Target: each file ≤200 lines. Extract formatters and parsers into separate files.

### Task 5: Clean noise from log_analyzer patterns

**Files:**
- Modify: `internal/engine/log_analyzer.go`

Add patterns to suppress known benign noise:
- `canceling statement due to user request` → suppress (postgres client disconnect)
- `connection to client lost` → suppress (postgres client disconnect)
- `context canceled` → suppress (normal request cancellation)

Add `Suppressed` field to `AnalyzeResult` to track suppressed noise count.
