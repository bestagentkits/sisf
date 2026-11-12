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
	unlock := readOnlyDir(t, f.Trust.Dir)
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
	unlock()
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
	if pr := candidatePR(t, f, number); pr.State != handoff.LandingLeftOpen || !strings.Contains(pr.Reason, "no longer be judged") || pr.Waiting != "" {
		t.Errorf("the pull request record after the block is %+v, want left for the Owner with nothing it waits on", pr)
	}
	if comment := commentContaining(t, f, number, "<!-- sisf:progress "); !strings.Contains(comment, "**Blocked:**") || !strings.Contains(comment, stop.Summary()) {
		t.Errorf("the progress comment does not say the issue is blocked, in the block's words:\n%s", comment)
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

// readOnlyDir makes dir read-only until the returned function restores it.
// The harness's trust volume is a real directory, so its writes are held
// back with file permissions. A user those permissions do not hold, such as
// root in a container, can still write it: the directory is probed, and the
// test is skipped rather than run without the failure it depends on.
func readOnlyDir(t *testing.T, dir string) (unlock func()) {
	t.Helper()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	restored := false
	unlock = func() {
		if !restored {
			restored = true
			if err := os.Chmod(dir, 0o700); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Cleanup(unlock)
	probe, err := os.CreateTemp(dir, "write-probe-*")
	if err == nil {
		_ = probe.Close()
		_ = os.Remove(probe.Name())
		unlock()
		t.Skip("the trust volume stays writable with its write permission removed (the tests run as a user file permissions do not hold, such as root), " +
			"so its LKG write cannot be made to fail; run the tests as an ordinary user")
	}
	return unlock
}
