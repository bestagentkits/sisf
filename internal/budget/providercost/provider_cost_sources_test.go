package providercost

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

var since = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

const testKey = "admin-key-for-tests"

func TestAmountsAreReadExactlyAndRoundedUp(t *testing.T) {
	t.Parallel()
	cases := []struct {
		amount  string
		perUnit int64
		want    core.Micros
	}{
		{"123.45", microsPerCent, 1_234_500},
		{"0.00001", microsPerCent, 1},
		{"0.0000001", microsPerDollar, 1},
		{"0.1", microsPerDollar, 100_000},
		{"1e-05", microsPerDollar, 10},
		{"2.5E2", microsPerDollar, 250_000_000},
		{"-3.2", microsPerDollar, 0},
		{"0", microsPerCent, 0},
		{"999999999999999999999999999999", microsPerDollar, 1<<63 - 1},
	}
	for _, c := range cases {
		got, err := micros(c.amount, c.perUnit)
		if err != nil || got != c.want {
			t.Errorf("micros(%q) = %d, %v; want %d", c.amount, got, err, c.want)
		}
	}
	for _, bad := range []string{"", "1,5", "0x10", "1e999", "NaN", "Infinity", " 1", "1.", ".5", "1/2"} {
		if _, err := micros(bad, microsPerDollar); !core.HasCode(err, core.CodeInvalidArgument) {
			t.Errorf("micros(%q): %v, want INVALID_ARGUMENT", bad, err)
		}
	}
}

func anthropicServer(t *testing.T, handle func(w http.ResponseWriter, r *http.Request)) Anthropic {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != testKey || r.Header.Get("anthropic-beta") != "analytics-usage-cost-2026-08-24" ||
			r.URL.Path != "/v1/organizations/analytics/cost" || r.URL.Query().Get("workspace_ids[]") != "wrkspc_factory" ||
			r.URL.Query().Get("starting_at") != "2026-11-01T00:00:00Z" {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		handle(w, r)
	}))
	t.Cleanup(srv.Close)
	return Anthropic{BaseURL: srv.URL, AdminKey: core.NewSecretValue([]byte(testKey)), WorkspaceID: "wrkspc_factory", HTTP: srv.Client()}
}

func TestAnthropicsCostIsSummedAcrossPages(t *testing.T) {
	t.Parallel()
	var expired atomic.Bool
	a := anthropicServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("page") {
		case "":
			_, _ = fmt.Fprint(w, `{"data":[{"cost":{"amount":"41280.5","currency":"USD","list_amount":"0"}}],"data_refreshed_at":"2026-11-09T04:00:00Z","has_more":true,"next_page":"p2"}`)
		case "p2":
			if expired.CompareAndSwap(false, true) {
				w.WriteHeader(http.StatusGone)
				return
			}
			_, _ = fmt.Fprint(w, `{"data":[{"cost":{"amount":"0.00001","currency":"USD"}}],"data_refreshed_at":"2026-11-09T03:00:00Z","has_more":false,"next_page":null}`)
		}
	})
	spent, covered, err := a.Spent(context.Background(), since)
	if err != nil {
		t.Fatal(err)
	}
	if want := core.Micros(412_805_000 + 1); spent != want {
		t.Errorf("spent %d, want %d", spent, want)
	}
	if !covered.Equal(time.Date(2026, 11, 9, 3, 0, 0, 0, time.UTC)) {
		t.Errorf("covered until %v, want the earliest refresh of the pages", covered)
	}
}

