package stages

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/internal/signing"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/deploy"
)

// The artifact the release is asked for carries the build's bundle, bound
// by the pair digest QA judged; a build recorded before bundles were
// built, with no bundle field, is still released as an image alone.
func TestTheReleaseRequestSignsTheBuildsBundle(t *testing.T) {
	t.Parallel()
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := signing.NewSigner(key)
	if err != nil {
		t.Fatal(err)
	}
	s := &ReleaseStage{FactoryID: "acme", ArtifactSigner: signer}
	image := "ghcr.io/acme/wordpress@sha256:" + strings.Repeat("a", 64)
	bundle := "sha256:" + strings.Repeat("b", 64)
	sbom, prov := "sha256:"+strings.Repeat("c", 64), "sha256:"+strings.Repeat("d", 64)
	pair := handoff.BuildRecord{Unit: "app", Image: image, Bundle: bundle, Digest: core.Digest(deploy.PairDigest(image, bundle)), SBOMRef: sbom, ProvenanceRef: prov}
	req, err := s.request(context.Background(), core.StateRecord{}, QAResult{}, handoff.CandidateDoc{}, pair)
	if err != nil {
		t.Fatal(err)
	}
	if req.Artifact.Bundle != bundle || req.Artifact.Digest != deploy.PairDigest(image, bundle) || req.Artifact.Validate() != nil {
		t.Fatalf("artifact %+v, want the signed pair", req.Artifact)
	}

	var old handoff.BuildRecord
	stored := `{"commit":"` + strings.Repeat("1", 40) + `","digest":"` + image[strings.Index(image, "@")+1:] + `","unit":"app","image":"` + image +
		`","builder":"sandbox:docker","tests_passed":true,"sbom_ref":"` + sbom + `","provenance_ref":"` + prov + `"}`
	if err := json.Unmarshal([]byte(stored), &old); err != nil {
		t.Fatal(err)
	}
	req, err = s.request(context.Background(), core.StateRecord{}, QAResult{}, handoff.CandidateDoc{}, old)
	if err != nil || req.Artifact.Bundle != "" || req.Artifact.Validate() != nil {
		t.Fatalf("artifact %+v, %v; want the image alone", req.Artifact, err)
	}
}
