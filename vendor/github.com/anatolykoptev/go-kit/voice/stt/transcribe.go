package stt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Transcribe sends the audio file to the STT service and returns the transcription.
func (c *Client) Transcribe(ctx context.Context, audioPath string) (*Response, error) {
	f, err := os.Open(audioPath)
	if err != nil {
		return nil, fmt.Errorf("open audio: %w", err)
	}
	defer f.Close()

	return c.transcribeFromReader(ctx, f, filepath.Base(audioPath), "")
}

// TranscribeVerbose sends the audio file and returns a VerboseResponse with segments and words.
func (c *Client) TranscribeVerbose(ctx context.Context, audioPath string) (*VerboseResponse, error) {
	f, err := os.Open(audioPath)
	if err != nil {
		return nil, fmt.Errorf("open audio: %w", err)
	}
	defer f.Close()

	var body bytes.Buffer
	ct, err := c.buildMultipart(&body, f, filepath.Base(audioPath), "verbose_json")
	if err != nil {
		return nil, err
	}
	snapshot := body.Bytes()

	do := func() (*VerboseResponse, error) {
		respBody, err := c.postTranscription(ctx, bytes.NewReader(snapshot), ct, maxVerboseResponse)
		if err != nil {
			return nil, err
		}
		var result VerboseResponse
		if err := json.Unmarshal(respBody, &result); err != nil {
			return nil, fmt.Errorf("unmarshal verbose response: %w", err)
		}
		if result.Language == "" {
			result.Language = c.language
		}
		return &result, nil
	}

	if c.retry != nil {
		return doWithRetry(ctx, c.retry, c.cb, do)
	}
	return do()
}

// TranscribeRaw sends the audio file and returns the raw response bytes (useful for text/srt/vtt).
func (c *Client) TranscribeRaw(ctx context.Context, audioPath string) ([]byte, error) {
	f, err := os.Open(audioPath)
	if err != nil {
		return nil, fmt.Errorf("open audio: %w", err)
	}
	defer f.Close()

	var body bytes.Buffer
	ct, err := c.buildMultipart(&body, f, filepath.Base(audioPath), "")
	if err != nil {
		return nil, err
	}
	snapshot := body.Bytes()

	do := func() ([]byte, error) {
		return c.postTranscription(ctx, bytes.NewReader(snapshot), ct, responseLimit(c.format))
	}

	if c.retry != nil {
		return doWithRetry(ctx, c.retry, c.cb, do)
	}
	return do()
}

// TranscribeReader accepts an io.Reader instead of a file path.
func (c *Client) TranscribeReader(ctx context.Context, r io.Reader, filename string) (*Response, error) {
	return c.transcribeFromReader(ctx, r, filename, "")
}

// transcribeFromReader is the shared implementation for Transcribe and TranscribeReader.
func (c *Client) transcribeFromReader(ctx context.Context, r io.Reader, filename, formatOverride string) (*Response, error) {
	format := c.format
	if formatOverride != "" {
		format = formatOverride
	}
	if format == "srt" || format == "vtt" {
		return nil, ErrUnsupportedFormat
	}
	var body bytes.Buffer
	ct, err := c.buildMultipart(&body, r, filename, formatOverride)
	if err != nil {
		return nil, err
	}
	snapshot := body.Bytes()

	do := func() (*Response, error) {
		respBody, err := c.postTranscription(ctx, bytes.NewReader(snapshot), ct, responseLimit(format))
		if err != nil {
			return nil, err
		}
		if format == "text" {
			// A plain-text body is the transcript itself, not JSON.
			return &Response{Text: strings.TrimSpace(string(respBody)), Language: c.language}, nil
		}
		var result Response
		if err := json.Unmarshal(respBody, &result); err != nil {
			return nil, fmt.Errorf("unmarshal response: %w", err)
		}
		if result.Language == "" {
			result.Language = c.language
		}
		return &result, nil
	}

	if c.retry != nil {
		return doWithRetry(ctx, c.retry, c.cb, do)
	}
	return do()
}

// Models fetches the list of available models from GET /v1/models.
func (c *Client) Models(ctx context.Context) (*ModelList, error) {
	do := func() (*ModelList, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/models", nil)
		if err != nil {
			return nil, fmt.Errorf("create request: %w", stripURLError(err))
		}
		c.setAuth(req)

		resp, err := c.http.Do(req)
		if err != nil {
			return nil, fmt.Errorf("send request: %w", stripURLError(err))
		}
		defer resp.Body.Close()

		respBody, err := c.readResponse(resp, maxModelsResponse)
		if err != nil {
			return nil, err
		}

		var result ModelList
		if err := json.Unmarshal(respBody, &result); err != nil {
			return nil, fmt.Errorf("unmarshal models: %w", err)
		}
		return &result, nil
	}

	if c.retry != nil {
		return doWithRetry(ctx, c.retry, c.cb, do)
	}
	return do()
}
