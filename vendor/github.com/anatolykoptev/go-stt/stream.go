package stt

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

const (
	defaultStreamLanguage   = "en"
	defaultStreamSampleRate = 16000
	defaultStreamEncoding   = "pcm_s16le"
	// defaultCloseTimeout bounds how long Close() waits for a graceful server
	// close frame before force-closing the connection. Keeps callers from
	// blocking forever on a hung server.
	defaultCloseTimeout = 5 * time.Second
	// defaultWSReadLimit caps the size of a single inbound WebSocket message.
	// Without it a malicious/buggy server could send a 1GB frame and force
	// ReadMessage to allocate the whole payload (memory bomb). 10MB is well
	// above any legitimate Deepgram event while bounding worst-case alloc.
	defaultWSReadLimit = 10 * 1024 * 1024
)

// StreamClient manages a WebSocket connection to /v1/listen.
type StreamClient struct {
	wsURL   string
	wsErr   error
	handler StreamHandler
	conn    *websocket.Conn
	done    chan struct{}
	mu      sync.Mutex
	apiKey  string

	// closed is set before the underlying conn is closed so readLoop can
	// distinguish a deliberate ForceClose from a server-initiated error.
	closed atomic.Bool
	// closeOnce guards the underlying conn.Close() so concurrent
	// ForceClose/Close calls are idempotent.
	closeOnce sync.Once
	// doneOnce guards close(sc.done) so a second Connect/readLoop path can't
	// double-close the channel and panic.
	doneOnce sync.Once
}

// NewStreamClient creates a StreamClient for the given base URL and params.
// If baseURL cannot be converted to a valid WebSocket URL (e.g. it has no
// recognizable scheme), the error is captured and surfaced by Connect.
func NewStreamClient(baseURL string, params StreamParams, handler StreamHandler) *StreamClient {
	wsURL, wsErr := buildStreamURL(baseURL, params)
	return &StreamClient{
		wsURL:   wsURL,
		wsErr:   wsErr,
		handler: handler,
		done:    make(chan struct{}),
	}
}

// SetAPIKey sets the API key for the Authorization: Bearer header on the
// WebSocket upgrade request. Must be called before Connect.
func (sc *StreamClient) SetAPIKey(key string) {
	sc.apiKey = key
}

// convertWebSocketScheme converts an HTTP/HTTPS base URL to a WebSocket URL
// using net/url. Unlike a naive strings.NewReplacer, it correctly handles
// schemeless loopback addresses (e.g. "127.0.0.1:8092"), ws/wss passthrough,
// trailing slashes, and preserved query strings. It requires an explicit
// http/https/ws/wss scheme and rejects anything else.
func convertWebSocketScheme(baseURL string) (string, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("invalid stream URL %q: %w", baseURL, err)
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	case "ws", "wss":
		// passthrough — already a WebSocket scheme
	default:
		return "", fmt.Errorf("invalid stream URL %q: missing or unsupported scheme %q (want http/https/ws/wss)", baseURL, u.Scheme)
	}
	// Strip a trailing slash so appending "/v1/listen" doesn't produce a
	// double slash ("host//v1/listen").
	u.Path = strings.TrimSuffix(u.Path, "/")
	return u.String(), nil
}

// buildStreamURL converts an HTTP base URL to a WebSocket URL with query params.
func buildStreamURL(baseURL string, p StreamParams) (string, error) {
	wsBase, err := convertWebSocketScheme(baseURL)
	if err != nil {
		return "", err
	}

	lang := p.Language
	if lang == "" {
		lang = defaultStreamLanguage
	}
	enc := p.Encoding
	if enc == "" {
		enc = defaultStreamEncoding
	}
	sr := p.SampleRate
	if sr == 0 {
		sr = defaultStreamSampleRate
	}

	// VAD and Punctuate default to true when nil (matching the server's
	// default behavior and the doc comments on StreamParams).
	vad := true
	if p.VAD != nil {
		vad = *p.VAD
	}
	punctuate := true
	if p.Punctuate != nil {
		punctuate = *p.Punctuate
	}

	q := url.Values{}
	q.Set("language", lang)
	q.Set("vad", fmt.Sprintf("%t", vad))
	q.Set("interim_results", fmt.Sprintf("%t", p.InterimResults))
	q.Set("smart_format", fmt.Sprintf("%t", p.SmartFormat))
	q.Set("punctuate", fmt.Sprintf("%t", punctuate))
	q.Set("encoding", enc)
	q.Set("sample_rate", fmt.Sprintf("%d", sr))

	return wsBase + "/v1/listen?" + q.Encode(), nil
}

