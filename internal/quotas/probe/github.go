package probe

import (
	"context"
	"encoding/json"
	"net/http"
)

const githubVendor = "github"

const githubBaseURL = "https://api.github.com"

// GitHubProber probes GitHub Actions billing quota.
type GitHubProber struct {
	pat     string
	client  *http.Client
	baseURL string // overridable in tests
}

// NewGitHub returns a GitHubProber. Returns nil if pat is empty.
func NewGitHub(pat string) *GitHubProber {
	if pat == "" {
		return nil
	}
	return &GitHubProber{
		pat:     pat,
		client:  &http.Client{Timeout: ProbeTimeout},
		baseURL: githubBaseURL,
	}
}

func (g *GitHubProber) Vendor() string { return githubVendor }

type githubActionsBilling struct {
	TotalMinutesUsed     float64 `json:"total_minutes_used"`
	TotalPaidMinutesUsed float64 `json:"total_paid_minutes_used"`
	IncludedMinutes      float64 `json:"included_minutes"`
}

func (g *GitHubProber) Probe(ctx context.Context) ([]Reading, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		g.baseURL+"/user/settings/billing/actions", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "token "+g.pat)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := g.client.Do(req)
	if err != nil {
		return nil, &timeoutOrNetErr{err}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &httpStatusErr{status: resp.StatusCode}
	}

	var billing githubActionsBilling
	if err := json.NewDecoder(resp.Body).Decode(&billing); err != nil {
		return nil, &parseErr{err}
	}

	if billing.IncludedMinutes <= 0 {
		// Free tier or API doesn't report included_minutes — report 100% as a safe default.
		return []Reading{{Product: "actions_minutes", Remaining: 100}}, nil
	}

	used := billing.TotalMinutesUsed
	remaining := billing.IncludedMinutes - used
	pct := remaining / billing.IncludedMinutes * 100
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}

	return []Reading{{Product: "actions_minutes", Remaining: pct}}, nil
}
