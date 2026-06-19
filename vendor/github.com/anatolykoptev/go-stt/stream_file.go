package stt

import (
	"context"
	"fmt"
	"os"
	"time"
)

const (
	defaultStreamFileSampleRate = 16000
	defaultStreamFileLanguage   = "ru"
	defaultChunkDuration        = 100 * time.Millisecond
	bytesPerSampleS16LE         = 2
)

// StreamFileOption configures StreamFile behaviour.
type StreamFileOption func(*streamFileConfig)

type streamFileConfig struct {
	language      string
	chunkDuration time.Duration
	interimResult bool
	sampleRate    int
}

func defaultStreamFileConfig() streamFileConfig {
	return streamFileConfig{
		language:      defaultStreamFileLanguage,
		chunkDuration: defaultChunkDuration,
		sampleRate:    defaultStreamFileSampleRate,
	}
}

// WithStreamLanguage sets the recognition language (default: "ru").
func WithStreamLanguage(lang string) StreamFileOption {
	return func(c *streamFileConfig) { c.language = lang }
}

// WithChunkDuration sets how many milliseconds of audio per chunk (default: 100ms).
func WithChunkDuration(d time.Duration) StreamFileOption {
	return func(c *streamFileConfig) { c.chunkDuration = d }
}

// WithInterim enables or disables interim results.
func WithInterim(v bool) StreamFileOption {
	return func(c *streamFileConfig) { c.interimResult = v }
}

// WithStreamSampleRate sets the PCM sample rate in Hz (default: 16000).
func WithStreamSampleRate(rate int) StreamFileOption {
	return func(c *streamFileConfig) { c.sampleRate = rate }
}

// StreamFile streams a WAV/PCM file over WebSocket in chunks and returns all final events.
// It simulates real-time playback by sleeping between chunks.
func StreamFile(ctx context.Context, baseURL string, audioPath string, opts ...StreamFileOption) ([]StreamEvent, error) {
	cfg := defaultStreamFileConfig()
	for _, o := range opts {
		o(&cfg)
	}

	data, err := os.ReadFile(audioPath)
	if err != nil {
		return nil, fmt.Errorf("read audio file: %w", err)
	}

	chunkSize := chunkSizeBytes(cfg.sampleRate, cfg.chunkDuration)

	params := StreamParams{
		Language:       cfg.language,
		InterimResults: cfg.interimResult,
		SampleRate:     cfg.sampleRate,
	}

	cs, err := StreamWithChannels(ctx, baseURL, params)
	if err != nil {
		return nil, fmt.Errorf("stream connect: %w", err)
	}

	if err := sendChunks(ctx, cs, data, chunkSize, cfg.chunkDuration); err != nil {
		cs.ForceClose()
		return nil, err
	}

	if err := cs.Finalize(); err != nil {
		cs.ForceClose()
		return nil, fmt.Errorf("finalize: %w", err)
	}

	events := collectFinalEvents(ctx, cs)
	if ctx.Err() != nil {
		cs.ForceClose()
		return events, ctx.Err()
	}

	_ = cs.Close()
	return events, nil
}

// chunkSizeBytes computes the byte size of one audio chunk given sample rate and duration.
func chunkSizeBytes(sampleRate int, d time.Duration) int {
	seconds := d.Seconds()
	return int(float64(sampleRate) * bytesPerSampleS16LE * seconds)
}

// sendChunks sends audio data in chunks, sleeping between each to simulate real-time.
func sendChunks(ctx context.Context, cs *ChannelStream, data []byte, chunkSize int, sleep time.Duration) error {
	if chunkSize <= 0 {
		chunkSize = 1
	}
	for i := 0; i < len(data); i += chunkSize {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		end := i + chunkSize
		if end > len(data) {
			end = len(data)
		}

		if err := cs.Send(data[i:end]); err != nil {
			return fmt.Errorf("send chunk: %w", err)
		}

		if end < len(data) {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(sleep):
			}
		}
	}
	return nil
}

// collectFinalEvents reads from the Events channel until a from_finalize=true event arrives
// or the context is cancelled or the channel is closed.
func collectFinalEvents(ctx context.Context, cs *ChannelStream) []StreamEvent {
	var collected []StreamEvent
	for {
		select {
		case <-ctx.Done():
			return collected
		case e, ok := <-cs.Events():
			if !ok {
				return collected
			}
			collected = append(collected, e)
			if e.FromFinalize {
				return collected
			}
		}
	}
}
