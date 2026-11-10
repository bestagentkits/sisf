package supplychain

import (
	"context"
	"regexp"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/deploy"
)

// pinnedImagePattern is an image reference pinned by a full digest:
// "name[:tag]@sha256:<64 hex>" or the sha512 form.
var pinnedImagePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._/:-]*@(sha256:[a-f0-9]{64}|sha512:[a-f0-9]{128})$`)

// CheckPinned requires the artifact to name its image by the digest it
// carries, and to point to its SBOM and provenance. An image named by a tag
// alone can move after it was signed, so it is never released.
func CheckPinned(a deploy.Artifact) error {
	if err := a.Digest.Validate(); err != nil {
		return invalid("artifact %q is not pinned by a full digest", a.Unit)
	}
	if !pinnedImagePattern.MatchString(a.Image) {
		return invalid("artifact %q image is not pinned by a digest", a.Unit)
	}
	if _, digest, _ := strings.Cut(a.Image, "@"); digest != string(a.Digest) {
		return invalid("artifact %q image is pinned to another digest than the artifact's", a.Unit)
	}
	if strings.TrimSpace(a.SBOMRef) == "" || strings.TrimSpace(a.ProvenanceRef) == "" {
		return invalid("artifact %q has no SBOM or provenance", a.Unit)
	}
	return nil
}

// SignArtifact signs a pinned artifact for the factory it names with signer,
// a delegated key for deploy artifacts. An unpinned artifact is never signed.
func SignArtifact(ctx context.Context, signer core.Signer, a deploy.Artifact) (deploy.Artifact, error) {
	if signer == nil {
		return deploy.Artifact{}, invalid("signing an artifact needs a signer")
	}
	if err := a.FactoryID.Validate(); err != nil {
		return deploy.Artifact{}, err
	}
	if a.Unit == "" {
		return deploy.Artifact{}, invalid("artifact needs a unit")
	}
	if err := CheckPinned(a); err != nil {
		return deploy.Artifact{}, err
	}
	sig, err := signer.Sign(ctx, a.SigningPayload())
	if err != nil {
		return deploy.Artifact{}, err
	}
	a.Signature = sig
	return a, nil
}

// VerifyArtifact is the release supervisor's check before an artifact is
// staged. It returns nil only when the artifact is signed, pinned, built
// for factory, its signature is good under verifier, and observed, the
// digest of what was actually fetched, is the digest it was signed for.
//
// An unsigned or wrongly signed artifact is SIGNATURE_INVALID, an unpinned
// one INVALID_ARGUMENT, and one whose fetched bytes differ TAMPERED.
func VerifyArtifact(ctx context.Context, verifier core.Verifier, a deploy.Artifact, factory core.FactoryID, observed core.ReleaseDigest) error {
	if verifier == nil {
		return core.NewError(core.CodeSignatureInvalid, "there is no verifier to check the artifact with")
	}
	if a.Signature.Validate() != nil {
		return core.NewError(core.CodeSignatureInvalid, "artifact "+a.Unit+" is not signed")
	}
	if err := a.ValidateFor(factory); err != nil {
		return err
	}
	if err := CheckPinned(a); err != nil {
		return err
	}
	if err := verifier.Verify(ctx, a.SigningPayload(), a.Signature); err != nil {
		return err
	}
	if err := observed.Validate(); err != nil {
		return core.NewError(core.CodeTampered, "the fetched artifact has no digest to compare").WithCause(err)
	}
	if observed != a.Digest {
		return core.NewError(core.CodeTampered, "the fetched artifact "+a.Unit+" is not the one that was signed")
	}
	return nil
}
