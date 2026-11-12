package decision_test

import (
	"context"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/decision"
	"github.com/bestagentkits/sisf/internal/decision/systemone"
	"github.com/bestagentkits/sisf/internal/decision/systemone/systemonetest"
	"github.com/bestagentkits/sisf/pkg/core"
	pkgdecision "github.com/bestagentkits/sisf/pkg/decision"
)

const systemOneVersion = "1"

var jevRoute = core.ModelRoute{Provider: "jev", Model: "jev-1"}

// neverClock never times a request out; timeouts are tested in the adapter.
type neverClock struct{}

func (neverClock) Now() time.Time                       { return time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC) }
func (neverClock) After(time.Duration) <-chan time.Time { return nil }

// typedQuestions parses the repository's DECISIONS.md as if read at the
// pinned decision commit.
func typedQuestions(t *testing.T) decision.TypedQuestions {
	t.Helper()
	pinned := policyRefs().DecisionSHA
	src := pinnedFiles{revision: pinned, files: map[string]string{"DECISIONS.md": decisionsFile(t)}}
	tq, err := decision.LoadTypedQuestions(context.Background(), src, pinned)
	if err != nil {
		t.Fatal(err)
	}
	return tq
}

// judgmentFor answers every typed question of req consistently.
func judgmentFor(req pkgdecision.Request, v pkgdecision.Verdict, k pkgdecision.Kind, p pkgdecision.Priority) pkgdecision.Judgment {
	j := pkgdecision.Judgment{Verdict: v, Kind: k, Priority: p, Model: jevRoute, SchemaVersion: req.SchemaVersion}
	for _, q := range req.Questions {
		value := map[string]string{decision.SystemOneVerdict: string(v), decision.SystemOneKind: string(k),
			decision.SystemOnePriority: string(p)}[q.ID]
		if value == "" {
			value = q.Options[0]
		}
		j.Answers = append(j.Answers, pkgdecision.Answer{QuestionID: q.ID, Value: value})
	}
	return j
}

// newSystemOneFixture is a layer that consults a fake Jev over HTTP.
func newSystemOneFixture(t *testing.T) (*fixture, *systemonetest.Server) {
	t.Helper()
	fake := systemonetest.New(systemOneVersion, "")
	t.Cleanup(fake.Close)
	client, err := systemone.New(systemone.Config{Backend: systemone.BackendJev, Endpoint: fake.URL, Version: systemOneVersion,
		Timeout: 5 * time.Second, Model: jevRoute}, systemone.Deps{Clock: neverClock{}})
	if err != nil {
		t.Fatal(err)
	}
	tq := typedQuestions(t)
	f := newFixtureWith(t, func(c *decision.Config) { c.SystemOne, c.Questions = client, tq })
	return f, fake
}

// regress gives endpoint a baseline and then one bad window, so the next
// bad window is a recurring regression the rules propose.
func (f *fixture) regress(t *testing.T, endpoint string) {
	t.Helper()
	f.baseline(t, endpoint)
	if o := f.window(t, endpoint, 1000, 100); o.Proposal != nil {
		t.Fatalf("one bad window is not yet a proposal: %+v", o)
	}
}

// runAllowingErrors runs the layer once and returns the outcome for
// endpoint together with the run's error.
func (f *fixture) runAllowingErrors(t *testing.T, endpoint string) (decision.Outcome, error) {
	t.Helper()
	f.clock.After(time.Minute)
	outs, err := f.layer.Run(context.Background())
	for _, o := range outs {
		if o.Endpoint == endpoint {
			return o, err
		}
	}
	t.Fatalf("no outcome for %q (%v)", endpoint, err)
	return decision.Outcome{}, err
}
