package bus

import (
	"context"
	"sync"
	"time"
)

// Message represents a message flowing through the bus.
type Message struct {
	ID        string
	Channel   string // "telegram", "a2a", "internal"
	SenderID  string
	ChatID    string
	Text      string
	Timestamp time.Time
}

// Bus is a simple message bus with inbound and outbound channels.
type Bus struct {
	inbound  chan Message
	outbound chan Message
	closed   chan struct{}
	once     sync.Once
}

// New creates a new message bus.
func New() *Bus {
	return &Bus{
		inbound:  make(chan Message, 100),
		outbound: make(chan Message, 100),
		closed:   make(chan struct{}),
	}
}

// PublishInbound sends a message to the agent for processing.
func (b *Bus) PublishInbound(msg Message) {
	select {
	case <-b.closed:
		return
	default:
		select {
		case b.inbound <- msg:
		case <-b.closed:
		}
	}
}

// ConsumeInbound blocks until a message is available or context is canceled.
func (b *Bus) ConsumeInbound(ctx context.Context) (Message, bool) {
	select {
	case msg, ok := <-b.inbound:
		return msg, ok
	case <-ctx.Done():
		return Message{}, false
	case <-b.closed:
		return Message{}, false
	}
}

// PublishOutbound sends a response message to channels.
func (b *Bus) PublishOutbound(msg Message) {
	select {
	case <-b.closed:
		return
	default:
		select {
		case b.outbound <- msg:
		case <-b.closed:
		}
	}
}

// SubscribeOutbound blocks until an outbound message is available.
func (b *Bus) SubscribeOutbound(ctx context.Context) (Message, bool) {
	select {
	case msg, ok := <-b.outbound:
		return msg, ok
	case <-ctx.Done():
		return Message{}, false
	case <-b.closed:
		return Message{}, false
	}
}

// Close shuts down the bus.
func (b *Bus) Close() {
	b.once.Do(func() {
		close(b.closed)
	})
}
