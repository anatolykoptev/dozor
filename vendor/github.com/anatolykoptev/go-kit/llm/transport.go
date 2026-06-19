package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content          string     `json:"content"`
			ReasoningContent string     `json:"reasoning_content,omitempty"`
			ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *Usage `json:"usage,omitempty"`
}

func (c *Client) doWithRetry(ctx context.Context, baseURL, apiKey string, req *ChatRequest) (*ChatResponse, error) {
	delay := retryDelay
	var lastErr error

	for attempt := range c.maxRetries {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
			delay = min(delay*2, maxRetryDelay)
		}

		result, err := c.doRequest(ctx, baseURL, apiKey, req)
		if err == nil {
			return result, nil
		}
		lastErr = err

		// Only retry on retryable errors.
		if !asRetryable(err) {
			return nil, err
		}
	}
	return nil, lastErr
}

func (c *Client) doRequest(ctx context.Context, baseURL, apiKey string, req *ChatRequest) (*ChatResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := c.httpClient.Do(httpReq) //nolint:gosec // G704: URL comes from caller config, not user input
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, newAPIError(resp.StatusCode, string(respBody), isRetryableStatus(resp.StatusCode), parseRetryAfter(resp.Header.Get("Retry-After")))
	}

	var chatResp chatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if len(chatResp.Choices) == 0 {
		return nil, errors.New("llm: empty choices in response")
	}

	msg := chatResp.Choices[0].Message
	clean, reasoning := splitReasoning(msg.Content, msg.ReasoningContent)
	return &ChatResponse{
		Content:      clean,
		Reasoning:    reasoning,
		ToolCalls:    msg.ToolCalls,
		FinishReason: chatResp.Choices[0].FinishReason,
		Usage:        chatResp.Usage,
	}, nil
}

// hasNonCooledEndpoint reports whether at least one chain endpoint's model is
// NOT currently in cooldown. Used to gate skipping so the loop never fail-closed
// (when every model is cooled, no skip happens and the primary is still tried).
func (c *Client) hasNonCooledEndpoint() bool {
	for _, ep := range c.endpoints {
		if !c.cooldown.cooling(ep.Model) {
			return true
		}
	}
	return false
}

// cooldownCandidates returns the endpoints to iterate and whether cooled models
// should be skipped, applying quota-aware cooldown selection:
//   - cooldown disabled → the full chain, no skipping (unchanged behaviour).
//   - cooldown enabled, ≥1 healthy → the full chain, skip cooled models
//     (degraded > dead).
//   - cooldown enabled, ALL cooled → never fail-closed: only the PRIMARY (one
//     last-resort upstream probe) so its real error surfaces, instead of burning
//     the whole known-dead chain.
func (c *Client) cooldownCandidates() (endpoints []Endpoint, skipCooled bool) {
	if c.cooldown == nil {
		return c.endpoints, false
	}
	if c.hasNonCooledEndpoint() {
		return c.endpoints, true
	}
	return c.endpoints[:1], false
}

// recordCooldownOutcome feeds an attempt result to the cooldown bookkeeping: a
// success clears the model's cooldown; a quota-class failure drives it. No-op
// when cooldown is disabled.
func (c *Client) recordCooldownOutcome(ep Endpoint, err error) {
	if c.cooldown == nil {
		return
	}
	if err == nil {
		c.cooldown.recordSuccess(ep.Model)
		return
	}
	if isQuotaError(err) {
		var apiErr *APIError
		_ = errors.As(err, &apiErr)
		var retryAfter time.Duration
		if apiErr != nil {
			retryAfter = apiErr.RetryAfter
		}
		c.cooldown.recordFailure(ep.Model, retryAfter)
	}
}

