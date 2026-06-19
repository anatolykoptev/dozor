package session

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// SummarizeFn is called by the compactor to get an LLM summary.
type SummarizeFn func(ctx context.Context, prompt string) (string, error)

const defaultMultiPartMin = 10

// Compactor manages session compaction.
type Compactor struct {
	Store          Store
	Summarize      SummarizeFn
	Threshold      int  // trigger when MessageCount >= this
	KeepLast       int  // messages to retain
	ExtractFacts   bool // true = parse "- " bullets as facts
	MultiPart      bool // split large histories before summarizing
	MultiPartMin   int  // minimum messages for multi-part split (default: 10)
	MaxTokensGuard int  // skip messages with len(Content)/4 > this
}

// Compact compacts the session identified by key.
func (c *Compactor) Compact(ctx context.Context, key string) {
	if c.Store.MessageCount(key) < c.Threshold {
		return
	}

	removed := c.Store.CompactMessages(key, c.KeepLast)
	if len(removed) == 0 {
		return
	}

	prompt := c.buildPrompt(key, removed)

	var result string
	var err error

	minPart := c.MultiPartMin
	if minPart <= 0 {
		minPart = defaultMultiPartMin
	}
	if c.MultiPart && len(removed) > minPart {
		result, err = c.multiPartSummarize(ctx, key, removed)
	} else {
		result, err = c.Summarize(ctx, prompt)
	}

	if err != nil {
		return
	}

	if c.ExtractFacts {
		facts := parseFacts(result)
		if len(facts) > 0 {
			c.Store.AddFacts(key, facts)
		} else {
			c.Store.SetSummary(key, result)
		}
	} else {
		c.Store.SetSummary(key, result)
	}
}

func (c *Compactor) buildPrompt(key string, msgs []Message) string {
	var b strings.Builder

	existing := c.Store.GetSummary(key)
	if existing != "" {
		fmt.Fprintf(&b, "Previous summary:\n%s\n\n", existing)
	}

	if c.ExtractFacts {
		b.WriteString("Extract key facts as a bullet list (each line starting with \"- \") from this conversation:\n\n")
	} else {
		b.WriteString("Summarize the following conversation:\n\n")
	}

	for _, m := range msgs {
		if c.MaxTokensGuard > 0 && len(m.Content)/4 > c.MaxTokensGuard {
			continue
		}
		fmt.Fprintf(&b, "%s: %s\n", m.Role, m.Content)
	}

	return b.String()
}

func (c *Compactor) multiPartSummarize(
	ctx context.Context, key string, removed []Message,
) (string, error) {
	mid := len(removed) / 2 //nolint:mnd // split in half
	first := removed[:mid]
	second := removed[mid:]

	p1 := c.buildPromptFromMessages(key, first)
	s1, err := c.Summarize(ctx, p1)
	if err != nil {
		return "", err
	}

	p2 := c.buildPromptFromMessages(key, second)
	s2, err := c.Summarize(ctx, p2)
	if err != nil {
		return "", err
	}

	mergePrompt := fmt.Sprintf(
		"Summarize these two summaries into one:\n\n1: %s\n\n2: %s",
		s1, s2,
	)
	return c.Summarize(ctx, mergePrompt)
}

func (c *Compactor) buildPromptFromMessages(
	key string, msgs []Message,
) string {
	var b strings.Builder

	existing := c.Store.GetSummary(key)
	if existing != "" {
		fmt.Fprintf(&b, "Previous summary:\n%s\n\n", existing)
	}

	b.WriteString("Summarize the following conversation:\n\n")
	for _, m := range msgs {
		if c.MaxTokensGuard > 0 && len(m.Content)/4 > c.MaxTokensGuard {
			continue
		}
		fmt.Fprintf(&b, "%s: %s\n", m.Role, m.Content)
	}

	return b.String()
}

func parseFacts(text string) []Fact {
	var facts []Fact
	now := time.Now()

	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimSpace(line)
		if content, ok := strings.CutPrefix(line, "- "); ok {
			content = strings.TrimSpace(content)
			if content != "" {
				facts = append(facts, Fact{
					Content:     content,
					ExtractedAt: now,
				})
			}
		}
	}

	return facts
}
