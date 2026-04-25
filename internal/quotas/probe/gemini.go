package probe

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
)

const geminiVendor = "gemini"

const geminiBaseURL = "https://generativelanguage.googleapis.com"

// GeminiProber probes Google AI Gemini free-tier quota by inspecting rate-limit
// headers from the models list endpoint.
//
// The Gemini free-tier API returns X-RateLimit-Remaining / X-RateLimit-Limit
// headers on every request. We use the models list call (cheap, no billing)
// as the probe. Paid quotas with separate limits are not observable via headers —
// this probe is best-effort and degrades gracefully.
type GeminiProber struct {
	apiKey  string
	client  *http.Client
	baseURL string // overridable in tests
}

// NewGemini returns a GeminiProber. Returns nil if apiKey is empty.
func NewGemini(apiKey string) *GeminiProber {
	if apiKey == "" {
		return nil
	}
	return &GeminiProber{
		apiKey:  apiKey,
		client:  &http.Client{Timeout: ProbeTimeout},
		baseURL: geminiBaseURL,
	}
}

func (g *GeminiProber) Vendor() string { return geminiVendor }

type geminiModelsResponse struct {
	Models []struct {
		Name string `json:"name"`
	} `json:"models"`
}

func (g *GeminiProber) Probe(ctx context.Context) ([]Reading, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		g.baseURL+"/v1beta/models?key="+g.apiKey, nil)
	if err != nil {
		return nil, err
	}

	resp, err := g.client.Do(req)
	if err != nil {
		return nil, &timeoutOrNetErr{err}
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return []Reading{{Product: "api_requests", Remaining: 0}}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &httpStatusErr{status: resp.StatusCode}
	}

	var models geminiModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&models); err != nil {
		return nil, &parseErr{err}
	}

	if pct, ok := parseRateLimitPct(resp); ok {
		return []Reading{{Product: "api_requests", Remaining: pct}}, nil
	}

	// No rate-limit headers — API is reachable (200 OK), report 100% as a proxy
	// for "not exhausted". This is the common paid-tier case.
	return []Reading{{Product: "api_requests", Remaining: 100}}, nil
}

// parseRateLimitPct reads X-RateLimit-Limit/Remaining headers and returns the
// percentage remaining. Returns (0, false) if headers are absent or unparseable.
func parseRateLimitPct(resp *http.Response) (float64, bool) {
	limitHdr := resp.Header.Get("X-RateLimit-Limit")
	remainingHdr := resp.Header.Get("X-RateLimit-Remaining")
	if limitHdr == "" || remainingHdr == "" {
		return 0, false
	}
	limit, err1 := strconv.ParseFloat(limitHdr, 64)
	remaining, err2 := strconv.ParseFloat(remainingHdr, 64)
	if err1 != nil || err2 != nil || limit <= 0 {
		return 0, false
	}
	pct := remaining / limit * 100
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	return pct, true
}
