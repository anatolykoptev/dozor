package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anatolykoptev/dozor/internal/bus"
)

func TestRunMessageLoop_LogsInboundText(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	slog.SetDefault(logger)
	defer slog.SetDefault(slog.Default())

	msgBus := bus.New()
	defer msgBus.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		runMessageLoop(ctx, messageLoopDeps{
			msgBus:      msgBus,
			adminChatID: "123",
		})
	}()

	msgBus.PublishInbound(bus.Message{
		ID:       "test-1",
		Channel:  "telegram",
		SenderID: "428660",
		ChatID:   "428660",
		Text:     "check server status",
	})

	time.Sleep(200 * time.Millisecond)
	cancel()
	wg.Wait()

	logs := buf.String()
	if !strings.Contains(logs, "check server status") {
		t.Error("inbound message text not found in logs")
	}
}
