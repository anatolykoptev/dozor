package bus

import (
	"context"
	"testing"
	"time"
)

func TestPublishInbound_ConsumeInbound(t *testing.T) {
	b := New()
	defer b.Close()

	msg := Message{
		ID:        "msg-1",
		Channel:   "telegram",
		SenderID:  "user-42",
		ChatID:    "chat-99",
		Text:      "hello",
		Timestamp: time.Now(),
	}

	b.PublishInbound(msg)

	ctx := context.Background()
	got, ok := b.ConsumeInbound(ctx)
	if !ok {
		t.Fatal("ConsumeInbound returned ok=false, want true")
	}
	if got.ID != msg.ID {
		t.Errorf("ID: got %q, want %q", got.ID, msg.ID)
	}
	if got.Text != msg.Text {
		t.Errorf("Text: got %q, want %q", got.Text, msg.Text)
	}
	if got.Channel != msg.Channel {
		t.Errorf("Channel: got %q, want %q", got.Channel, msg.Channel)
	}
}

func TestPublishOutbound_SubscribeOutbound(t *testing.T) {
	b := New()
	defer b.Close()

	msg := Message{
		ID:      "out-1",
		Channel: "internal",
		Text:    "response text",
	}

	b.PublishOutbound(msg)

	ctx := context.Background()
	got, ok := b.SubscribeOutbound(ctx)
	if !ok {
		t.Fatal("SubscribeOutbound returned ok=false, want true")
	}
	if got.ID != msg.ID {
		t.Errorf("ID: got %q, want %q", got.ID, msg.ID)
	}
	if got.Text != msg.Text {
		t.Errorf("Text: got %q, want %q", got.Text, msg.Text)
	}
}

func TestConsumeInbound_ContextCancellation(t *testing.T) {
	b := New()
	defer b.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately before consuming

	_, ok := b.ConsumeInbound(ctx)
	if ok {
		t.Error("ConsumeInbound returned ok=true on cancelled context, want false")
	}
}

func TestSubscribeOutbound_ContextCancellation(t *testing.T) {
	b := New()
	defer b.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately before subscribing

	_, ok := b.SubscribeOutbound(ctx)
	if ok {
		t.Error("SubscribeOutbound returned ok=true on cancelled context, want false")
	}
}

func TestConsumeInbound_ContextCancelledWhileBlocking(t *testing.T) {
	b := New()
	defer b.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, ok := b.ConsumeInbound(ctx)
	elapsed := time.Since(start)

	if ok {
		t.Error("ConsumeInbound returned ok=true, want false after context timeout")
	}
	// Should have returned promptly on timeout, not hung.
	if elapsed > 500*time.Millisecond {
		t.Errorf("ConsumeInbound blocked too long: %v", elapsed)
	}
}

func TestSubscribeOutbound_ContextCancelledWhileBlocking(t *testing.T) {
	b := New()
	defer b.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, ok := b.SubscribeOutbound(ctx)
	elapsed := time.Since(start)

	if ok {
		t.Error("SubscribeOutbound returned ok=true, want false after context timeout")
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("SubscribeOutbound blocked too long: %v", elapsed)
	}
}

func TestMessageOrdering_Inbound(t *testing.T) {
	b := New()
	defer b.Close()

	texts := []string{"first", "second", "third", "fourth", "fifth"}
	for _, text := range texts {
		b.PublishInbound(Message{Text: text})
	}

	ctx := context.Background()
	for i, want := range texts {
		got, ok := b.ConsumeInbound(ctx)
		if !ok {
			t.Fatalf("message %d: ConsumeInbound returned ok=false", i)
		}
		if got.Text != want {
			t.Errorf("message %d: got %q, want %q", i, got.Text, want)
		}
	}
}

func TestMessageOrdering_Outbound(t *testing.T) {
	b := New()
	defer b.Close()

	texts := []string{"alpha", "beta", "gamma", "delta"}
	for _, text := range texts {
		b.PublishOutbound(Message{Text: text})
	}

	ctx := context.Background()
	for i, want := range texts {
		got, ok := b.SubscribeOutbound(ctx)
		if !ok {
			t.Fatalf("message %d: SubscribeOutbound returned ok=false", i)
		}
		if got.Text != want {
			t.Errorf("message %d: got %q, want %q", i, got.Text, want)
		}
	}
}

func TestPublishInbound_AfterClose_DoesNotBlock(t *testing.T) {
	b := New()
	b.Close()

	// Should return immediately without blocking or panicking.
	done := make(chan struct{})
	go func() {
		b.PublishInbound(Message{Text: "dropped"})
		close(done)
	}()

	select {
	case <-done:
		// OK
	case <-time.After(time.Second):
		t.Error("PublishInbound blocked after Close")
	}
}

func TestPublishOutbound_AfterClose_DoesNotBlock(t *testing.T) {
	b := New()
	b.Close()

	done := make(chan struct{})
	go func() {
		b.PublishOutbound(Message{Text: "dropped"})
		close(done)
	}()

	select {
	case <-done:
		// OK
	case <-time.After(time.Second):
		t.Error("PublishOutbound blocked after Close")
	}
}

func TestConsumeInbound_AfterClose(t *testing.T) {
	b := New()
	b.Close()

	ctx := context.Background()
	_, ok := b.ConsumeInbound(ctx)
	if ok {
		t.Error("ConsumeInbound returned ok=true after Close, want false")
	}
}

func TestSubscribeOutbound_AfterClose(t *testing.T) {
	b := New()
	b.Close()

	ctx := context.Background()
	_, ok := b.SubscribeOutbound(ctx)
	if ok {
		t.Error("SubscribeOutbound returned ok=true after Close, want false")
	}
}

func TestClose_Idempotent(t *testing.T) {
	b := New()
	// Calling Close multiple times must not panic.
	b.Close()
	b.Close()
	b.Close()
}
