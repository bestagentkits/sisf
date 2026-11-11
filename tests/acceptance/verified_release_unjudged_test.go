//go:build unix

package acceptance

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
	"github.com/bestagentkits/sisf/pkg/deploy"
)

// A verified release whose LKG could not be written, and which the unit no
// longer serves when it can be, is not recorded as the LKG of a unit it does
// not serve, nor left waiting in Verified: the supervisor blocks the issue
// as release_unjudged. Its summary says the pull request stays open and
// unmerged, nothing is merged or published, and the Owner's retry returns
// the issue to Ready with the pull request still open.
func TestAVerifiedReleaseThatNoLongerServesBlocksItsIssueUntilTheOwnerRetriesIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f, number := greetingIssue(t)
	if err := os.Chmod(f.Trust.Dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(f.Trust.Dir, 0o700) })
	for range 6 {
		if f.Cycle(1); f.Record(number).State == core.StateVerified {
			break
		}
	}
	if got := f.Record(number).State; got != core.StateVerified {
		t.Fatalf("with the trust volume read-only the issue is %s, want it held in Verified", got)
	}
	installed := f.Trust.Installed["app"]
	if err := f.Deploy.Rollback(ctx, deploy.Revision{Unit: "app", Digest: installed}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(f.Trust.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	f.Cycle(2)

	wantSurfaces(t, f, number, core.StateBlocked)
	record := f.Record(number)
	stop := runStopOf(t, f, record.RunID)
	if stop.Cause != statemachine.BlockReleaseUnjudged || !stop.Verified {
		t.Fatalf("the issue is blocked with %+v, want a verified release that could not be judged", stop)
	}
	if !strings.Contains(stop.Summary(), "stays open and unmerged") || !strings.Contains(stop.WayOut(), "factoryd issue retry --issue "+f.Issue(number).String()) {
		t.Fatalf("the block does not tell the Owner the pull request stays open and how to retry: %q / %q", stop.Summary(), stop.WayOut())
	}
	if lkg, found, err := f.Trust.LKG.Get(ctx, "app"); err != nil || !found || lkg.Digest != installed {
		t.Fatalf("the LKG is %+v (%v), want the installed release that serves", lkg, err)
	}
	if pr := candidatePR(t, f, number); pr.State == handoff.LandingMerged || f.GitData.Merges() != 0 || mainHolds(f) {
		t.Fatalf("the unjudged release's change was merged: %+v, %d merges", pr, f.GitData.Merges())
	}

	if _, err := f.Control.Retry(ctx, f.Issue(number), ownerActor(f)); err != nil {
		t.Fatalf("the Owner's retry: %v", err)
	}
	if got := f.Record(number).State; got != core.StateReady {
		t.Fatalf("the retried issue is %s, want Ready", got)
	}
	if f.GitData.Merges() != 0 || mainHolds(f) {
		t.Fatal("the retry merged the pull request of the unjudged release")
	}
}
