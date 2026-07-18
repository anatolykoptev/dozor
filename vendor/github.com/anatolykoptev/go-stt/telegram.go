package stt

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
)

// TranscribeURL downloads audio from a URL, transcribes it, and cleans up the temp file.
// Useful for Telegram voice messages where you have the file download URL.
func (c *Client) TranscribeURL(ctx context.Context, audioURL string) (*Response, error) {
	tmp, err := c.downloadToTemp(ctx, audioURL)
	if err != nil {
		return nil, fmt.Errorf("download audio: %w", err)
	}
	defer os.Remove(tmp)
	return c.Transcribe(ctx, tmp)
}

// TranscribeURLVerbose is like TranscribeURL but returns verbose results.
func (c *Client) TranscribeURLVerbose(ctx context.Context, audioURL string) (*VerboseResponse, error) {
	tmp, err := c.downloadToTemp(ctx, audioURL)
	if err != nil {
		return nil, fmt.Errorf("download audio: %w", err)
	}
	defer os.Remove(tmp)
	return c.TranscribeVerbose(ctx, tmp)
}

// downloadToTemp downloads audio from url to a temp file and returns its path.
// It uses the Client's configured http.Client (with timeout) instead of
// http.DefaultClient, and the Client's tempDir if configured (fallback
// os.TempDir()). The caller is responsible for removing the temp file.
func (c *Client) downloadToTemp(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	c.setAuth(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download: HTTP %d", resp.StatusCode)
	}
	dir := c.tempDir
	f, err := os.CreateTemp(dir, "stt-voice-*.ogg")
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", err
	}
	f.Close()
	return f.Name(), nil
}
