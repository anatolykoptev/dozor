package telegram

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anatolykoptev/dozor/internal/bus"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Channel is a Telegram bot that bridges messages to/from the bus.
type Channel struct {
	bot     *tgbotapi.BotAPI
	bus     *bus.Bus
	allowed map[int64]bool // whitelisted user IDs
	ctx     context.Context

	// typing indicator cancellation per chat
	stopTyping sync.Map // chatID string → chan struct{}
}

// New creates a new Telegram channel from environment variables.
// DOZOR_TELEGRAM_TOKEN — bot token (required)
// DOZOR_TELEGRAM_ALLOWED — comma-separated user IDs
func New(msgBus *bus.Bus) (*Channel, error) {
	token := os.Getenv("DOZOR_TELEGRAM_TOKEN")
	if token == "" {
		return nil, fmt.Errorf("DOZOR_TELEGRAM_TOKEN not set")
	}

	bot, err := tgbotapi.NewBotAPI(token)
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

	return &Channel{
		bot:     bot,
		bus:     msgBus,
		allowed: allowed,
	}, nil
}

// Start begins polling for updates and dispatching outbound messages.
func (c *Channel) Start(ctx context.Context) {
	c.ctx = ctx

	slog.Info("telegram bot started",
		slog.String("username", c.bot.Self.UserName),
		slog.Int("allowed_users", len(c.allowed)))

	// Inbound: poll for updates.
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 30
	updates := c.bot.GetUpdatesChan(u)

	go func() {
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
	}()

	// Outbound: send replies matching "telegram" channel.
	go func() {
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
	}()
}

func (c *Channel) handleMessage(msg *tgbotapi.Message) {
	if msg.From == nil {
		return
	}

	userID := msg.From.ID
	// Whitelist check (skip if no whitelist configured — open to all).
	if len(c.allowed) > 0 && !c.allowed[userID] {
		slog.Warn("telegram: unauthorized user", slog.Int64("user_id", userID))
		return
	}

	text := msg.Text
	if text == "" {
		text = msg.Caption
	}
	if text == "" {
		return // ignore non-text messages
	}

	chatID := fmt.Sprintf("%d", msg.Chat.ID)

	// Start typing indicator.
	c.bot.Send(tgbotapi.NewChatAction(msg.Chat.ID, tgbotapi.ChatTyping))
	stopChan := make(chan struct{})
	c.stopTyping.Store(chatID, stopChan)
	go c.typingLoop(msg.Chat.ID, stopChan)

	senderID := fmt.Sprintf("%d", userID)
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

	// Stop typing indicator.
	if stop, ok := c.stopTyping.LoadAndDelete(msg.ChatID); ok {
		if ch, ok := stop.(chan struct{}); ok {
			close(ch)
		}
	}

	text := msg.Text
	if text == "" {
		return
	}

	text = sanitizeUTF8(text)

	// Try HTML mode first, fall back to plain text.
	htmlText := markdownToTelegramHTML(text)
	if err := c.sendChunked(chatID, htmlText, tgbotapi.ModeHTML); err != nil {
		slog.Warn("telegram: HTML send failed, falling back to plain text", slog.Any("error", err))
		plain := stripMarkdown(text)
		if err := c.sendChunked(chatID, plain, ""); err != nil {
			slog.Error("telegram: send failed", slog.Any("error", err))
		}
	}
}

func (c *Channel) sendChunked(chatID int64, text string, parseMode string) error {
	const maxLen = 4096
	chunks := splitMessage(text, maxLen)
	for _, chunk := range chunks {
		if chunk == "" {
			continue
		}
		tgMsg := tgbotapi.NewMessage(chatID, chunk)
		if parseMode != "" {
			tgMsg.ParseMode = parseMode
		}
		if _, err := c.bot.Send(tgMsg); err != nil {
			return err
		}
	}
	return nil
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
			c.bot.Send(tgbotapi.NewChatAction(chatID, tgbotapi.ChatTyping))
		}
	}
}
