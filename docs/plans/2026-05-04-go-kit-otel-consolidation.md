# go-kit consolidation + OTel tracing

**Date:** 2026-05-04
**Branch:** `feat/go-kit-otel-consolidation`
**Risk:** medium — large dependency bump + multi-pkg refactor on a live monitoring agent. Mitigated by manual install (no auto-deploy for dozor).

## Goals

1. Bump `go-kit` from `v0.19.0` → `v0.40.0`.
2. Replace dozor's hand-rolled patterns with go-kit equivalents where they exist.
3. Add OpenTelemetry tracing via `go-kit/tracing` (Jaeger collector already runs at `127.0.0.1:4318`, UI at `:16686`).

## Non-goals

- Replace `internal/bus/` (different semantics from `go-kit/eventbus` — bus is multi-channel transport, eventbus is pub/sub topics).
- Replace `internal/deploy/{queue,debounce,webhook}.go` (deploy-pipeline-specific, no go-kit equivalent).
- Replace `internal/approvals/` (Telegram approval flow, specific).
- Replace `prometheus/client_golang` metrics (HTTP scrape model is correct here).

## Wave 1 — bump + low-risk consolidations

After each step: `GOWORK=off go build ./... && GOWORK=off go test ./...`. Single commit per step.

1. **Bump go-kit** — `go get github.com/anatolykoptev/go-kit@v0.40.0 && go mod tidy`. Verify `tgfmt` API still compatible (3 import sites in `internal/telegram/`, `internal/session/`).
2. **`internal/engine/config_env.go` → `go-kit/env`** — replace `env/envInt/envFloat/envBool/envList/envDuration/envDurationStr` (~50 LOC) with thin shims that call go-kit/env. Delete the impl, keep the dozor-flavored function names as aliases to avoid touching ~40 call sites.
3. **`internal/mcpclient/kb.go` retry → `go-kit/retry`** — replace inline `for attempt := 1; …; backoff *= 2` block (~40 LOC) with `retry.Do(ctx, retry.Exponential(...), fn)`.
4. **Panic recover** — wrap `bus.ConsumeInbound` goroutine, `gateway_webhook.go` HTTP handlers, `internal/tools/handlers.go` MCP tool dispatch with `toolutil/recover` so panics become errors instead of crashing the process.
5. **OTel boilerplate** — `tracing.Setup(ctx, "dozor", WithSampleRatio(1.0))` in `cmd/dozor/main.go`; wrap HTTP servers (`gateway.go`, `gateway_webhook.go`, `a2a/server.go`, MCP HTTP) with `httpmw.Handler`; install `slogh.NewHandler` so logs carry `trace_id`/`span_id`. No spans on hot paths yet.
6. `.env` — add `OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318`.

End of Wave 1: install + restart, verify Jaeger UI receives `dozor` service traces. Commit-by-commit pushable to GitHub.

## Wave 2 — bigger consolidations

7. **`internal/engine/circuitbreaker.go` → `go-kit/breaker`** — drop `failsafe-go` dep. Same Closed/Open/HalfOpen API. Update `engine/config_kb.go` `initCBConfig` if needed.
8. **`internal/provider/{openai,fallback,errors,types}.go` → `go-kit/llm`** — biggest change. dozor today: 419 LOC. After: ~80 LOC of glue.
   - `openai.go` → `llm.NewHTTPClient(...)` (OpenAI-compatible POST)
   - `fallback.go NewFromEnv` → `llm.WithFallback(primary, fallback)`
   - `errors.go ProviderError.IsRateLimit` → use `llm.IsRateLimit(err)` if available, else keep tiny shim
   - update `internal/agent/loop.go` callers to use the `llm.Provider` interface
   - tests in `openai_test.go` (592 LOC) move to integration tests against go-kit/llm
9. **Rate-limit on webhook** — `gateway_webhook.go` wraps `/webhook/monitor/healthcheck` with `ratelimit.NewKeyedLimiter` keyed on sender host (10 RPS, burst 30). Returns 429 if exceeded, no LLM-bearing message generated.
10. **OTel spans on hot paths**:
    - `internal/agent/Loop.Process` — span per invocation (label: `iteration`, `tool_calls`, `model`)
    - `internal/toolreg.Dispatch` — span per tool call (label: `tool.name`, `tool.duration_ms`)
    - `internal/provider.Chat` — span per LLM call (label: `model`, `prompt_tokens`, `completion_tokens`)
    - `pkg/extensions/a2aclient.Call` — span per outbound A2A
    - `internal/telegram.Send` — span per Telegram send (label: `chat_id`, `length`)
    - `gateway_webhook.ServeHTTP` — span attributes: `webhook.path`, `webhook.sender`, `body.bytes`
11. **`hedge` for LLM** (optional) — `hedge.Do(ctx, hedge.Delay(3s), primary, fallback)` so primary slow = fallback starts in parallel.

## Bonus

12. **`internal/quotas/probe/{anthropic,gemini,webshare}.go`** — these are minimal HTTP clients to LLM API endpoints. If `go-kit/llm` exposes provider-agnostic quota probing, route them through it. Otherwise leave (not worth it).
13. **`internal/engine/httpclient.go newHTTPClient`** — if go-kit has a shared HTTP client factory with sane defaults, use it. Otherwise upstream a contribution to go-kit later.

## Rollback

- All commits are individually revertable (`git revert <sha>`).
- Binary install is manual (`make install`); failed start → `cp ~/.local/bin/dozor.bak ~/.local/bin/dozor && systemctl --user restart dozor`.
- Save `~/.local/bin/dozor` as `dozor.bak` before each install.

## Validation per wave

- `GOWORK=off go vet ./... && go build ./... && go test ./...`
- Restart dozor on krolik, watch `journalctl --user -u dozor -f` for 30s.
- Send test webhook to `/webhook/monitor/healthcheck`, confirm processing.
- Open Jaeger UI `:16686`, filter by service `dozor`, confirm trace appears (Wave 1) and is detailed (Wave 2).
