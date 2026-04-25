package probe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

const anthropicVendor = "anthropic"

const anthropicAPIBase = "https://api.anthropic.com"

// AnthropicProber probes Anthropic organization spend against spending limit.
type AnthropicProber struct {
	adminKey string
	orgID    string
	client   *http.Client
	baseURL  string // overridable in tests
}

// NewAnthropic returns an AnthropicProber. Returns nil if adminKey is empty.
func NewAnthropic(adminKey, orgID string) *AnthropicProber {
	if adminKey == "" {
		return nil
	}
	return &AnthropicProber{
		adminKey: adminKey,
		orgID:    orgID,
		client:   &http.Client{Timeout: ProbeTimeout},
		baseURL:  anthropicAPIBase,
	}
}

func (a *AnthropicProber) Vendor() string { return anthropicVendor }

type anthropicUsageReport struct {
	Data []struct {
		InputTokens  int64 `json:"input_tokens"`
		OutputTokens int64 `json:"output_tokens"`
		CacheTokens  int64 `json:"cache_read_input_tokens"`
	} `json:"data"`
}

// anthropicOrg holds spending limit info from /v1/organizations endpoint.
type anthropicOrg struct {
	SpendingLimitUSD float64 `json:"monthly_spending_limit_usd"`
}

func (a *AnthropicProber) Probe(ctx context.Context) ([]Reading, error) {
	limit, err := a.getSpendingLimit(ctx)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		return nil, &parseErr{errors.New("anthropic: no monthly_spending_limit_usd set")}
	}

	used, err := a.getMonthlySpend(ctx)
	if err != nil {
		return nil, err
	}

	remaining := (limit - used) / limit * 100
	if remaining < 0 {
		remaining = 0
	}
	if remaining > 100 {
		remaining = 100
	}

	return []Reading{{Product: "spend", Remaining: remaining}}, nil
}

func (a *AnthropicProber) getSpendingLimit(ctx context.Context) (float64, error) {
	url := a.baseURL + "/v1/organizations"
	if a.orgID != "" {
		url = fmt.Sprintf("%s/v1/organizations/%s", a.baseURL, a.orgID)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("x-api-key", a.adminKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := a.client.Do(req)
	if err != nil {
		return 0, &timeoutOrNetErr{err}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, &httpStatusErr{status: resp.StatusCode}
	}

	var org anthropicOrg
	if err := json.NewDecoder(resp.Body).Decode(&org); err != nil {
		return 0, &parseErr{err}
	}
	return org.SpendingLimitUSD, nil
}

func (a *AnthropicProber) getMonthlySpend(ctx context.Context) (float64, error) {
	now := time.Now().UTC()
	startDate := fmt.Sprintf("%d-%02d-01", now.Year(), now.Month())

	url := fmt.Sprintf(
		"%s/v1/organizations/%s/usage/messages?start_time=%s",
		a.baseURL, a.orgID, startDate,
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("x-api-key", a.adminKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := a.client.Do(req)
	if err != nil {
		return 0, &timeoutOrNetErr{err}
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusUnprocessableEntity {
		return 0, nil
	}
	if resp.StatusCode != http.StatusOK {
		return 0, &httpStatusErr{status: resp.StatusCode}
	}

	var report anthropicUsageReport
	if err := json.NewDecoder(resp.Body).Decode(&report); err != nil {
		return 0, &parseErr{err}
	}

	const (
		inputCostPerMTok  = 3.0
		outputCostPerMTok = 15.0
	)
	var totalUSD float64
	for _, d := range report.Data {
		totalUSD += float64(d.InputTokens+d.CacheTokens) / 1_000_000 * inputCostPerMTok
		totalUSD += float64(d.OutputTokens) / 1_000_000 * outputCostPerMTok
	}
	return totalUSD, nil
}
