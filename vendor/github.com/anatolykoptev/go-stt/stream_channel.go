package stt

import (
	"context"
	"fmt"
)

const channelBufferSize = 64

// ChannelStream wraps StreamClient with Go channels instead of callbacks.
type ChannelStream struct {
	sc     *StreamClient
	events chan StreamEvent
	errs   chan error
}

// channelHandler implements StreamHandler and pushes to channels.
type channelHandler struct {
	events chan StreamEvent
	errs   chan error
}

func (h *channelHandler) OnEvent(e StreamEvent) {
	select {
	case h.events <- e:
	default:
	}
}

func (h *channelHandler) OnError(err error) {
	select {
	case h.errs <- err:
	default:
	}
}

// StreamWithChannels creates a streaming session returning Go channels.
func StreamWithChannels(ctx context.Context, baseURL string, params StreamParams) (*ChannelStream, error) {
	events := make(chan StreamEvent, channelBufferSize)
	errs := make(chan error, channelBufferSize)

	h := &channelHandler{events: events, errs: errs}
	sc := NewStreamClient(baseURL, params, h)

	if err := sc.Connect(ctx); err != nil {
		return nil, fmt.Errorf("stream connect: %w", err)
	}

	cs := &ChannelStream{sc: sc, events: events, errs: errs}

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
