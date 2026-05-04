# W2.8 — internal/provider → go-kit/llm consolidation

**Date:** 2026-05-04
**Branch:** `feat/provider-go-kit-llm`
**Risk:** medium-high — 419 LOC code + 592 LOC tests. Mitigation: layered
migration (mechanics first, policy second), each layer commit-bisectable.

## Goal

Replace dozor's hand-rolled OpenAI client mechanics with `go-kit/llm.Client`
while preserving the dozor-specific retry/classification policy and the
existing public API of `internal/provider` (so callers in
`internal/agent/loop.go` and `cmd/dozor/setup.go` are unchanged).

## Non-goals

- Replacing dozor's `Provider` interface with `kitllm.Client` directly.
  Callers compile against `provider.Provider`; flipping the interface
  breaks every call site for no LOC win.
- Changing the `withFallback` chain shape (already tuned in W2.11 with
  hedge.DoFallback).
- Adopting kitllm's internal retry — we keep dozor's classification
  semantics (auth fail-fast, retry-after override), so kitllm retries
  are explicitly disabled (`WithMaxRetries(0)` or middleware bypass).

## Research summary

**go-kit/llm** offers everything we need at the mechanics layer:

| dozor today | go-kit/llm replacement |
|---|---|
| `OpenAI.doChatCtx` (HTTP build, JSON marshal, response decode) | `kitllm.Client.Chat(ctx, msgs, opts...)` |
| `chatCompletionResponse` / `chatChoice` / `chatMessage` / `apiToolCall` types | `kitllm.ChatResponse` / `kitllm.ToolCall` / `kitllm.FunctionCall` |
| `httpmw.WrapTransport` on the client | `kitllm.WithHTTPClient(httpmw.Client())` |

**dozor-specific policy** that must not regress (asserted by tests):

| Test | Behaviour | Where to preserve |
|---|---|---|
| `TestChat_AuthError` | 401 → fail immediately, no retry | Middleware: classify, abort if auth |
| `TestChat_RateLimitRetry` | 429 with retry delay → wait + retry | Middleware: parse retry-after, sleep, retry |
| `TestChat_RateLimitRetryAfterDelay` | Retry-after from Google `details[].metadata.retryDelay` | Middleware: parse Google format |
| `TestChat_ServerErrorRetry` | 5xx → retry with backoff (jitter ±25%) | Middleware: backoff loop |
| `TestChat_MaxRetriesExhausted` | Surface last error after 3 attempts | Middleware: bounded loop |
| `TestChat_NetworkError` | Transport error → retry (not classified as auth) | Middleware: treat non-APIError as transient |
| `TestChat_EmptyChoices` | `len(choices) == 0` → error | kitllm already returns error (`empty choices in response`) |
| `TestChat_BlockedResponse` | empty content + finish_reason="content_filter" → not retried, surfaced | Caller-side check (post-decode) |
| `TestShouldRetry_Classification` | enum-style classification with `IsAuth/IsRateLimit/IsServerError/IsTransient` | Keep ProviderError + classification helpers; use as middleware decision input |
| `TestParseProviderError_GoogleRetryAfter` | Parse Google details[].metadata.retryDelay | Keep parseProviderError; unchanged |

**Lesson from Deneb's `llmerr/reason.go`:** classification (kind of failure)
and retry policy (what to do) are separate concerns. Adopting that
separation in dozor is a follow-up — for now, keep the existing
`ProviderError.IsAuth/IsRateLimit/IsServerError/IsTransient` API but
move the *application* of those checks into a kitllm middleware.

## Architecture

```
┌──────────────────────────────────────────────────────────────────┐
│ internal/agent/loop.go                                           │
│  l.provider.Chat(messages, tools)                                │
└────────────────────────┬─────────────────────────────────────────┘
                         │ unchanged Provider interface
                         ▼
┌──────────────────────────────────────────────────────────────────┐
│ internal/provider/openai.go (NEW: thin wrapper)                  │
│  • Public: Provider interface, Message/Response/ToolCall types   │
│  • Internal: kitllm.Client                                       │
│  • Calls kitllm.Client.Chat with WithMiddleware(retryClassifier) │
└────────────────────────┬─────────────────────────────────────────┘
                         │ kitllm.Middleware injection point
                         ▼
┌──────────────────────────────────────────────────────────────────┐
│ internal/provider/retry_middleware.go (NEW)                      │
│  • Wraps next() with: shouldRetry, chatBackoff, retry-after parse│
│  • Maps kitllm.APIError → dozor.ProviderError for classification │
│  • Returns dozor.ProviderError on final failure                  │
└────────────────────────┬─────────────────────────────────────────┘
                         │ next() = kitllm.Client.Chat (no retry)
                         ▼
┌──────────────────────────────────────────────────────────────────┐
│ go-kit/llm (mechanics: HTTP, JSON, transport)                    │
│  • WithMaxRetries(0) — we own the retry loop                     │
│  • Single doRequest call, surfaces APIError on non-200           │
└──────────────────────────────────────────────────────────────────┘
```

## Phase plan (commit-by-commit)

Each phase has its own commit; `go build ./... && go test ./...` after
each. If a phase regresses tests, revert just that commit.

### Phase 1 — Type adapter layer (no behaviour change)
**File:** `internal/provider/types_kit.go` (new)
**LOC:** +60 / −0

Add bidirectional converters:
- `toKitMessages([]provider.Message) []kitllm.Message`
- `toKitTools([]provider.ToolDefinition) []kitllm.Tool`
- `fromKitResponse(*kitllm.ChatResponse) *provider.Response`
- `fromKitToolCalls([]kitllm.ToolCall) []provider.ToolCall`

