package report_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/bestagentkits/sisf/internal/intake/report"
	"github.com/bestagentkits/sisf/pkg/core"
)

// backlogStore refuses every report as one too many of its client's.
type backlogStore struct{ details map[string]string }

func (s backlogStore) Save(context.Context, report.Report) (report.Report, bool, error) {
	err := core.NewError(core.CodeRateLimited, "too many waiting")
	err.Details = s.details
	return report.Report{}, false, err
}

// A store that refuses a report because its client has too many waiting is
// answered 429 with the time to retry the store names, or a default one.
func TestAReportRefusedAsTooManyWaitingIsAnsweredTooManyRequests(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		details map[string]string
		want    string
	}{
		{map[string]string{"retry_after_seconds": "1800"}, "1800"},
		{nil, "300"},
	} {
		h, _, _ := newHandler(t, func(c *report.Config) { c.Store = backlogStore{details: tc.details} })
		rec := post(h, "198.51.100.1", jsonType, `{"title":"Login fails","body":"b"}`)
		if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != tc.want {
			t.Fatalf("details %v: %d, Retry-After %q; want 429 and %s", tc.details, rec.Code, rec.Header().Get("Retry-After"), tc.want)
		}
	}
}
