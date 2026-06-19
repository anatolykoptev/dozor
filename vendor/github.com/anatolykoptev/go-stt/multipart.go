package stt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
)

// buildMultipart creates a multipart body with audio content and all configured fields.
// formatOverride replaces c.format when non-empty.
func (c *Client) buildMultipart(audioReader io.Reader, filename, formatOverride string) (*bytes.Buffer, string, error) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)

	part, err := w.CreateFormFile("file", filename)
	if err != nil {
		return nil, "", fmt.Errorf("create form file: %w", err)
	}
	if _, err := io.Copy(part, audioReader); err != nil {
		return nil, "", fmt.Errorf("copy audio: %w", err)
	}

	_ = w.WriteField("model", c.model)
	_ = w.WriteField("language", c.language)

	format := c.format
	if formatOverride != "" {
		format = formatOverride
	}
	_ = w.WriteField("response_format", format)

	if err := c.writeOptionalFields(w); err != nil {
		return nil, "", err
	}

	if err := w.Close(); err != nil {
		return nil, "", fmt.Errorf("close writer: %w", err)
	}
	return &body, w.FormDataContentType(), nil
}

// writeOptionalFields writes optional client fields to the multipart writer.
func (c *Client) writeOptionalFields(w *multipart.Writer) error {
	if c.punctuate != nil {
		_ = w.WriteField("punctuate", strconv.FormatBool(*c.punctuate))
	}
	if c.smartFormat != nil {
		_ = w.WriteField("smart_format", strconv.FormatBool(*c.smartFormat))
	}
	if c.diarize {
		_ = w.WriteField("diarize", "true")
	}
	if c.diarizeSpeakers > 0 {
		_ = w.WriteField("diarize_speakers", strconv.Itoa(c.diarizeSpeakers))
	}
	if len(c.keywords) > 0 {
		b, err := json.Marshal(c.keywords)
		if err != nil {
			return fmt.Errorf("marshal keywords: %w", err)
		}
		_ = w.WriteField("keywords", string(b))
	}
	if len(c.customSpelling) > 0 {
		b, err := json.Marshal(c.customSpelling)
		if err != nil {
			return fmt.Errorf("marshal custom_spelling: %w", err)
		}
		_ = w.WriteField("custom_spelling", string(b))
	}
	return nil
}

// postTranscription sends a multipart request to /v1/audio/transcriptions and returns raw bytes.
func (c *Client) postTranscription(ctx context.Context, body io.Reader, contentType string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/audio/transcriptions", body)
	if err != nil {
		return nil, 0, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", contentType)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read response: %w", err)
	}
	return respBody, resp.StatusCode, nil
}
