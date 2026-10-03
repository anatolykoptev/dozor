package telegram

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"unicode/utf8"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	stt "github.com/anatolykoptev/go-kit/voice/stt"
)

// stripURLError returns the cause of a *url.Error. net/http (and tgbotapi) put
// the full request URL in that error's text, and a Telegram URL embeds the bot
// token, so no returned error may wrap the *url.Error itself.
func stripURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return ue.Err
	}
	return err
}

// transcribeVoice downloads a Telegram voice message and transcribes it via go-kit's voice/stt.
// Returns the transcribed text or an error. Errors never contain the file URL
// (it holds the bot token) and nothing logs the transcript.
func (c *Channel) transcribeVoice(ctx context.Context, voice *tgbotapi.Voice) (string, error) {
	// Get the file URL from Telegram.
	fileConfig := tgbotapi.FileConfig{FileID: voice.FileID}
	tgFile, err := c.bot.GetFile(fileConfig)
	if err != nil {
		return "", fmt.Errorf("get telegram file: %w", stripURLError(err))
	}
	fileURL := tgFile.Link(c.bot.Token)

	// Download to a temp file.
	tmpFile, err := os.CreateTemp("", "dozor-voice-*.ogg")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	defer os.Remove(tmpFile.Name())
	defer tmpFile.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL, nil)
	if err != nil {
		return "", errors.New("create download request: invalid URL") // the parse error would echo the URL
	}
	client := c.dl
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download voice file: %w", stripURLError(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download voice: status %d", resp.StatusCode)
	}
	if _, err := io.Copy(tmpFile, resp.Body); err != nil {
		return "", fmt.Errorf("save voice file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return "", fmt.Errorf("close temp file: %w", err)
	}

	// Transcribe.
	sttClient := stt.New(c.sttURL, stt.WithLanguage(c.sttLang))
	result, err := sttClient.Transcribe(ctx, tmpFile.Name())
	if err != nil {
		return "", fmt.Errorf("transcribe: %w", err)
	}
	slog.Info("voice transcribed",
		slog.Int("text_runes", utf8.RuneCountInString(result.Text)),
		slog.Int("file_bytes", voice.FileSize))
	return result.Text, nil
}
