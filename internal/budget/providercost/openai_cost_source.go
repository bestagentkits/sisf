package providercost

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// OpenAIURL is the OpenAI API.
const OpenAIURL = "https://api.openai.com"

// OpenAI reads the cost an OpenAI project was charged, from the
// organization Costs API (GET /v1/organization/costs), which needs an admin
// key. The figure is limited to one project that the factory alone uses.
type OpenAI struct {
	// BaseURL is the API's address; empty means OpenAIURL.
	BaseURL string
	// AdminKey is an admin key of the organization.
	AdminKey core.SecretValue
	// ProjectID is the project the factory's runs are charged to.
	ProjectID string
	HTTP      *http.Client
}

// openAICosts is the part of the answer that is read. A value is a JSON
// number of dollars, kept as written.
type openAICosts struct {
	Data []struct {
		Results []struct {
			Amount struct {
				Value    json.Number `json:"value"`
				Currency string      `json:"currency"`
			} `json:"amount"`
		} `json:"results"`
	} `json:"data"`
	HasMore  bool    `json:"has_more"`
	NextPage *string `json:"next_page"`
}

// microsPerDollar converts the API's dollars to micros.
const microsPerDollar = 1_000_000

// Spent returns the project's cost in daily buckets from since to now. The
// API does not say how far its data reaches, so the time is zero.
func (o OpenAI) Spent(ctx context.Context, since time.Time) (core.Micros, time.Time, error) {
	if o.AdminKey.IsZero() || o.ProjectID == "" {
		return 0, time.Time{}, core.NewError(core.CodeInvalidArgument, "reading OpenAI's cost needs an admin key and the factory's project")
	}
	base := strings.TrimSuffix(o.BaseURL, "/")
	if base == "" {
		base = OpenAIURL
	}
	var (
		spent core.Micros
		page  string
	)
	for range maxPages {
		q := url.Values{}
		q.Set("start_time", strconv.FormatInt(since.Unix(), 10))
		q.Set("bucket_width", "1d")
		q.Set("project_ids", o.ProjectID)
		q.Set("limit", "180")
		if page != "" {
			q.Set("page", page)
		}
		req, err := http.NewRequest(http.MethodGet, base+"/v1/organization/costs?"+q.Encode(), nil)
		if err != nil {
			return 0, time.Time{}, core.NewError(core.CodeInvalidArgument, "the OpenAI API address is not valid").WithCause(err)
		}
		req.Header.Set("Authorization", "Bearer "+string(o.AdminKey.Reveal()))
		var report openAICosts
		if err := getJSON(ctx, o.HTTP, req, "OpenAI", &report); err != nil {
			return 0, time.Time{}, err
		}
		for _, bucket := range report.Data {
			for _, r := range bucket.Results {
				if !strings.EqualFold(r.Amount.Currency, "usd") {
					return 0, time.Time{}, core.NewError(core.CodeNotSupported, "OpenAI reported a cost in "+r.Amount.Currency+", not USD")
				}
				m, err := micros(r.Amount.Value.String(), microsPerDollar)
				if err != nil {
					return 0, time.Time{}, err
				}
				spent = add(spent, m)
			}
		}
		if !report.HasMore || report.NextPage == nil || *report.NextPage == "" {
			return spent, time.Time{}, nil
		}
		page = *report.NextPage
	}
	return 0, time.Time{}, core.NewError(core.CodeUnavailable, "OpenAI's cost report has more pages than expected")
}
