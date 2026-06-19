package stt

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
)

const (
	defaultStreamLanguage   = "en"
	defaultStreamSampleRate = 16000
	defaultStreamEncoding   = "pcm_s16le"
)

// StreamClient manages a WebSocket connection to /v1/listen.
type StreamClient struct {
	wsURL   string
	handler StreamHandler
	conn    *websocket.Conn
	done    chan struct{}
	mu      sync.Mutex
}

// NewStreamClient creates a StreamClient for the given base URL and params.
func NewStreamClient(baseURL string, params StreamParams, handler StreamHandler) *StreamClient {
	return &StreamClient{
		wsURL:   buildStreamURL(baseURL, params),
		handler: handler,
		done:    make(chan struct{}),
	}
}

// buildStreamURL converts an HTTP base URL to a WebSocket URL with query params.
func buildStreamURL(baseURL string, p StreamParams) string {
	wsBase := strings.NewReplacer("https://", "wss://", "http://", "ws://").Replace(baseURL)

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

	q := url.Values{}
	q.Set("language", lang)
	q.Set("vad", fmt.Sprintf("%t", p.VAD))
	q.Set("interim_results", fmt.Sprintf("%t", p.InterimResults))
	q.Set("smart_format", fmt.Sprintf("%t", p.SmartFormat))
	q.Set("punctuate", fmt.Sprintf("%t", p.Punctuate))
	q.Set("encoding", enc)
	q.Set("sample_rate", fmt.Sprintf("%d", sr))

	return wsBase + "/v1/listen?" + q.Encode()
}

// Connect dials the WebSocket server and starts the read goroutine.
func (sc *StreamClient) Connect(ctx context.Context) error {
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, sc.wsURL, nil)
	if err != nil {
		return fmt.Errorf("ws dial: %w", err)
	}
	sc.mu.Lock()
	sc.conn = conn
	sc.mu.Unlock()

	go sc.readLoop()
	return nil
}

// readLoop reads messages from the server until the connection closes.
func (sc *StreamClient) readLoop() {
	defer close(sc.done)
	for {
		msgType, data, err := sc.conn.ReadMessage()
		if err != nil {
			if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
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

// Close sends a CloseStream control message and waits for the connection to end.
func (sc *StreamClient) Close() error {
	if err := sc.sendControl("CloseStream"); err != nil {
		return err
	}
	<-sc.done
	return nil
}

// Done returns a channel that is closed when the connection ends.
func (sc *StreamClient) Done() <-chan struct{} {
	return sc.done
}

// ForceClose closes the underlying WebSocket connection immediately without
// sending a CloseStream control message. Use when graceful close is not possible.
func (sc *StreamClient) ForceClose() {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if sc.conn != nil {
		sc.conn.Close() //nolint:errcheck // best-effort force close
	}
}

// sendControl sends a JSON text frame with {"type": typeName}.
func (sc *StreamClient) sendControl(typeName string) error {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if sc.conn == nil {
		return fmt.Errorf("stream not connected")
	}
	msg, _ := json.Marshal(map[string]string{"type": typeName})
	if err := sc.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
		return fmt.Errorf("ws control %s: %w", typeName, err)
	}
	return nil
}
