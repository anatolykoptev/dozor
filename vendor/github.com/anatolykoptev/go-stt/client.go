// Package stt provides a speech-to-text client for OpenAI-compatible STT APIs
// (ox-whisper, OpenAI Whisper, etc.) via multipart upload.
package stt

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

const defaultTimeout = 60 * time.Second

// Client sends audio files to an OpenAI-compatible /v1/audio/transcriptions endpoint.
type Client struct {
	baseURL         string
	language        string
	model           string
	format          string
	http            *http.Client
	punctuate       *bool
	smartFormat     *bool
	diarize         bool
	diarizeSpeakers int
	keywords        []string
	customSpelling  map[string]string
	retry           *retryConfig
	cb              *circuitBreaker
	apiKey          string
	tempDir         string
}

// Option configures the Client.
type Option func(*Client)

// WithLanguage sets the transcription language (default: "ru").
func WithLanguage(lang string) Option {
	return func(c *Client) { c.language = lang }
}

// WithModel sets the model name (default: "moonshine-v2").
func WithModel(model string) Option {
	return func(c *Client) { c.model = model }
}

// WithFormat sets the response format: "json" or "verbose_json" (default: "json").
func WithFormat(format string) Option {
	return func(c *Client) { c.format = format }
}

// WithTimeout sets the HTTP request timeout (default: 60s).
func WithTimeout(timeout time.Duration) Option {
	return func(c *Client) { c.http.Timeout = timeout }
}

// WithPunctuate enables or disables automatic punctuation.
func WithPunctuate(v bool) Option {
	return func(c *Client) { c.punctuate = &v }
}

// WithSmartFormat enables or disables smart formatting (numbers, dates, etc.).
func WithSmartFormat(v bool) Option {
	return func(c *Client) { c.smartFormat = &v }
}

// WithDiarize enables speaker diarization.
func WithDiarize(v bool) Option {
	return func(c *Client) { c.diarize = v }
}

// WithDiarizeSpeakers sets the expected number of speakers for diarization.
func WithDiarizeSpeakers(n int) Option {
	return func(c *Client) { c.diarizeSpeakers = n }
}

// WithKeywords sets hint keywords to boost recognition accuracy.
func WithKeywords(kw []string) Option {
	return func(c *Client) { c.keywords = kw }
}

// WithCustomSpelling sets custom spelling corrections (original → corrected).
func WithCustomSpelling(m map[string]string) Option {
	return func(c *Client) { c.customSpelling = m }
}

// WithRetry enables automatic retry with exponential backoff for transient errors.
// maxAttempts is the total number of attempts (including the first) and must satisfy
// 1 <= maxAttempts <= 100 (100 is a generous library ceiling; canonical SDKs use 3).
// baseDelay is the initial wait before the second attempt and must be > 0.
//
// On invalid input, WithRetry panics. This follows the Go convention for
// programmer/configuration errors (cf. regexp.MustCompile, time.Tick) and avoids a
// breaking change to the Option/New signatures, which do not return errors. A panic
// surfaces the misconfiguration loudly at construction time rather than silently
// looping an unbounded number of times or silently clamping the value.
func WithRetry(maxAttempts int, baseDelay time.Duration) Option {
	return WithRetryWithMaxDelay(maxAttempts, baseDelay, 30*time.Second) //nolint:mnd
}

// WithRetryWithMaxDelay is like WithRetry but allows configuring maxDelay
// (the cap on exponential backoff). WithRetry uses a default of 30s.
// maxDelay must be > 0; it may be less than baseDelay (baseDelay is capped
// immediately on the first doubling).
func WithRetryWithMaxDelay(maxAttempts int, baseDelay, maxDelay time.Duration) Option {
	const maxAllowedAttempts = 100 //nolint:mnd
	if maxAttempts < 1 || maxAttempts > maxAllowedAttempts {
		panic(fmt.Sprintf("stt.WithRetry: maxAttempts must be in [1, %d], got %d", maxAllowedAttempts, maxAttempts))
	}
	if baseDelay <= 0 {
		panic(fmt.Sprintf("stt.WithRetry: baseDelay must be > 0, got %v", baseDelay))
	}
	if maxDelay <= 0 {
		panic(fmt.Sprintf("stt.WithRetry: maxDelay must be > 0, got %v", maxDelay))
	}
	return func(c *Client) {
		c.retry = &retryConfig{
			maxAttempts: maxAttempts,
			baseDelay:   baseDelay,
			maxDelay:    maxDelay,
		}
	}
}

// WithCircuitBreaker enables a circuit breaker that stops sending requests after
// maxFails consecutive transient failures until the cooldown period has elapsed.
func WithCircuitBreaker(maxFails int, cooldown time.Duration) Option {
	return func(c *Client) {
		c.cb = &circuitBreaker{
			maxFails: maxFails,
			cooldown: cooldown,
		}
	}
}

// WithAPIKey sets the API key sent as "Authorization: Bearer <key>" on all
// HTTP requests (transcription, models, health) and on the WebSocket upgrade
// request. Required for OpenAI API; self-hosted ox-whisper does not need it.
func WithAPIKey(key string) Option {
	return func(c *Client) { c.apiKey = key }
}

// WithTempDir sets the directory used for temporary files downloaded by
// TranscribeURL/TranscribeURLVerbose. Defaults to os.TempDir() when unset.
func WithTempDir(dir string) Option {
	return func(c *Client) { c.tempDir = dir }
}

// setAuth sets the Authorization header on an HTTP request if an API key is configured.
func (c *Client) setAuth(req *http.Request) {
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
}

// New creates an STT client for the given base URL (e.g. "http://127.0.0.1:8092").
func New(baseURL string, opts ...Option) *Client {
	c := &Client{
		baseURL:  baseURL,
		language: "ru",
		model:    "moonshine-v2",
		format:   "json",
		http:     &http.Client{Timeout: defaultTimeout},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// IsAvailable checks if the STT service is reachable via /health.
func (c *Client) IsAvailable() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second) //nolint:mnd
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/health", nil)
	if err != nil {
		return false
	}
	c.setAuth(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
