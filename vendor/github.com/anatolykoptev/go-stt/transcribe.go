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
		respBody, status, err := c.postTranscription(ctx, bytes.NewReader(snapshot), ct)
		if err != nil {
			return nil, err
		}
		if status != http.StatusOK {
			return nil, &Error{StatusCode: status, Message: string(respBody)}
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
		respBody, status, err := c.postTranscription(ctx, bytes.NewReader(snapshot), ct)
		if err != nil {
			return nil, err
		}
		if status != http.StatusOK {
			return nil, &Error{StatusCode: status, Message: string(respBody)}
		}
		return respBody, nil
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
	var body bytes.Buffer
	ct, err := c.buildMultipart(&body, r, filename, formatOverride)
	if err != nil {
		return nil, err
	}
	snapshot := body.Bytes()

	do := func() (*Response, error) {
		respBody, status, err := c.postTranscription(ctx, bytes.NewReader(snapshot), ct)
		if err != nil {
			return nil, err
		}
		if status != http.StatusOK {
			return nil, &Error{StatusCode: status, Message: string(respBody)}
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
			return nil, fmt.Errorf("create request: %w", err)
		}
		c.setAuth(req)

		resp, err := c.http.Do(req)
		if err != nil {
			return nil, fmt.Errorf("send request: %w", err)
		}
		defer resp.Body.Close()

		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("read response: %w", err)
		}
		if resp.StatusCode != http.StatusOK {
			return nil, &Error{StatusCode: resp.StatusCode, Message: string(respBody)}
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
