package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anatolykoptev/dozor/internal/bus"
	"github.com/anatolykoptev/dozor/internal/engine"
	tgfmt "github.com/anatolykoptev/go-kit/telegram"
	"github.com/anatolykoptev/go-kit/telegram/tgsafe"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type Channel struct {
	bot     *tgbotapi.BotAPI
	bus     *bus.Bus
	allowed map[int64]bool // whitelisted user IDs
	ctx     context.Context
	sttURL  string   // STT service base URL
	sttLang string   // STT transcription language
	dl      httpDoer // voice-file download client; scrubs the bot token from errors (nil = a fresh scrubbing client)

	stopTyping sync.Map // chatID string → chan struct{}
}

func New(msgBus *bus.Bus) (*Channel, error) {
	token := os.Getenv("DOZOR_TELEGRAM_TOKEN")
	if token == "" {
		return nil, errors.New("DOZOR_TELEGRAM_TOKEN not set")
	}

	bot, dl, err := newBot(token, tgbotapi.APIEndpoint, &http.Client{Timeout: botHTTPTimeout})
	if err != nil {
		return nil, fmt.Errorf("create telegram bot: %w", err)
	}

	allowed := make(map[int64]bool)
	if ids := os.Getenv("DOZOR_TELEGRAM_ALLOWED"); ids != "" {
		for _, s := range strings.Split(ids, ",") {
			s = strings.TrimSpace(s)
			if id, err := strconv.ParseInt(s, 10, 64); err == nil {
				allowed[id] = true
			}
		}
	}

	sttURL := os.Getenv("STT_URL")
	if sttURL == "" {
		sttURL = "http://127.0.0.1:8092"
	}
	sttLang := os.Getenv("STT_LANGUAGE")
	if sttLang == "" {
		sttLang = "ru"
	}

	return &Channel{
		bot:     bot,
		dl:      dl,
		bus:     msgBus,
		allowed: allowed,
		sttURL:  sttURL,
		sttLang: sttLang,
	}, nil
}

// botHTTPTimeout bounds every Bot API request; above the 30 s long-poll.
const botHTTPTimeout = 75 * time.Second

// httpDoer is the part of *http.Client the voice download needs.
type httpDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// newBot builds the bot on a client that scrubs the bot token out of every
// error: tgbotapi's requests fail with a *url.Error whose text carries
// bot<TOKEN>. The same client is returned for file downloads (their URL embeds
// the token too).
func newBot(token, endpoint string, base *http.Client) (*tgbotapi.BotAPI, *tgsafe.HTTPClient, error) {
	hc := tgsafe.NewHTTPClient(base, token)
	bot, err := tgbotapi.NewBotAPIWithClient(token, endpoint, hc)
	if err != nil {
		return nil, nil, err
	}
	return bot, hc, nil
}

// startUpdates installs the scrubbing SDK logger and starts long polling.
// GetUpdatesChan prints every failed poll through the SDK's package logger
// (stderr by default), which would carry the token.
func (c *Channel) startUpdates() tgbotapi.UpdatesChannel {
	_ = tgbotapi.SetLogger(tgsafe.NewLogger(slog.Default(), c.bot.Token))
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 30
	return c.bot.GetUpdatesChan(u)
}

func (c *Channel) Start(ctx context.Context) {
	c.ctx = ctx

	slog.Info("telegram bot started",
		slog.String("username", c.bot.Self.UserName),
		slog.Int("allowed_users", len(c.allowed)))

	updates := c.startUpdates()

	go c.pollUpdates(ctx, updates)
	go c.dispatchOutbound(ctx)
}

func (c *Channel) pollUpdates(ctx context.Context, updates tgbotapi.UpdatesChannel) {
	for {
		select {
		case <-ctx.Done():
			c.bot.StopReceivingUpdates()
			return
		case update, ok := <-updates:
			if !ok {
				return
			}
			if update.Message != nil {
				c.handleMessage(update.Message)
			}
		}
	}
}

func (c *Channel) dispatchOutbound(ctx context.Context) {
	for {
		msg, ok := c.bus.SubscribeOutbound(ctx)
		if !ok {
			return
		}
		if msg.Channel != "telegram" {
			continue
		}
		c.sendReply(msg)
	}
}

