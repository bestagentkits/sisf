package providercost

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// AnthropicURL is the Claude API.
const AnthropicURL = "https://api.anthropic.com"

// Anthropic reads the cost an Anthropic workspace was charged, from the
// Usage and Cost Analytics API (GET /v1/organizations/analytics/cost),
// which needs an Admin API key. The figure is limited to one workspace that
// the factory alone uses, so spending of other work there is not taken for
// the factory's.
type Anthropic struct {
	// BaseURL is the API's address; empty means AnthropicURL.
	BaseURL string
	// AdminKey is an Admin API key of the organization.
	AdminKey core.SecretValue
	// WorkspaceID is the workspace the factory's runs are charged to.
	WorkspaceID string
	HTTP        *http.Client
}

// anthropicCostReport is the part of the answer that is read. Amounts are
// in cents, as decimal strings with fractions.
type anthropicCostReport struct {
	Data []struct {
		Cost struct {
			Amount   string `json:"amount"`
			Currency string `json:"currency"`
		} `json:"cost"`
	} `json:"data"`
	DataRefreshedAt *time.Time `json:"data_refreshed_at"`
	HasMore         bool       `json:"has_more"`
	NextPage        *string    `json:"next_page"`
}

// microsPerCent converts the API's cents to micros of a dollar.
const microsPerCent = 10_000

// Spent returns the workspace's cost from since to now, after discounts and
// before credits, and the time the provider's data was refreshed. The API
// covers at most 31 days from since, which holds a month.
func (a Anthropic) Spent(ctx context.Context, since time.Time) (core.Micros, time.Time, error) {
	if a.AdminKey.IsZero() || a.WorkspaceID == "" {
		return 0, time.Time{}, core.NewError(core.CodeInvalidArgument, "reading Anthropic's cost needs an admin key and the factory's workspace")
	}
	for attempt := 0; ; attempt++ {
		spent, covered, err := a.read(ctx, since)
		if err == errExpiredCursor && attempt == 0 {
			continue
		}
		return spent, covered, err
	}
}

func (a Anthropic) read(ctx context.Context, since time.Time) (core.Micros, time.Time, error) {
	base := strings.TrimSuffix(a.BaseURL, "/")
	if base == "" {
		base = AnthropicURL
	}
	var (
		spent   core.Micros
		covered time.Time
		page    string
	)
	for range maxPages {
		q := url.Values{}
		q.Set("starting_at", since.UTC().Format(time.RFC3339))
		q.Set("workspace_ids[]", a.WorkspaceID)
		q.Set("limit", "1000")
		if page != "" {
			q.Set("page", page)
		}
		req, err := http.NewRequest(http.MethodGet, base+"/v1/organizations/analytics/cost?"+q.Encode(), nil)
		if err != nil {
			return 0, time.Time{}, core.NewError(core.CodeInvalidArgument, "the Anthropic API address is not valid").WithCause(err)
		}
		req.Header.Set("x-api-key", string(a.AdminKey.Reveal()))
		req.Header.Set("anthropic-version", "2023-06-01")
		req.Header.Set("anthropic-beta", "analytics-usage-cost-2026-08-24")
		var report anthropicCostReport
		if err := getJSON(ctx, a.HTTP, req, "Anthropic", &report); err != nil {
			return 0, time.Time{}, err
		}
		for _, row := range report.Data {
			if row.Cost.Currency != "USD" {
				return 0, time.Time{}, core.NewError(core.CodeNotSupported, "Anthropic reported a cost in "+row.Cost.Currency+", not USD")
			}
			m, err := micros(row.Cost.Amount, microsPerCent)
			if err != nil {
				return 0, time.Time{}, err
			}
			spent = add(spent, m)
		}
		if report.DataRefreshedAt != nil && (covered.IsZero() || report.DataRefreshedAt.Before(covered)) {
			covered = report.DataRefreshedAt.UTC()
		}
		if !report.HasMore || report.NextPage == nil || *report.NextPage == "" {
			return spent, covered, nil
		}
		page = *report.NextPage
	}
	return 0, time.Time{}, core.NewError(core.CodeUnavailable, "Anthropic's cost report has more pages than expected")
}