func TestACostReportTheFactoryCannotReadIsAnError(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		status int
		body   string
		code   core.ErrorCode
	}{
		"another currency": {200, `{"data":[{"cost":{"amount":"1","currency":"EUR"}}],"has_more":false}`, core.CodeNotSupported},
		"a loose amount":   {200, `{"data":[{"cost":{"amount":"1e999","currency":"USD"}}],"has_more":false}`, core.CodeInvalidArgument},
		"not JSON":         {200, `<html>`, core.CodeUnavailable},
		"a refused key":    {401, `{}`, core.CodeUnauthenticated},
		"no permission":    {403, `{}`, core.CodePermissionDenied},
		"an outage":        {503, `{}`, core.CodeUnavailable},
		"always expired":   {410, `{}`, core.CodeConflict},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a := anthropicServer(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(c.status)
				_, _ = fmt.Fprint(w, c.body)
			})
			if _, _, err := a.Spent(context.Background(), since); !core.HasCode(err, c.code) {
				t.Fatalf("%v, want %s", err, c.code)
			}
		})
	}
}

// The admin key never follows a redirect to another server.
func TestTheAdminKeyIsNotSentWhereARedirectPoints(t *testing.T) {
	t.Parallel()
	var leaked atomic.Bool
	elsewhere := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "" || r.Header.Get("Authorization") != "" {
			leaked.Store(true)
		}
	}))
	t.Cleanup(elsewhere.Close)
	a := anthropicServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+"/v1/organizations/analytics/cost", http.StatusFound)
	})
	if _, _, err := a.Spent(context.Background(), since); !core.HasCode(err, core.CodeUnavailable) {
		t.Errorf("a redirect: %v, want UNAVAILABLE", err)
	}
	if leaked.Load() {
		t.Fatal("the admin key was sent where the redirect pointed")
	}
}

func TestAReadNeedsAKeyAndTheFactorysOwnScope(t *testing.T) {
	t.Parallel()
	for name, source := range map[string]interface {
		Spent(context.Context, time.Time) (core.Micros, time.Time, error)
	}{
		"anthropic without a workspace": Anthropic{AdminKey: core.NewSecretValue([]byte(testKey))},
		"anthropic without a key":       Anthropic{WorkspaceID: "wrkspc_factory"},
		"openai without a project":      OpenAI{AdminKey: core.NewSecretValue([]byte(testKey))},
		"openai without a key":          OpenAI{ProjectID: "proj_factory"},
	} {
		if _, _, err := source.Spent(context.Background(), since); !core.HasCode(err, core.CodeInvalidArgument) {
			t.Errorf("%s: %v, want INVALID_ARGUMENT", name, err)
		}
	}
}

func TestOpenAIsCostIsSummedAcrossBucketsAndPages(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.Header.Get("Authorization") != "Bearer "+testKey || r.URL.Path != "/v1/organization/costs" ||
			q.Get("project_ids") != "proj_factory" || q.Get("start_time") != fmt.Sprint(since.Unix()) {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		switch q.Get("page") {
		case "":
			_, _ = fmt.Fprint(w, `{"object":"page","data":[{"object":"bucket","start_time":1,"end_time":2,"results":[
				{"object":"organization.costs.result","amount":{"value":0.06,"currency":"usd"}},
				{"object":"organization.costs.result","amount":{"value":1e-7,"currency":"usd"}}]}],"has_more":true,"next_page":"n"}`)
		default:
			_, _ = fmt.Fprint(w, `{"object":"page","data":[{"object":"bucket","results":[{"amount":{"value":12.345678,"currency":"usd"}},
				{"amount":{"value":-1.5,"currency":"usd"}}]}],"has_more":false,"next_page":null}`)
		}
	}))
	t.Cleanup(srv.Close)
	o := OpenAI{BaseURL: srv.URL, AdminKey: core.NewSecretValue([]byte(testKey)), ProjectID: "proj_factory", HTTP: srv.Client()}
	spent, covered, err := o.Spent(context.Background(), since)
	if err != nil {
		t.Fatal(err)
	}
	if want := core.Micros(60_000 + 1 + 12_345_678); spent != want || !covered.IsZero() {
		t.Errorf("spent %d covered %v, want %d and no coverage", spent, covered, want)
	}
}
