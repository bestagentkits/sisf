package telemetryactions_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/actions/telemetryactions"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/telemetry"
)

var at = time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)

func seeded(t *testing.T) *telemetry.MemoryStore {
	t.Helper()
	store := telemetry.NewMemoryStore(100)
	issue := core.IssueRef{Owner: "acme", Repo: "app", Number: 7}
	events := []telemetry.Event{
		{Kind: telemetry.KindLog, Name: "checkout.failed", At: at, Body: "card declined", Severity: "error",
			Attributes: telemetry.Attributes{ReleaseDigest: "sha256:r1", RunID: "run-1", IssueRef: issue},
			Fields:     map[string]string{"feature": "checkout"}},
		{Kind: telemetry.KindLog, Name: "search.slow", At: at.Add(time.Second), Body: "took 3s",
			Attributes: telemetry.Attributes{ReleaseDigest: "sha256:r1"}, Fields: map[string]string{"feature": "search"}},
		{Kind: telemetry.KindEvent, Name: "deploy.done", At: at.Add(2 * time.Second),
			Attributes: telemetry.Attributes{ReleaseDigest: "sha256:r2"}},
	}
	for _, e := range events {
		if err := store.Append(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

func search(t *testing.T, reader telemetry.Reader, input string) (telemetryactions.Result, error) {
	t.Helper()
	set, err := telemetryactions.Actions(reader)
	if err != nil {
		t.Fatal(err)
	}
	res, err := set[0].Handle(context.Background(), core.ActionRequest{Input: json.RawMessage(input)})
	if err != nil {
		return telemetryactions.Result{}, err
	}
	var out telemetryactions.Result
	if err := json.Unmarshal(res.Output, &out); err != nil {
		t.Fatal(err)
	}
	return out, nil
}

func names(r telemetryactions.Result) []string {
	out := []string{}
	for _, h := range r.Events {
		out = append(out, h.Name)
	}
	return out
}

func TestTheSearchNeedsAReader(t *testing.T) {
	t.Parallel()
	if _, err := telemetryactions.Actions(nil); core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("got %v", err)
	}
}

func TestTheSearchIsAReadThatDeclaresItsScope(t *testing.T) {
	t.Parallel()
	s := telemetryactions.Spec()
	if s.Sensitivity != core.SensitivityRead || len(s.Scopes) == 0 {
		t.Fatalf("spec %+v", s)
	}
}

func TestTheSearchFilters(t *testing.T) {
	t.Parallel()
	store := seeded(t)
	cases := map[string][]string{
		`{}`:                                {"checkout.failed", "search.slow", "deploy.done"},
		`{"release":"sha256:r1"}`:           {"checkout.failed", "search.slow"},
		`{"issue":"acme/app#7"}`:            {"checkout.failed"},
		`{"run":"run-1"}`:                   {"checkout.failed"},
		`{"feature":"search"}`:              {"search.slow"},
		`{"query":"DECLINED"}`:              {"checkout.failed"},
		`{"kind":"event"}`:                  {"deploy.done"},
		`{"release":"sha256:r1","limit":1}`: {"checkout.failed"},
	}
	for input, want := range cases {
		got, err := search(t, store, input)
		if err != nil {
			t.Fatalf("%s: %v", input, err)
		}
		if g := names(got); len(g) != len(want) || (len(g) > 0 && g[0] != want[0]) {
			t.Errorf("%s: got %v, want %v", input, g, want)
		}
	}
}

func TestALimitedSearchSaysItIsTruncated(t *testing.T) {
	t.Parallel()
	got, err := search(t, seeded(t), `{"limit":1}`)
	if err != nil || !got.Truncated || len(got.Events) != 1 {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestTheHitCarriesTheAttributes(t *testing.T) {
	t.Parallel()
	got, err := search(t, seeded(t), `{"run":"run-1"}`)
	if err != nil || len(got.Events) != 1 {
		t.Fatalf("got %+v, %v", got, err)
	}
	h := got.Events[0]
	if h.Issue != "acme/app#7" || h.Release != "sha256:r1" || h.Feature != "checkout" || h.At != "2026-11-11T09:00:00Z" {
		t.Fatalf("hit %+v", h)
	}
}

func TestABadFilterIsRefused(t *testing.T) {
	t.Parallel()
	for _, input := range []string{`{"issue":"not-an-issue"}`, `{"kind":"trace"}`, `{"limit":501}`, `{"unknown":1}`} {
		if _, err := search(t, seeded(t), input); core.CodeOf(err) != core.CodeInvalidArgument {
			t.Errorf("%s: got %v", input, err)
		}
	}
}

type brokenReader struct{}

func (brokenReader) Query(context.Context, telemetry.Query) ([]telemetry.Event, error) {
	return nil, errors.New("collector down")
}

func TestAnUnreadableStoreIsUnavailable(t *testing.T) {
	t.Parallel()
	if _, err := search(t, brokenReader{}, `{}`); core.CodeOf(err) != core.CodeUnavailable {
		t.Fatalf("got %v", err)
	}
}
