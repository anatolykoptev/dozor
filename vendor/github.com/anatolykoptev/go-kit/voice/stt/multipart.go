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
	// language is optional — skip if empty (lets the STT service auto-detect).
	if c.language != "" {
		if err := writeField(mw, "language", c.language); err != nil {
			return "", err
		}
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
	var fields [][2]string // name, value
	if c.punctuate != nil {
		fields = append(fields, [2]string{"punctuate", strconv.FormatBool(*c.punctuate)})
	}
	if c.smartFormat != nil {
		fields = append(fields, [2]string{"smart_format", strconv.FormatBool(*c.smartFormat)})
	}
	if c.diarize {
		fields = append(fields, [2]string{"diarize", "true"})
	}
	if c.diarizeSpeakers > 0 {
		fields = append(fields, [2]string{"diarize_speakers", strconv.Itoa(c.diarizeSpeakers)})
	}
	if len(c.keywords) > 0 {
		b, err := json.Marshal(c.keywords)
		if err != nil {
			return fmt.Errorf("marshal keywords: %w", err)
		}
		fields = append(fields, [2]string{"keywords", string(b)})
	}
	if len(c.customSpelling) > 0 {
		b, err := json.Marshal(c.customSpelling)
		if err != nil {
			return fmt.Errorf("marshal custom_spelling: %w", err)
		}
		fields = append(fields, [2]string{"custom_spelling", string(b)})
	}
	for _, f := range fields {
		if err := writeField(w, f[0], f[1]); err != nil {
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

// postTranscription sends a multipart request to /v1/audio/transcriptions and
// returns the response body, capped at limit bytes. A non-200 answer becomes an
// *Error that carries the status (and a strict reason token) but not the body.
func (c *Client) postTranscription(ctx context.Context, body io.Reader, contentType string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/audio/transcriptions", body)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", stripURLError(err))
	}
	req.Header.Set("Content-Type", contentType)
	c.setAuth(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", stripURLError(err))
	}
	defer resp.Body.Close()
	return c.readResponse(resp, limit)
}

// readResponse returns the body of a 200 response (capped at limit) or an
// *Error for any other status.
func (c *Client) readResponse(resp *http.Response, limit int64) ([]byte, error) {
	if resp.StatusCode != http.StatusOK {
		eb, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		return nil, newHTTPError(resp.StatusCode, eb, c.apiKey)
	}
	return readLimited(resp.Body, limit)
}
