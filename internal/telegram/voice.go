package telegram

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"

	stt "github.com/anatolykoptev/go-stt"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// transcribeVoice downloads a Telegram voice message and transcribes it via go-stt.
// Returns the transcribed text or an error.
func (c *Channel) transcribeVoice(ctx context.Context, voice *tgbotapi.Voice) (string, error) {
	// Get the file URL from Telegram.
	fileConfig := tgbotapi.FileConfig{FileID: voice.FileID}
	tgFile, err := c.bot.GetFile(fileConfig)
	if err != nil {
		return "", fmt.Errorf("get telegram file: %w", err)
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
		return "", fmt.Errorf("create download request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("download voice file: %w", err)
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
	client := stt.New(c.sttURL, stt.WithLanguage(c.sttLang))
	result, err := client.Transcribe(ctx, tmpFile.Name())
	if err != nil {
		return "", fmt.Errorf("transcribe: %w", err)
	}

	slog.Info("voice transcribed",
		slog.String("text", result.Text),
		slog.Int("duration_bytes", voice.FileSize))

	return result.Text, nil
}
