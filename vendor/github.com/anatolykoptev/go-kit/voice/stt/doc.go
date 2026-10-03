// Package stt is a speech-to-text client for OpenAI-compatible
// /v1/audio/transcriptions servers (self-hosted Whisper-compatible servers,
// OpenAI, Groq, ...): multipart upload, json/verbose_json/text/srt/vtt
// responses, optional retry with a circuit breaker, URL helpers and a
// WebSocket streaming client.
//
// There is no default server: [New] takes the base URL from the caller, and
// API keys are passed with [WithAPIKey]; the package never reads environment
// variables.
//
// Privacy: transcripts and audio are never logged, and an [Error] never carries
// the server's response body (only the HTTP status and, at most, a strict
// error-code token). Response bodies are read through size caps.
package stt