// attemptEndpoint performs ONE chain-endpoint attempt: it applies the per-model
// override, the optional per-attempt timeout, fires the endpoint observer, and
// feeds the cooldown bookkeeping. The single authority for "try one endpoint" —
// shared by the chain loop and the never-fail-closed race guard so both paths
// stay observably identical (same observer + cooldown side effects). It does NOT
// classify the error for chain advancement (DeadlineExceeded / failover /
// retryable); that loop-control logic stays in executeInner.
func (c *Client) attemptEndpoint(ctx context.Context, ep Endpoint, req *ChatRequest) (*ChatResponse, error) {
	epReq := *req
	if ep.Model != "" {
		epReq.Model = ep.Model
	}

	// Per-attempt timeout: derive a child ctx bounded by d, but only when
	// d > 0 and WithEndpoints is in use. The outer ctx remains the absolute
	// ceiling — context.WithTimeout takes min(d, time-left-on-outer).
	attemptCtx := ctx
	var cancelAttempt context.CancelFunc
	if c.perAttemptTimeout > 0 {
		attemptCtx, cancelAttempt = context.WithTimeout(ctx, c.perAttemptTimeout)
	}

	result, err := c.doWithRetry(attemptCtx, ep.URL, ep.Key, &epReq)

	if cancelAttempt != nil {
		cancelAttempt()
	}
	// Served-model attribution: the model that returned the 200 is this
	// endpoint's effective model. Set here (the single "try one endpoint"
	// authority) so BOTH the chain loop and the never-fail-closed race guard
	// attribute identically. epReq.Model carries the per-endpoint override when
	// set; fall back to req.Model otherwise.
	if err == nil && result != nil {
		result.ServedBy = epReq.Model
	}
	if c.endpointObserver != nil {
		c.endpointObserver(ep, err)
	}
	// Feed the cooldown bookkeeping: success clears, quota-fail drives.
	c.recordCooldownOutcome(ep, err)
	return result, err
}

func (c *Client) executeInner(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	if len(c.endpoints) > 0 {
		var lastErr error
		attempted := false
		endpoints, skipCooled := c.cooldownCandidates()
		for _, ep := range endpoints {
			// Skip a model in quota cooldown, but only while a healthier
			// candidate remains — degraded > dead.
			if skipCooled && c.cooldown.cooling(ep.Model) {
				continue
			}
			attempted = true

			result, err := c.attemptEndpoint(ctx, ep, req)
			if err == nil {
				return result, nil
			}
			lastErr = err

			// A per-attempt DeadlineExceeded where the outer ctx is still alive
			// means this endpoint was slow (not a genuine give-up by the caller).
			// Treat it as retryable-advance: continue to the next endpoint.
			// If the outer ctx is also done, fall through to the asRetryable gate,
			// which will return non-retryable → abort the chain (correct).
			if c.perAttemptTimeout > 0 && errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
				continue
			}

			// A "request too large for this model" failure (413 TPM/payload, or
			// 400 context_length_exceeded) is non-retryable on THIS endpoint — the
			// identical request recurs — but the next model in the chain may have a
			// larger context window or token budget. Advance to it instead of
			// aborting. The endpointObserver has already fired with this endpoint's
			// error, so the failover stays observable.
			if asFailover(err) {
				continue
			}

			if !asRetryable(err) {
				return nil, err
			}
		}
		// Race guard: cooldownCandidates() snapshotted skipCooled=true (≥1 model
		// healthy at that instant), but a concurrent goroutine cooled the last
		// healthy model between the snapshot and the per-iteration cooling()
		// re-check, so EVERY endpoint was skipped — `attempted` is false and
		// lastErr is nil. Returning here would yield (nil, nil) → a nil-deref in
		// CompleteRaw and every public caller. Never fail-closed: force one
		// last-resort attempt on the primary (degraded > dead) so a real response
		// or a real upstream error always surfaces. (nil, nil) is then
		// structurally impossible.
		if !attempted {
			return c.attemptEndpoint(ctx, endpoints[0], req)
		}
		return nil, lastErr
	}
	result, err := c.doWithRetry(ctx, c.baseURL, c.apiKey, req)
	if err == nil {
		return result, nil
	}
	for _, key := range c.fallbackKeys {
		if key == "" {
			continue
		}
		result, err = c.doWithRetry(ctx, c.baseURL, key, req)
		if err == nil {
			return result, nil
		}
	}
	return nil, err
}
