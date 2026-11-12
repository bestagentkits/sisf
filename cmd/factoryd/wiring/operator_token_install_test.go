package wiring_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/cmd/factoryd/wiring"
	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
)

// A development install on the operator's gh login names no GitHub App
// installation. Its control zone still starts, takes no webhook, learns of
// a ready issue only through reconciliation with GitHub, runs it once
// however often it is polled again, and lets the signed-in Owner use the
// surfaces, because GitHub confirms the token may write to the repository.
func TestAnInstallOnTheOperatorsLoginTakesIssuesOnlyByReconciliationAndRunsEachOnce(t *testing.T) {
	t.Parallel()
	f := harness.New(t, harness.Options{OperatorToken: true})

	resp, err := testClient.Post(f.Base()+"/webhooks/github", "application/json", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("the webhook route answered %d, want 404 on an install that takes no webhooks", resp.StatusCode)
	}

	number := f.OpenIssue("maintainer", "MEMBER", "Health check fails", bugBody, github.LabelReady, github.LabelKindBug)
	// Reconciliation stores the change in the inbox at the end of a pass;
	// the next pass hands it to intake.
	for range 2 {
		f.Pass()
	}
	if got := f.Record(number).State; got != core.StateReady {
		t.Fatalf("a reconciled ready issue is %s, want %s", got, core.StateReady)
	}
	// The issue keeps changing, by the factory's own edits and a person's
	// comment, and is polled again and again: it is still run exactly once.
	for i := range 4 {
		if i == 1 {
			f.Comment(number, "maintainer", "MEMBER", "Any news?")
		}
		f.Clock.Advance(wiring.DefaultReconcileInterval)
		f.Pass()
		f.Tick()
	}
	if runs := f.Runs(number); len(runs) != 1 {
		t.Fatalf("%d runs for an issue seen by repeated polls, want exactly 1", len(runs))
	}

	var scopes []string
	for _, spec := range f.Control.Actions.Specs() {
		if spec.Name == "budget.get" {
			scopes = spec.Scopes
		}
	}
	req, err := http.NewRequest(http.MethodPost, f.Base()+"/api/v1/actions/budget.get", strings.NewReader(`{"input":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header = http.Header{"Content-Type": {"application/json"}, "Authorization": {"Bearer " + f.SignInOwner().PAT(scopes...)}}
	resp, err = testClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the Owner's budget.get answered %d: %s", resp.StatusCode, raw)
	}
}
