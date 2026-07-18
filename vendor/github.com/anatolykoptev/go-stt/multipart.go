package stt

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
)

// buildMultipart creates a multipart body with audio content and all configured fields.
// formatOverride replaces c.format when non-empty. The body is written to w so
// that callers (and tests) can inject a custom io.Writer.
func (c *Client) buildMultipart(w io.Writer, audioReader io.Reader, filename, formatOverride string) (string, error) {
	mw := multipart.NewWriter(w)

	part, err := mw.CreateFormFile("file", filename)
	if err != nil {
		return "", fmt.Errorf("create form file: %w", err)
	}
	if _, err := io.Copy(part, audioReader); err != nil {
		return "", fmt.Errorf("copy audio: %w", err)
	}

	if err := writeField(mw, "model", c.model); err != nil {
		return "", err
	}
	if err := writeField(mw, "language", c.language); err != nil {
		return "", err
	}

	format := c.format
	if formatOverride != "" {
		format = formatOverride
	}
	if err := writeField(mw, "response_format", format); err != nil {
		return "", err
	}

	if err := c.writeOptionalFields(mw); err != nil {
		return "", err
	}

	if err := mw.Close(); err != nil {
		return "", fmt.Errorf("close writer: %w", err)
	}
	return mw.FormDataContentType(), nil
}

// writeOptionalFields writes optional client fields to the multipart writer.
func (c *Client) writeOptionalFields(w *multipart.Writer) error {
	if c.punctuate != nil {
		if err := writeField(w, "punctuate", strconv.FormatBool(*c.punctuate)); err != nil {
			return err
		}
	}
	if c.smartFormat != nil {
		if err := writeField(w, "smart_format", strconv.FormatBool(*c.smartFormat)); err != nil {
			return err
		}
	}
	if c.diarize {
		if err := writeField(w, "diarize", "true"); err != nil {
			return err
		}
	}
	if c.diarizeSpeakers > 0 {
		if err := writeField(w, "diarize_speakers", strconv.Itoa(c.diarizeSpeakers)); err != nil {
			return err
		}
	}
	if len(c.keywords) > 0 {
		b, err := json.Marshal(c.keywords)
		if err != nil {
			return fmt.Errorf("marshal keywords: %w", err)
		}
		if err := writeField(w, "keywords", string(b)); err != nil {
			return err
		}
	}
	if len(c.customSpelling) > 0 {
		b, err := json.Marshal(c.customSpelling)
		if err != nil {
			return fmt.Errorf("marshal custom_spelling: %w", err)
		}
		if err := writeField(w, "custom_spelling", string(b)); err != nil {
			return err
		}
	}
	return nil
}

// writeField writes a single form field to the multipart writer, wrapping any
// error so that a failing WriteField is never silently ignored (which would
// produce a malformed request body).
func writeField(w *multipart.Writer, name, value string) error {
	if err := w.WriteField(name, value); err != nil {
		return fmt.Errorf("write field %q: %w", name, err)
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
	c.setAuth(req)

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