func (c *Channel) handleMessage(msg *tgbotapi.Message) {
	if msg.From == nil {
		return
	}

	userID := msg.From.ID
	if len(c.allowed) > 0 && !c.allowed[userID] {
		slog.Warn("telegram: unauthorized user", slog.Int64("user_id", userID))
		return
	}

	text := msg.Text
	if text == "" {
		text = msg.Caption
	}
	if text == "" && msg.Voice != nil {
		transcribed, err := c.transcribeVoice(context.Background(), msg.Voice)
		if err != nil {
			slog.Error("telegram: voice transcription failed", slog.Any("error", err))
			return
		}
		text = transcribed
	}
	if text == "" {
		return // ignore non-text messages
	}

	chatID := strconv.FormatInt(msg.Chat.ID, 10)

	if _, err := c.bot.Send(tgbotapi.NewChatAction(msg.Chat.ID, tgbotapi.ChatTyping)); err != nil {
		slog.Debug("telegram: failed to send typing indicator", slog.Any("error", err))
	}
	stopChan := make(chan struct{})
	c.stopTyping.Store(chatID, stopChan)
	go c.typingLoop(msg.Chat.ID, stopChan)

	senderID := strconv.FormatInt(userID, 10)
	c.bus.PublishInbound(bus.Message{
		ID:        fmt.Sprintf("tg-%d", msg.MessageID),
		Channel:   "telegram",
		SenderID:  senderID,
		ChatID:    chatID,
		Text:      text,
		Timestamp: time.Now(),
	})
}

func (c *Channel) sendReply(msg bus.Message) {
	chatID, err := strconv.ParseInt(msg.ChatID, 10, 64)
	if err != nil {
		slog.Error("telegram: invalid chat ID", slog.String("chat_id", msg.ChatID))
		return
	}

	if stop, ok := c.stopTyping.LoadAndDelete(msg.ChatID); ok {
		if ch, ok := stop.(chan struct{}); ok {
			close(ch)
		}
	}

	// Photo attachment path: when bytes are present, send as a Telegram photo.
	// Text becomes the caption (rune-safe truncation to Telegram's 1024-char cap;
	// a byte-slice would corrupt a multi-byte rune — e.g. Cyrillic — at the cut).
	if len(msg.Photo) > 0 {
		caption := engine.TruncateRunesEllipsis(msg.Text, engine.MaxCaptionRunes)
		photo := tgbotapi.NewPhoto(chatID, tgbotapi.FileBytes{Name: "alert.png", Bytes: msg.Photo})
		if caption != "" {
			photo.Caption = caption
		}
		if _, err := c.bot.Send(photo); err != nil {
			slog.Error("telegram: photo send failed, falling back to text", slog.Any("error", err))
			// fall through to text path so the alert still reaches the operator
		} else {
			slog.Info("telegram: photo sent", slog.String("chat_id", msg.ChatID), slog.Int("photo_bytes", len(msg.Photo)))
			engine.DefaultTGLog.Record(engine.TGMessage{
				Kind:      engine.ClassifyKind(msg.ID),
				ChatID:    msg.ChatID,
				Text:      caption,
				HasPhoto:  true,
				Timestamp: time.Now(),
			})
			return
		}
	}

	text := msg.Text
	if text == "" {
		return
	}

	text = sanitizeUTF8(text)

	slog.Info("telegram: sending reply",
		slog.String("chat_id", msg.ChatID),
		slog.Int("length", len(text)))

	htmlText := markdownToTelegramHTML(text)
	sent := false
	if err := c.sendChunked(chatID, htmlText, tgbotapi.ModeHTML); err != nil {
		slog.Warn("telegram: HTML send failed, falling back to plain text", slog.Any("error", err))
		plain := stripMarkdown(text)
		if err := c.sendChunked(chatID, plain, ""); err != nil {
			slog.Error("telegram: send failed", slog.Any("error", err))
		} else {
			sent = true
		}
	} else {
		sent = true
	}
	if sent {
		engine.DefaultTGLog.Record(engine.TGMessage{
			Kind:      engine.ClassifyKind(msg.ID),
			ChatID:    msg.ChatID,
			Text:      text,
			HasPhoto:  false,
			Timestamp: time.Now(),
		})
	}
}

func (c *Channel) sendChunked(chatID int64, text string, parseMode string) error {
	chunks := splitMessage(text, tgfmt.MaxMessageLen)
	for _, chunk := range chunks {
		if chunk == "" {
			continue
		}
		tgMsg := tgbotapi.NewMessage(chatID, chunk)
		if parseMode != "" {
			tgMsg.ParseMode = parseMode
		}
		if err := c.sendWithRetry(tgMsg); err != nil {
			return err
		}
	}
	return nil
}

func (c *Channel) sendWithRetry(msg tgbotapi.Chattable) error {
	const maxRetries = 3
	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		_, err := c.bot.Send(msg)
		if err == nil {
			return nil
		}
		lastErr = err
		if !tgfmt.IsTransientError(err) {
			return err
		}
		slog.Warn("telegram: transient error, retrying",
			slog.Int("attempt", attempt+1),
			slog.Any("error", err))
		time.Sleep(time.Duration(attempt+1) * 500 * time.Millisecond)
	}
	return fmt.Errorf("telegram send failed after %d retries: %w", maxRetries, lastErr)
}

func (c *Channel) typingLoop(chatID int64, stop <-chan struct{}) {
	ticker := time.NewTicker(4 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			_, _ = c.bot.Send(tgbotapi.NewChatAction(chatID, tgbotapi.ChatTyping))
		}
	}
}
