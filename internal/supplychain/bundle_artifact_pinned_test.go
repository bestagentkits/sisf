package supplychain

import (
	"context"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/signing/delegation"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/deploy"
)

// bundleArtifact is an unsigned artifact of a runtime image pinned by its
// own digest and a content bundle, carrying the pair digest.
func bundleArtifact() deploy.Artifact {
	image := "wordpress:7.0@" + string(imageDigest)
	bundle := "sha256:" + strings.Repeat("c", 64)
	return deploy.Artifact{
		FactoryID: testFactory, Unit: "app", Digest: deploy.PairDigest(image, bundle), Image: image, Bundle: bundle,
		SBOMRef: "sbom/x", ProvenanceRef: "provenance/x",
	}
}

func TestASignedBundleArtifactIsAcceptedAsItsPair(t *testing.T) {
	t.Parallel()
	tr := newTrust(t)
	signer := tr.signer(t, delegation.PurposeDeployArtifact, delegation.HolderSupervisor)
	a, err := SignArtifact(context.Background(), signer, bundleArtifact())
	if err != nil {
		t.Fatalf("SignArtifact: %v", err)
	}
	if err := VerifyArtifact(context.Background(), tr.verifier, a, testFactory, a.Digest); err != nil {
		t.Fatalf("VerifyArtifact: %v", err)
	}
	// Swapping the bundle after signing breaks the signature and the pair.
	swapped := a
	swapped.Bundle = "sha256:" + strings.Repeat("d", 64)
	if err := VerifyArtifact(context.Background(), tr.verifier, swapped, testFactory, swapped.Digest); err == nil {
		t.Fatal("a swapped bundle verified")
	}
}

func TestABundleArtifactMustCarryItsPairDigest(t *testing.T) {
	t.Parallel()
	cases := map[string]func(a *deploy.Artifact){
		"the image digest":  func(a *deploy.Artifact) { a.Digest = core.ReleaseDigest(imageDigest) },
		"another pair":      func(a *deploy.Artifact) { a.Digest = deploy.PairDigest(a.Image, "sha256:"+strings.Repeat("e", 64)) },
		"an unpinned image": func(a *deploy.Artifact) { a.Image = "wordpress:7.0"; a.Digest = deploy.PairDigest(a.Image, a.Bundle) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a := bundleArtifact()
			mutate(&a)
			wantCode(t, CheckPinned(a), core.CodeInvalidArgument)
		})
	}
}
