package stt

import (
	"context"
	"fmt"
	"sync/atomic"
)

const channelBufferSize = 64

// ChannelStream wraps StreamClient with Go channels instead of callbacks.
type ChannelStream struct {
	sc     *StreamClient
	events chan StreamEvent
	errs   chan error
	// droppedEvents counts events silently dropped when the events channel
	// buffer was full (slow consumer). Exposed via DroppedEvents().
	droppedEvents atomic.Int64
}

// channelHandler implements StreamHandler and pushes to channels.
type channelHandler struct {
	events chan StreamEvent
	errs   chan error
	// droppedEvents is shared with the parent ChannelStream so the count
	// is visible to the caller.
	droppedEvents *atomic.Int64
}

func (h *channelHandler) OnEvent(e StreamEvent) {
	select {
	case h.events <- e:
	default:
		h.droppedEvents.Add(1)
	}
}

func (h *channelHandler) OnError(err error) {
	select {
	case h.errs <- err:
	default:
		// Errors are also dropped on overflow, but we don't expose a
		// separate counter — events are the primary data stream.
	}
}

// StreamWithChannels creates a streaming session returning Go channels.
func StreamWithChannels(ctx context.Context, baseURL string, params StreamParams) (*ChannelStream, error) {
	return StreamWithChannelsAndAPIKey(ctx, baseURL, params, "")
}

// StreamWithChannelsAndAPIKey creates a streaming session with an API key
// for the Authorization: Bearer header on the WebSocket upgrade request.
// Pass an empty string for self-hosted endpoints that don't require auth.
func StreamWithChannelsAndAPIKey(ctx context.Context, baseURL string, params StreamParams, apiKey string) (*ChannelStream, error) {
	events := make(chan StreamEvent, channelBufferSize)
	errs := make(chan error, channelBufferSize)

	cs := &ChannelStream{events: events, errs: errs}
	h := &channelHandler{events: events, errs: errs, droppedEvents: &cs.droppedEvents}
	sc := NewStreamClient(baseURL, params, h)
	sc.SetAPIKey(apiKey)

	if err := sc.Connect(ctx); err != nil {
		return nil, fmt.Errorf("stream connect: %w", err)
	}
	cs.sc = sc

	// Close channels when the underlying connection ends.
	go func() {
		<-sc.Done()
		close(events)
		close(errs)
	}()

	return cs, nil
}

// Events returns the read-only channel of streaming events.
func (cs *ChannelStream) Events() <-chan StreamEvent {
	return cs.events
}

// Errors returns the read-only channel of streaming errors.
func (cs *ChannelStream) Errors() <-chan error {
	return cs.errs
}

// DroppedEvents returns the number of events that were silently dropped
// because the events channel buffer was full (slow consumer). The count
// is cumulative for the lifetime of this ChannelStream.
func (cs *ChannelStream) DroppedEvents() int64 {
	return cs.droppedEvents.Load()
}

// Send transmits a binary PCM audio frame to the server.
func (cs *ChannelStream) Send(data []byte) error {
	return cs.sc.Send(data)
}

// Finalize sends a Finalize control message to flush pending audio.
func (cs *ChannelStream) Finalize() error {
	return cs.sc.Finalize()
}

// Close sends CloseStream and waits for the connection to end.
// The Events and Errors channels are closed automatically when the connection ends.
func (cs *ChannelStream) Close() error {
	return cs.sc.Close()
}

// ForceClose closes the WebSocket connection immediately without sending CloseStream.
// Use when the context is cancelled and graceful shutdown is not possible.
func (cs *ChannelStream) ForceClose() {
	cs.sc.ForceClose()
}