// Connect dials the WebSocket server and starts the read goroutine. Calling
// Connect twice on the same StreamClient returns an error instead of starting
// a second readLoop (which would double-close sc.done and panic).
func (sc *StreamClient) Connect(ctx context.Context) error {
	// Surface any URL-conversion error captured at construction time so
	// callers learn about a bad baseURL on Connect rather than dialing a
	// misrouted address.
	if sc.wsErr != nil {
		return fmt.Errorf("ws url: %w", sc.wsErr)
	}
	sc.mu.Lock()
	if sc.conn != nil {
		sc.mu.Unlock()
		return fmt.Errorf("stream already connected")
	}
	sc.mu.Unlock()

	// Pass the Authorization header on the WebSocket upgrade request if an
	// API key is configured (required for OpenAI API; self-hosted ignores it).
	var header http.Header
	if sc.apiKey != "" {
		header = http.Header{}
		header.Set("Authorization", "Bearer "+sc.apiKey)
	}
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, sc.wsURL, header)
	if err != nil {
		return fmt.Errorf("ws dial: %w", err)
	}
	// Cap inbound message size so a malicious/buggy server can't force a
	// giant allocation (memory bomb). gorilla returns an error on the next
	// ReadMessage that exceeds the limit; readLoop routes it to OnError.
	conn.SetReadLimit(defaultWSReadLimit)
	sc.mu.Lock()
	// Re-check under lock in case a concurrent Connect raced past the first guard.
	if sc.conn != nil {
		sc.mu.Unlock()
		_ = conn.Close() //nolint:errcheck // discard the extra connection
		return fmt.Errorf("stream already connected")
	}
	sc.conn = conn
	sc.closed.Store(false)
	sc.mu.Unlock()

	go sc.readLoop()
	return nil
}

// readLoop reads messages from the server until the connection closes. The
// done channel is closed exactly once via doneOnce so a second Connect (or any
// other path) cannot double-close it and panic.
func (sc *StreamClient) readLoop() {
	defer sc.doneOnce.Do(func() { close(sc.done) })
	for {
		// Bail out early if ForceClose already tore the conn down.
		if sc.closed.Load() {
			return
		}
		sc.mu.Lock()
		conn := sc.conn
		sc.mu.Unlock()
		if conn == nil {
			return
		}
		msgType, data, err := conn.ReadMessage()
		if err != nil {
			if !sc.closed.Load() && !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				sc.handler.OnError(fmt.Errorf("ws read: %w", err))
			}
			return
		}
		if msgType != websocket.TextMessage {
			continue
		}
		var event StreamEvent
		if err := json.Unmarshal(data, &event); err != nil {
			sc.handler.OnError(fmt.Errorf("ws parse: %w", err))
			continue
		}
		sc.handler.OnEvent(event)
	}
}

// Send transmits a binary PCM audio frame to the server.
func (sc *StreamClient) Send(data []byte) error {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if sc.conn == nil {
		return fmt.Errorf("stream not connected")
	}
	if err := sc.conn.WriteMessage(websocket.BinaryMessage, data); err != nil {
		return fmt.Errorf("ws send: %w", err)
	}
	return nil
}

// Finalize sends a Finalize control message to flush pending audio.
func (sc *StreamClient) Finalize() error {
	return sc.sendControl("Finalize")
}

// Close sends a CloseStream control message and waits for the connection to
// end. If the server does not respond within defaultCloseTimeout, the
// connection is force-closed so the caller never blocks forever on a hung
// server.
func (sc *StreamClient) Close() error {
	if err := sc.sendControl("CloseStream"); err != nil {
		// Best-effort: still ensure the conn is torn down.
		sc.ForceClose()
		return err
	}
	select {
	case <-sc.done:
		return nil
	case <-time.After(defaultCloseTimeout):
		sc.ForceClose()
		return nil
	}
}

// Done returns a channel that is closed when the connection ends.
func (sc *StreamClient) Done() <-chan struct{} {
	return sc.done
}

// ForceClose closes the underlying WebSocket connection immediately without
// sending a CloseStream control message. Use when graceful close is not
// possible. It is safe to call concurrently and multiple times. Setting a zero
// read deadline unblocks any in-flight ReadMessage in readLoop so the goroutine
// exits promptly.
func (sc *StreamClient) ForceClose() {
	sc.closed.Store(true)
	sc.mu.Lock()
	conn := sc.conn
	sc.mu.Unlock()
	if conn == nil {
		return
	}
	// Unblock a blocked ReadMessage so readLoop exits immediately.
	_ = conn.SetReadDeadline(time.Now()) //nolint:errcheck // best-effort
	sc.closeOnce.Do(func() {
		_ = conn.Close() //nolint:errcheck // best-effort force close
	})
}

// sendControl sends a JSON text frame with {"type": typeName}.
func (sc *StreamClient) sendControl(typeName string) error {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if sc.conn == nil {
		return fmt.Errorf("stream not connected")
	}
	msg, err := json.Marshal(map[string]string{"type": typeName})
	if err != nil {
		return fmt.Errorf("ws control %s: marshal: %w", typeName, err)
	}
	if err := sc.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
		return fmt.Errorf("ws control %s: %w", typeName, err)
	}
	return nil
}
