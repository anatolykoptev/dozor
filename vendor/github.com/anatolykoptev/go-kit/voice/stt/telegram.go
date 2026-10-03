package stt

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
)

// TranscribeURL downloads audio from a URL, transcribes it, and cleans up the temp file.
// Useful for Telegram voice messages where you have the file download URL.
//
// audioURL must be trusted: the client fetches whatever it points at (an SSRF
// surface if it is user-controlled). For untrusted URLs pass an http.Client
// with a guarded transport via [WithHTTPClient]. Redirects are never followed
// and the client's API key is never sent to audioURL. Errors never contain
// audioURL (a Telegram file URL embeds the bot token).
func (c *Client) TranscribeURL(ctx context.Context, audioURL string) (*Response, error) {
	tmp, err := c.downloadToTemp(ctx, audioURL)
	if err != nil {
		return nil, fmt.Errorf("download audio: %w", err)
	}
	defer os.Remove(tmp)
	return c.Transcribe(ctx, tmp)
}

// TranscribeURLVerbose is like [Client.TranscribeURL] (same trust, redirect and
// error rules) but returns verbose results.
func (c *Client) TranscribeURLVerbose(ctx context.Context, audioURL string) (*VerboseResponse, error) {
	tmp, err := c.downloadToTemp(ctx, audioURL)
	if err != nil {
		return nil, fmt.Errorf("download audio: %w", err)
	}
	defer os.Remove(tmp)
	return c.TranscribeVerbose(ctx, tmp)
}

// defaultMaxDownload caps a downloaded audio file unless [WithMaxDownload] says otherwise.
const defaultMaxDownload int64 = 256 << 20

var (
	errDownloadURL    = errors.New("download: invalid URL")
	errDownloadFailed = errors.New("download: request failed")
)

// downloadError reduces a transport error to its class. The *url.Error that
// net/http returns prints the full request URL, which for a Telegram file link
// contains the bot token, so neither it nor anything wrapping it is returned.
func downloadError(err error) error {
	var ne net.Error
	switch {
	case errors.Is(err, context.Canceled):
		return fmt.Errorf("download: canceled: %w", context.Canceled)
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return fmt.Errorf("download: timeout: %w", context.DeadlineExceeded)
	default:
		return errDownloadFailed
	}
}

// downloadToTemp downloads audio from rawURL to a temp file and returns its
// path, using a copy of the Client's http.Client (so its timeout and transport
// apply) with redirects refused. The Client's API key is NOT sent: rawURL is
// an arbitrary host. The temp file goes in the Client's tempDir (default
// os.TempDir()); the caller removes it.
func (c *Client) downloadToTemp(ctx context.Context, rawURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", errDownloadURL // the parse error would echo rawURL
	}
	hc := *c.http
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := hc.Do(req)
	if err != nil {
		return "", downloadError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download: HTTP %d", resp.StatusCode)
	}
	limit := c.maxDownload
	if limit <= 0 {
		limit = defaultMaxDownload
	}
	f, err := os.CreateTemp(c.tempDir, "stt-voice-*.ogg")
	if err != nil {
		return "", err
	}
	n, err := io.Copy(f, io.LimitReader(resp.Body, limit+1))
	if err == nil && n > limit {
		err = ErrResponseTooLarge
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}
