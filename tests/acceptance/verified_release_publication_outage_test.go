//go:build unix

package acceptance

import (
	"net/http"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/pkg/core"
)

// A publication that GitHub refuses after the release was verified leaves
// the issue Verified, shows on its pull request what finishing waits on,
// and is finished by a later pass of the supervisor: published, merged
// once and closed.
func TestAVerifiedReleaseWhosePublicationFailedIsFinishedByALaterPass(t *testing.T) {
	t.Parallel()
	f, number := greetingIssue(t)
	for range 4 { // every try of the client's read of the changelog the publication extends
		f.GitData.FailOn(http.MethodGet, "CHANGELOG", http.StatusBadGateway, "Server Error")
	}
	for range 6 {
		if f.Cycle(1); f.Record(number).State == core.StateVerified {
			break
		}
	}
	pr := candidatePR(t, f, number)
	if r := f.Record(number); r.State != core.StateVerified || pr.State != handoff.LandingPending || !strings.Contains(pr.Waiting, "publishing") {
		t.Fatalf("after the outage the issue is %s with its pull request %+v; want Verified, pending and waiting on the publication", r.State, pr)
	}
	if got := f.Inspect(number).PullRequest; got == nil || got.State != string(handoff.LandingPending) {
		t.Fatalf("runs.inspect shows the pull request as %+v, want pending", got)
	}
	f.Cycle(2)
	wantSurfaces(t, f, number, core.StateClosed)
	if pr := candidatePR(t, f, number); pr.State != handoff.LandingMerged || pr.Waiting != "" || f.GitData.Merges() != 1 || !mainHolds(f) {
		t.Fatalf("after the outage ended: %+v, %d merges", pr, f.GitData.Merges())
	}
}
