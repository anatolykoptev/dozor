package provider

import (
	"context"
	"errors"
	"os"
	"testing"

	kitllm "github.com/anatolykoptev/go-kit/llm"
)

func TestNewOpenAI_EmptyKeyReturnsUnavailable(t *testing.T) {
	os.Setenv("DOZOR_LLM_URL", "http://example/v1")
	os.Setenv("DOZOR_LLM_MODEL", "test-model")
	os.Unsetenv("DOZOR_LLM_API_KEY")
	defer os.Unsetenv("DOZOR_LLM_URL")
	defer os.Unsetenv("DOZOR_LLM_MODEL")

	p, ok := NewOpenAI()
	if ok {
		t.Fatal("expected ok=false on empty key")
	}
	if p == nil {
		t.Fatal("expected non-nil provider")
	}
	_, err := p.Chat(context.Background(), nil, nil)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("want errors.Is(err, ErrUnavailable), got %v", err)
	}
	if !errors.Is(err, kitllm.ErrUnavailable) {
		t.Fatal("sentinel must be shared with kitllm.ErrUnavailable")
	}
}

func TestNewOpenAI_KeyReturnsRealClient(t *testing.T) {
	os.Setenv("DOZOR_LLM_URL", "http://example/v1")
	os.Setenv("DOZOR_LLM_MODEL", "test-model")
	os.Setenv("DOZOR_LLM_API_KEY", "test-key")
	defer os.Unsetenv("DOZOR_LLM_URL")
	defer os.Unsetenv("DOZOR_LLM_MODEL")
	defer os.Unsetenv("DOZOR_LLM_API_KEY")

	p, ok := NewOpenAI()
	if !ok {
		t.Fatal("expected ok=true with key")
	}
	if _, isUnavail := p.(unavailable); isUnavail {
		t.Fatal("expected real OpenAI client, got unavailable{}")
	}
}
