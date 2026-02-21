# Dozor Refactoring Plan

Code review: 2026-02-20. Findings: 15 issues (3 high, 7 medium, 5 low).

## Phase 1: Critical — Tool Layer Deduplication

### Finding 1: bridge.go is a full copy of tools/
- **Files**: `internal/toolreg/bridge.go` (670 lines) vs `internal/tools/*.go`
- **Problem**: Both register identical tools. bridge.go already drifted (missing `connections`, `cron`, `filter`)
- **Fix**: Extract shared handler functions from `internal/tools/`, have bridge.go delegate to them
- **Impact**: ~1200 duplicate lines eliminated, no more drift risk

### Finding 2: systemd property parsing x4
- **Files**: `engine/systemd.go`, `engine/remote.go`, `tools/services.go`, `toolreg/bridge.go`
- **Problem**: `ActiveEnterTimestamp` + `MemoryCurrent` parsing copy-pasted 4 times
- **Fix**: Extract `FormatSystemctlProperties(output string, b *strings.Builder)` in engine/systemd.go

### Finding 3: HTTP client creation x3
- **Files**: `engine/remote.go`, `engine/probe.go`, `engine/websearch.go`
- **Problem**: `&http.Client{TLS, Redirect, Timeout}` created identically in 3 places
- **Fix**: `func newHTTPClient(timeout time.Duration) *http.Client` helper

## Phase 2: Medium — DRY + Cleanup

### Finding 4: BytesToMB / FormatBytesMB duplicate parsing
- **File**: `engine/sizeparse.go`
- **Fix**: `FormatBytesMB` should call `BytesToMB` internally

### Finding 5: cleanup_targets.go repetitive pattern
- **File**: `engine/cleanup_targets.go` (304 lines)
- **Problem**: scan/clean pattern repeated for each target (go, npm, uv, pip, journal, tmp, caches)
- **Fix**: Generic `cleanupSpec` struct + `scanGeneric`/`cleanGeneric` methods → ~100 lines

### Finding 6: Bot scanner paths in two lists
- **Files**: `engine/logs.go` (36 regexps), `engine/security.go` (string list)
- **Fix**: Single canonical list, used by both `filterBotScanner` and `checkReconnaissance`

### Finding 7: Custom min() function
- **File**: `engine/websearch.go`
- **Fix**: Delete — Go 1.21+ has builtin `min()`

### Finding 8: WebSearchConfig duplicated in Config
- **Files**: `engine/config.go`, `engine/websearch.go`
- **Fix**: Embed `WebSearchConfig` inside `Config` struct, pass directly

### Finding 9: executeLocal/executeSSH 90% identical
- **File**: `engine/transport.go`
- **Fix**: Extract `runCommand(ctx, name, args, command) CommandResult` helper

### Finding 10: Execute/ExecuteUnsafe identical
- **File**: `engine/transport.go`
- **Fix**: Remove `ExecuteUnsafe` or add actual validation to `Execute`

## Phase 3: Low — Polish

### Finding 11: resolveServices on 2 structs
- **Files**: `engine/agent.go`, `engine/security.go`
- **Fix**: SecurityCollector uses callback to agent's resolution

### Finding 12: Regex compiled in hot paths
- **File**: `engine/websearch.go`
- **Fix**: Move to package-level `var` declarations

### Finding 13: Inconsistent byte formatting
- **Files**: `engine/overview.go` (`formatBytes`), `engine/sizeparse.go`
- **Fix**: Consolidate naming, eliminate `formatBytes` from overview.go

### Finding 14: Stale comment
- **File**: `internal/tools/register.go` line 31
- **Fix**: Remove "// New tools"

### Finding 15: Error-level check repeated inline
- **Files**: `engine/log_analyzer.go`, `engine/logs.go`
- **Fix**: Add `IsErrorLevel(level string) bool` helper or `LogEntry.IsError()` method

## Metrics

| Phase | Findings | Est. lines saved | Risk |
|-------|----------|-----------------|------|
| 1 | 1-3 | ~1400 | Medium (bridge.go rewrite) |
| 2 | 4-10 | ~300 | Low |
| 3 | 11-15 | ~50 | Minimal |

## What Was Done Well (keep)

- Clean separation: engine / tools / toolreg / agent / skills / extensions
- Well-defined types with JSON tags and helpers
- Consistent input validation via validation.go
- Extension system with optional capabilities (Startable, Stoppable)
- Security: command blocklist, shell sanitization, container exec validation
- Tests for critical paths (format, probe, validation, security, dev_mode)
