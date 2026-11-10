package release

import (
	"context"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/signing/delegation"
	"github.com/bestagentkits/sisf/pkg/core"
)

func TestAPromotedLKGStaysRestorableAfterTheKeyThatSignedItExpired(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := context.Background()
	first, firstRun := issueRef(50), core.RunID("run-50")
	w.seedReleaseCandidate(first, firstRun, firstBuild)
	firstRequest := w.request(first, firstRun, firstBuild)
	if out, err := w.pipeline().Release(ctx, firstRequest); err != nil || out.Result != ResultClosed {
		t.Fatalf("first release: %s, %v", out.Result, err)
	}
	firstRelease := core.ReleaseDigest(firstBuild)
	signedBy := w.lkg().ArtifactKeyID

	// The delegated keys live a day; a week later both have expired.
	w.clock.Advance(8 * 24 * time.Hour)
	signed := firstRequest.Artifact
	if err := w.keys.verifier.Verify(ctx, signed.SigningPayload(), signed.Signature); err == nil {
		t.Fatal("the expired artifact key still verifies; the test does not exercise expiry")
	}
	w.keys.qa = w.keys.delegate(t, delegation.PurposeQAReceipt)
	w.keys.artifact = w.keys.delegate(t, delegation.PurposeDeployArtifact)
	w.backend.errorRate[core.ReleaseDigest(secondBuild)] = 0.10

	second, secondRun := issueRef(51), core.RunID("run-51")
	w.seedReleaseCandidate(second, secondRun, secondBuild)
	out, err := w.pipeline().Release(ctx, w.request(second, secondRun, secondBuild))
	if err != nil || out.Result != ResultBlocked {
		t.Fatalf("second release: %s, %v", out.Result, err)
	}
	if out.RestoredTo != firstRelease || out.Observed.Digest != firstRelease || !out.Observed.Healthy {
		t.Fatalf("restored to %s observing %+v, want the first release running again", out.RestoredTo, out.Observed)
	}
	if lkg := w.lkg(); lkg.Digest != firstRelease || lkg.ArtifactKeyID != signedBy {
		t.Fatalf("LKG %+v, want the first release as verified at its promotion", lkg)
	}
}
