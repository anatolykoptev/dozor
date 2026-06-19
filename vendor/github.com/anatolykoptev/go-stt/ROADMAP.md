# go-stt Roadmap

OpenAI-compatible STT client for Go. Covers both HTTP batch and WebSocket streaming.

## Phase 1 — Extended HTTP API ✓ (v0.2)

Expose full ox-whisper `/v1/audio/transcriptions` capabilities.

- [x] **Verbose response** — word-level timestamps, confidence, segments (`TranscribeVerbose`)
- [x] **Output formats** — `text`, `srt`, `vtt` via `TranscribeRaw()` returning `[]byte`
- [x] **Advanced options** — `WithPunctuate`, `WithSmartFormat`, `WithDiarize`, `WithKeywords`, `WithCustomSpelling`
- [x] **`TranscribeReader`** — accept `io.Reader` instead of file path
- [x] **Model listing** — `Models()` via `GET /v1/models`
- [x] **Typed errors** — `Error{StatusCode, Message}` with `IsTransient()` and `errors.As` support

## Phase 2 — WebSocket Streaming ✓ (v0.2)

Real-time streaming client matching ox-whisper `/v1/listen` protocol.

- [x] **`StreamClient`** — persistent WebSocket connection with gorilla/websocket
- [x] **Event handler** — `StreamHandler` interface with `OnEvent`/`OnError`
- [x] **Control messages** — `Finalize()`, `Close()`, `ForceClose()`
- [x] **PCM encoding** — s16le and f32le binary frame sending
- [x] **Connection params** — language, vad, interim_results, smart_format, punctuate, encoding, sample_rate
- [x] **Channel-based API** — `StreamWithChannels()` returning `Events()` / `Errors()` channels

## Phase 3 — Reliability ✓ (v0.2)

Production hardening.

- [x] **Retry with backoff** — `WithRetry(maxAttempts, baseDelay)` for transient HTTP errors (429, 502-504)
- [x] **Circuit breaker** — `WithCircuitBreaker(maxFails, cooldown)` avoids hammering dead service
- [x] **Typed errors** — `Error{StatusCode, Message}` with `IsTransient()` for retry decisions

## Phase 4 — Streaming Helpers ✓ (v0.2)

Higher-level APIs for common streaming patterns.

- [x] **`StreamFile`** — stream a file over WebSocket in configurable chunks
- [x] **Channel-based API** — `StreamWithChannels` + `Events()` / `Errors()` channels
- [x] **`TranscribeURL`** — download audio from URL → transcribe → cleanup temp file

## Future (v0.3+)

- [ ] **Auto-reconnect** — WebSocket reconnect with exponential backoff
- [ ] **Concurrent limiter** — `WithMaxConcurrency(n)` semaphore for batch workloads
- [ ] **Interim coalescing** — buffer interim results, emit only when stable
- [ ] **Utterance assembly** — collect `speech_final` segments into complete utterances
- [ ] **Audio format detection** — detect ogg/wav/mp3/flac, pass appropriate encoding
- [ ] **Large file chunking** — split files >25MB by silence for APIs with size limits
- [ ] **SRT/VTT parser** — parse subtitle output back into typed structs

## Non-Goals

- Audio recording/capture (OS-specific, out of scope)
- Model management (server-side concern)
- TTS (separate domain)

## Competitive Landscape

| Feature | go-stt | Deepgram SDK | AssemblyAI SDK | OpenAI SDK |
|---------|--------|-------------|---------------|------------|
| HTTP batch | ✓ | ✓ | ✓ | ✓ |
| WebSocket streaming | ✓ | ✓ (rich) | ✗ | ✗ |
| Word timestamps | ✓ | ✓ | ✓ | ✓ |
| SRT/VTT output | ✓ | ✗ | ✓ | ✓ |
| Retry/backoff | ✓ | partial | ✓ | ✗ |
| Circuit breaker | ✓ | ✗ | ✗ | ✗ |
| Typed errors | ✓ | ✗ | ✓ | ✓ |
| Diarization | ✓ | ✓ | ✓ | ✓ |
| Channel API | ✓ | ✓ | ✗ | ✗ |
| TranscribeURL | ✓ | ✗ | ✗ | ✗ |
| StreamFile | ✓ | ✗ | ✗ | ✗ |
| Auto-reconnect | v0.3 | ✓ | n/a | n/a |

**Differentiator**: go-stt targets self-hosted ox-whisper (CPU-only, zero data retention) while being compatible with any OpenAI-like STT API. Deepgram-level streaming without cloud lock-in. Circuit breaker — unique among STT SDKs.
