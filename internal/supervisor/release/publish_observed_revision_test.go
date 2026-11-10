package release

import (
	"context"
	"slices"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
)

func TestAHealthyCanaryIsPublishedOnlyOnceItsObservedRevisionIsTheRelease(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	issue, run := issueRef(1), core.RunID("run-1")
	w.seedReleaseCandidate(issue, run, firstBuild)

	out, err := w.pipeline().Release(context.Background(), w.request(issue, run, firstBuild))
	if err != nil {
		t.Fatalf("Release: %v (outcome %+v)", err, out)
	}
	release := core.ReleaseDigest(firstBuild)
	if out.Result != ResultClosed || out.State != core.StateClosed {
		t.Fatalf("outcome %s in %s, want closed in Closed", out.Result, out.State)
	}
	w.requireState(issue, core.StateClosed)
	if out.Observed.Digest != release || !out.Observed.Healthy {
		t.Fatalf("the outcome rests on observation %+v, want the release running healthy", out.Observed)
	}
	promotions, rollbacks, _, _ := w.backend.snapshot()
	if !slices.Equal(promotions, []int{5, 25, 100}) || len(rollbacks) != 0 {
		t.Fatalf("promotions %v rollbacks %v, want a 5/25/100 canary and no rollback", promotions, rollbacks)
	}
	if got := w.record(issue).ReleaseDigest; got != release {
		t.Fatalf("the record names release %s, want %s", got, release)
	}
	lkg := w.lkg()
	if lkg.Digest != release || lkg.Source != SourcePromoted || lkg.ArtifactKeyID != w.keys.artifact.KeyID() {
		t.Fatalf("LKG after the release is %+v, want the verified release", lkg)
	}
	if len(w.publisher.entries) != 1 || w.publisher.entries[0].Release != release || out.PublicationRef == "" {
		t.Fatalf("published %+v (ref %q), want one changelog and catalog entry for the release", w.publisher.entries, out.PublicationRef)
	}
}

func TestABlueGreenReleaseSwitchesAllTrafficAtOnceWhenTheBackendCannotSplit(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	delete(w.backend.caps, core.CapDeployCanary)
	issue, run := issueRef(2), core.RunID("run-2")
	w.seedReleaseCandidate(issue, run, firstBuild)

	out, err := w.pipeline().Release(context.Background(), w.request(issue, run, firstBuild))
	if err != nil || out.Result != ResultClosed {
		t.Fatalf("Release: %v (outcome %s)", err, out.Result)
	}
	if promotions, _, _, _ := w.backend.snapshot(); !slices.Equal(promotions, []int{100}) {
		t.Fatalf("promotions %v, want one switch to 100 percent", promotions)
	}
}

func TestAReleaseTheRunningSystemDoesNotReportIsNeverVerified(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	// Every call answers as if it worked, but what runs is still the LKG.
	w.backend.runningInstead = lkgDigest
	issue, run := issueRef(3), core.RunID("run-3")
	w.seedReleaseCandidate(issue, run, firstBuild)

	out, err := w.pipeline().Release(context.Background(), w.request(issue, run, firstBuild))
	if err != nil {
		t.Fatalf("Release: %v", err)
	}
	if out.Result != ResultBlocked || out.RestoredTo != lkgDigest {
		t.Fatalf("outcome %s restored to %s, want blocked on the LKG", out.Result, out.RestoredTo)
	}
	w.requireState(issue, core.StateBlocked)
	if len(w.publisher.entries) != 0 {
		t.Fatalf("a release that was never observed running was published: %+v", w.publisher.entries)
	}
	if lkg := w.lkg(); lkg.Digest != lkgDigest {
		t.Fatalf("the LKG moved to %s although the release was never observed", lkg.Digest)
	}
}

func TestAVerifiedReleaseThatCannotBePublishedStaysVerifiedAndOpen(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.publisher.err = core.NewError(core.CodeUnavailable, "GitHub is down")
	issue, run := issueRef(4), core.RunID("run-4")
	w.seedReleaseCandidate(issue, run, firstBuild)

	out, err := w.pipeline().Release(context.Background(), w.request(issue, run, firstBuild))
	if !core.HasCode(err, core.CodeUnavailable) || out.Result != ResultHeld {
		t.Fatalf("Release = %s, %v; want held on the failed publication", out.Result, err)
	}
	w.requireState(issue, core.StateVerified)
}
