// Package acceptance checks the factory's promises end to end in one
// process, through the same wiring the factoryd binary runs.
package acceptance

import (
	"testing"

	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
)

const completeBody = "The health check fails when the database is slow.\n\n### Definition of Done\n- /healthz answers 200 within one second while the database is slow\n"

// assertOneRun checks that issue number has exactly one run, that the agent
// was started once, and that the issue is being developed.
func assertOneRun(t *testing.T, f *harness.Factory, number int) {
	t.Helper()
	if runs := f.Runs(number); len(runs) != 1 {
		t.Fatalf("issue %d has %d runs, want 1: %+v", number, len(runs), runs)
	}
	if started := f.Agent.Started(f.Issue(number)); len(started) != 1 {
		t.Fatalf("the agent was started %d times for issue %d, want 1", len(started), number)
	}
	if state := f.Record(number).State; state != core.StateDeveloping {
		t.Fatalf("issue %d is %s, want %s", number, state, core.StateDeveloping)
	}
}

// Every delivery about one issue arrives late, newest first, and twice; the
// whole batch is then redelivered. The issue is scoped and claimed once.
func TestDuplicateAndReorderedDeliveriesForOneIssueMakeOneRun(t *testing.T) {
	t.Parallel()
	f := harness.New(t, harness.Options{})
	f.GitHub.Hold()
	number := f.OpenIssue("maintainer", "MEMBER", "Health check fails", "The health check fails.")
	if err := f.GitHub.AddLabels(f.Repo, number, f.GitHub.Person("maintainer", "MEMBER"), github.LabelKindBug, github.LabelReady); err != nil {
		t.Fatal(err)
	}
	f.Comment(number, "maintainer", "MEMBER", completeBody)
	held := len(f.GitHub.Deliveries())
	if held < 3 {
		t.Fatalf("only %d deliveries were produced", held)
	}
	f.GitHub.DuplicateNext(held)
	f.GitHub.Release(true)
	f.Settle(4)
	assertOneRun(t, f, number)

	for _, d := range f.GitHub.Deliveries() {
		if err := f.GitHub.Redeliver(d.ID); err != nil {
			t.Fatal(err)
		}
	}
	f.Settle(4)
	assertOneRun(t, f, number)
	if got := f.Record(number).RunID; got != f.Runs(number)[0].RunID {
		t.Fatalf("the issue points at run %s, not its only run", got)
	}
}

// Deliveries that arrive after the issue was claimed, including a late
// "labeled sisf:ready", start nothing more.
func TestDeliveriesAfterTheClaimStartNoSecondRun(t *testing.T) {
	t.Parallel()
	f := harness.New(t, harness.Options{})
	f.GitHub.Hold()
	number := f.OpenIssue("maintainer", "MEMBER", "Health check fails", completeBody, github.LabelReady, github.LabelKindBug)
	f.GitHub.Release(false)
	f.Settle(2)
	assertOneRun(t, f, number)

	f.GitHub.DuplicateNext(4)
	maintainer := f.GitHub.Person("maintainer", "MEMBER")
	if err := f.GitHub.RemoveLabel(f.Repo, number, maintainer, github.LabelReady); err != nil {
		t.Fatal(err)
	}
	if err := f.GitHub.AddLabels(f.Repo, number, maintainer, github.LabelReady); err != nil {
		t.Fatal(err)
	}
	for _, d := range f.GitHub.Deliveries() {
		if err := f.GitHub.Redeliver(d.ID); err != nil {
			t.Fatal(err)
		}
	}
	f.Settle(4)
	assertOneRun(t, f, number)
}
