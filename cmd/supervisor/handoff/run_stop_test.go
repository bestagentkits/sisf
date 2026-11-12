package handoff_test

import (
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
)

// A verified release blocked because it could no longer be judged tells the
// Owner that it was not published, that its pull request stays open and
// unmerged, and how a retry returns the issue to Ready; whether anything was
// restored follows the recovery report.
func TestAnUnjudgedVerifiedReleaseSaysItsPullRequestStaysOpen(t *testing.T) {
	t.Parallel()
	for _, restored := range []bool{false, true} {
		stop := handoff.RunStop{IssueID: core.IssueRef{Owner: "acme", Repo: "app", Number: 7}, RunID: "run-7", Cause: statemachine.BlockReleaseUnjudged, Verified: true, Release: "sha256:aa"}
		want := "nothing was restored"
		if restored {
			stop.RestoredTo, want = "sha256:bb", "the last known good release serves again"
		}
		summary, wayOut := stop.Summary(), stop.WayOut()
		for _, phrase := range []string{"was verified", "not published", "stays open and unmerged", want} {
			if !strings.Contains(summary, phrase) {
				t.Errorf("restored %v: the summary does not say %q: %q", restored, phrase, summary)
			}
		}
		for _, phrase := range []string{"stays open and unmerged", "merge it by hand", "factoryd issue retry --issue " + core.IssueRef{Owner: "acme", Repo: "app", Number: 7}.String(), readyAgain} {
			if !strings.Contains(wayOut, phrase) {
				t.Errorf("restored %v: the way out does not say %q: %q", restored, phrase, wayOut)
			}
		}
	}
	// A release blocked while it served, before it was verified, keeps its
	// own words: there is no pull request of a verified release to speak of.
	served := handoff.RunStop{IssueID: core.IssueRef{Owner: "acme", Repo: "app", Number: 8}, RunID: "run-8", Cause: statemachine.BlockReleaseUnjudged, RestoredTo: "sha256:bb"}
	if got := served.Summary() + served.WayOut(); strings.Contains(got, "unmerged") || !strings.Contains(got, "serves again") {
		t.Errorf("the block of an unverified release reads %q", got)
	}
}

// readyAgain is how a way out promises the issue back in Ready: once the
// Owner put another policy in force, the retry asks for an approval of the
// scope first when that policy does not permit it by itself.
const readyAgain = "the issue returns to Ready, after an approval of its scope when the policy changed since and asks for one,"

// Every way out that promises a retry returns the issue to Ready says that
// a policy changed since the block may ask for an approval of its scope
// first; a superseded policy says how its scope is judged in its own words.
func TestAWayOutThatPromisesReadyAgainNamesTheApprovalAPolicyChangeMayNeed(t *testing.T) {
	t.Parallel()
	issue := core.IssueRef{Owner: "acme", Repo: "app", Number: 7}
	stops := []handoff.RunStop{
		{Cause: statemachine.BlockRunExhausted},
		{Cause: statemachine.BlockRolledBack},
		{Cause: statemachine.BlockReleaseInterrupted},
		{Cause: statemachine.BlockReleaseUnjudged},
		{Cause: statemachine.BlockReleaseUnjudged, Verified: true},
		{Cause: statemachine.BlockReleaseRefused, ReleaseCode: core.CodeApprovalRequired},
		{Cause: statemachine.BlockReleaseRefused, ReleaseCode: core.CodeExpired},
	}
	for _, stop := range stops {
		stop.IssueID, stop.RunID = issue, "run-7"
		if w := stop.WayOut(); !strings.Contains(w, readyAgain) {
			t.Errorf("the way out of %s (%s) does not say %q: %q", stop.Cause, stop.ReleaseCode, readyAgain, w)
		}
	}
	superseded := handoff.RunStop{IssueID: issue, RunID: "run-7", Cause: statemachine.BlockPolicySuperseded}
	if w := superseded.WayOut(); !strings.Contains(w, "judged under the policy in force") {
		t.Errorf("the way out of a superseded policy reads %q", w)
	}
}
