package release

import (
	"context"
	"sync"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
	"github.com/bestagentkits/sisf/pkg/deploy"
)

// heldPrograms stands for the supervisor's program store: it measures the
// program kept under each digest. A program it was told was replaced
// measures as the replacement; one it was told was removed is not found.
type heldPrograms struct {
	mu       sync.Mutex
	replaced map[core.ReleaseDigest]core.ReleaseDigest
	removed  map[core.ReleaseDigest]bool
}

func (h *heldPrograms) ObserveArtifact(_ context.Context, a deploy.Artifact) (core.ReleaseDigest, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.removed[a.Digest] {
		return "", core.NewError(core.CodeNotFound, "the store holds no program "+string(a.Digest))
	}
	if other, ok := h.replaced[a.Digest]; ok {
		return other, nil
	}
	return a.Digest, nil
}

// replace makes the program kept for digest measure as other.
func (h *heldPrograms) replace(digest core.Digest, other core.Digest) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.replaced == nil {
		h.replaced = map[core.ReleaseDigest]core.ReleaseDigest{}
	}
	h.replaced[core.ReleaseDigest(digest)] = core.ReleaseDigest(other)
}

// remove makes the program kept for digest gone.
func (h *heldPrograms) remove(digest core.Digest) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.removed == nil {
		h.removed = map[core.ReleaseDigest]bool{}
	}
	h.removed[core.ReleaseDigest(digest)] = true
}

// A release is verified on the bytes the supervisor measures, not on the
// digest the signed artifact carries: an artifact signed for one build
// whose kept program is now another is TAMPERED and never staged, and one
// whose program is gone is never staged either.
func TestAReleaseWhoseProgramIsNotTheSignedBuildIsNeverDeployed(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		change func(h *heldPrograms)
		code   core.ErrorCode
	}{
		"the program was replaced": {func(h *heldPrograms) { h.replace(firstBuild, otherBuild) }, core.CodeTampered},
		"the program is gone":      {func(h *heldPrograms) { h.remove(firstBuild) }, core.CodeNotFound},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			w := newWorld(t)
			issue, run := issueRef(240), core.RunID("run-240")
			w.seedReleaseCandidate(issue, run, firstBuild)
			c.change(w.programs)
			out, err := w.pipeline().Release(context.Background(), w.request(issue, run, firstBuild))
			requireRefusedUntouched(t, w, issue, out, err, c.code)
		})
	}
}

// A verified release becomes the last known good only on the program still
// kept for it: one replaced since its verification is not recorded, and the
// release is blocked as not judged rather than held for a signature.
func TestAVerifiedReleaseWhoseProgramWasReplacedNeverBecomesTheLKG(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t)
	unlock := w.lockTrustVolume()
	issue, run := issueRef(241), core.RunID("run-241")
	w.seedReleaseCandidate(issue, run, firstBuild)
	req := w.request(issue, run, firstBuild)
	if out, err := w.pipeline().Release(ctx, req); err == nil || out.State != core.StateVerified {
		t.Fatalf("Release = %+v, %v; want held in Verified on the LKG", out, err)
	}
	unlock()
	w.programs.replace(firstBuild, otherBuild)

	out, err := w.pipeline().FinishVerified(ctx, req)
	if err != nil || out.Result != ResultBlocked || out.Cause != statemachine.BlockReleaseUnjudged {
		t.Fatalf("FinishVerified = %+v, %v; want blocked as not judged", out, err)
	}
	if lkg := w.lkg(); lkg.Digest == core.ReleaseDigest(firstBuild) {
		t.Fatalf("the LKG is %+v, a release whose program was replaced", lkg)
	}
}

func TestAPipelineWithoutAnArtifactObserverIsRefused(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.cfg.Observer = nil
	if _, err := New(w.cfg); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Fatalf("New without an observer: %v, want INVALID_ARGUMENT", err)
	}
}