This is mechanical mapping. No logic changes anywhere else. Build + test
must remain green.

### Phase 2 — Replace doChatCtx with kitllm.Client (mechanics)
**Files:** `internal/provider/openai.go`
**LOC:** −80 / +30

- Add `client *kitllm.Client` field to `OpenAI`.
- `NewOpenAI()` constructs `kitllm.NewClient(apiURL, apiKey, model,
  kitllm.WithHTTPClient(httpmw.Client()), kitllm.WithMaxRetries(0))`.
- `doChatCtx` becomes a single kitllm Chat call with type conversion
  via Phase 1 adapters.
- Remove: `chatCompletionResponse`, `chatChoice`, `chatMessage`,
  `apiToolCall` types (now kitllm types).

Tests `TestChat_Success`, `TestChat_WithToolCalls`, `TestChat_EmptyChoices`
must still pass — they exercise the doChatCtx path through Chat.

### Phase 3 — Move retry/classification into a middleware
**Files:** `internal/provider/retry_middleware.go` (new), `openai.go`
**LOC:** −60 / +90

- New `retryMiddleware(opts retryOpts) kitllm.Middleware` that wraps
  `next(ctx, req)`:
  1. Calls `next`. On success, return.
  2. On error: convert `*kitllm.APIError` → `*provider.ProviderError`
     via existing `parseProviderError` (preserves Google retryDelay
     parsing!).
  3. Apply existing `shouldRetry` to decide retry / abort.
  4. Sleep `chatBackoff(attempt)` (existing impl, unchanged).
  5. Loop until `chatMaxRetries` exhausted.
- `chatWithRetry` deleted; the middleware owns the loop.
- `Chat` calls `client.Chat(context.Background(), msgs, opts...)` with
  the middleware installed.
- Public `shouldRetry`, `chatBackoff`, and `ProviderError` API kept —
  tests `TestShouldRetry_*`, `TestProviderError_Methods`,
  `TestParseProviderError_*` continue to pass without modification.

### Phase 4 — Test migration (kitllm types in test bodies where needed)
**Files:** `internal/provider/openai_test.go`
**LOC:** ±20

- `chatCompletion` / `chatCompletionWithTools` test helpers update to
  emit JSON shape that kitllm decodes (same wire format — should be
  zero diff in JSON; only the Go-side type changes).
- `apiToolCall` references in tests rename to `kitllm.ToolCall` or
  use a local alias.
- `newTestOpenAI` returns an `*OpenAI` whose internal `client` points
  at the test server.

### Phase 5 — Withdraw dead code
**Files:** `internal/provider/openai.go`, `errors.go`
**LOC:** −50 / +0

- Delete unused: legacy parse paths now in kitllm; HTTP retry constants
  consumed by middleware only; `chatLogTruncLen` if no longer used.
- Run `goimports -w` on the package.

### Phase 6 — `withFallback` adapter shape (no behaviour change)
**Files:** `internal/provider/fallback.go`
**LOC:** ±10

- Verify `chatSequential` and the hedge.DoFallback path both still
  work — they call `Provider.Chat`, which is now kitllm-backed.
- No structural change; just confirm tests pass.

## Validation checkpoints

After each phase:
1. `GOWORK=off go vet ./...` clean
2. `GOWORK=off go test -count=1 -timeout 180s ./internal/provider/...`
   all 14 tests pass
3. Full repo: `GOWORK=off go test -count=1 -timeout 240s ./...` clean
4. After Phase 3 (largest): smoke install on krolik, send a Telegram
   message that hits the LLM, confirm response.

## Risks & mitigations

| Risk | Likelihood | Mitigation |
|---|---|---|
| kitllm JSON shape differs subtly (e.g. `tool_calls` vs `tool_call`) | Low | Round-trip a real OpenAI / Gemini response in Phase 1 before Phase 2 |
| kitllm retries despite WithMaxRetries(0) | Low | Verify reading `doWithRetry` source — `c.maxRetries=0` skips the loop |
| Middleware execution order wrong (kitllm calls our middleware AFTER its retry) | Medium | Read kitllm `executeInner` — confirm middleware wraps the whole call, retry-disabled |
| `parseProviderError` expects raw body bytes, kitllm gives string | Low | Trivial: `[]byte(apiErr.Body)` |
| `withFallback` builds two `*OpenAI` from one shared http.Client (`primaryOpenAI.client` reuse) | Low | Both still work — kitllm.Client owns its own httpClient internally |

## Out of scope for W2.8 (follow-up tickets)

- **Adopt Deneb-style Reason enum** (14 reasons, RecoveryAction). Bigger
  rewrite of classification — separate proposal. Today we keep dozor's
  4-method classifier.
- **Streaming** (`kitllm.Stream`). dozor doesn't stream today; not
  needed.
- **JSON Schema responses** (`WithJSONSchema`). Not used.
- **Multimodal** (`CompleteMultimodal`). Not used.
- **WithCircuitBreaker** at kitllm level — dozor wraps Provider in its
  own breaker chain; let it stay that way.

## Rollback

Each phase is one commit; revert is `git revert <sha>`. The branch is
not auto-deployed (dozor uses manual `make install`), so production
binary on krolik can be rolled back with the saved `dozor.bak.*` file.

Validation that *production* is not affected: after merge, do NOT run
`make install` until the next session — gives time for any last-mile
issues to surface in `go test`.
