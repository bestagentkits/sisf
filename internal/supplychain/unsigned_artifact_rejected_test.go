package supplychain

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/signing"
	"github.com/bestagentkits/sisf/internal/signing/delegation"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/deploy"
)

// pinnedArtifact is an unsigned artifact pinned by imageDigest.
func pinnedArtifact() deploy.Artifact {
	return deploy.Artifact{
		FactoryID: testFactory, Unit: "app", Digest: core.ReleaseDigest(imageDigest),
		Image:   "ghcr.io/acme/app:1.4.0@" + string(imageDigest),
		SBOMRef: "sbom/" + string(otherDigest), ProvenanceRef: "provenance/" + string(otherDigest),
	}
}

// signedArtifact returns an artifact signed by a delegated deploy key.
func signedArtifact(t *testing.T, tr *trust) deploy.Artifact {
	t.Helper()
	signer := tr.signer(t, delegation.PurposeDeployArtifact, delegation.HolderSupervisor)
	a, err := SignArtifact(context.Background(), signer, pinnedArtifact())
	if err != nil {
		t.Fatalf("SignArtifact: %v", err)
	}
	return a
}

func TestSignedPinnedArtifactIsAccepted(t *testing.T) {
	t.Parallel()
	tr := newTrust(t)
	a := signedArtifact(t, tr)
	if err := VerifyArtifact(context.Background(), tr.verifier, a, testFactory, a.Digest); err != nil {
		t.Fatalf("VerifyArtifact: %v", err)
	}
}

func TestUnsignedArtifactIsRejected(t *testing.T) {
	t.Parallel()
	tr := newTrust(t)
	a := pinnedArtifact()
	wantCode(t, VerifyArtifact(context.Background(), tr.verifier, a, testFactory, a.Digest), core.CodeSignatureInvalid)

	partial := signedArtifact(t, tr)
	partial.Signature.Value = nil
	wantCode(t, VerifyArtifact(context.Background(), tr.verifier, partial, testFactory, partial.Digest), core.CodeSignatureInvalid)
	wantCode(t, VerifyArtifact(context.Background(), nil, signedArtifact(t, tr), testFactory, partial.Digest), core.CodeSignatureInvalid)
}

func TestWronglySignedArtifactIsRejected(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cases := map[string]func(t *testing.T, tr *trust) deploy.Artifact{
		"key without a delegation": func(t *testing.T, _ *trust) deploy.Artifact {
			_, priv, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatalf("generate key: %v", err)
			}
			rogue, err := signing.NewSigner(priv)
			if err != nil {
				t.Fatalf("NewSigner: %v", err)
			}
			a, err := SignArtifact(ctx, rogue, pinnedArtifact())
			if err != nil {
				t.Fatalf("SignArtifact: %v", err)
			}
			return a
		},
		"root key used directly": func(t *testing.T, tr *trust) deploy.Artifact {
			a, err := SignArtifact(ctx, tr.root, pinnedArtifact())
			if err != nil {
				t.Fatalf("SignArtifact: %v", err)
			}
			return a
		},
		"key for another purpose": func(t *testing.T, tr *trust) deploy.Artifact {
			qaKey := tr.signer(t, delegation.PurposeQAReceipt, delegation.HolderSupervisor)
			if _, err := SignArtifact(ctx, qaKey, pinnedArtifact()); !core.HasCode(err, core.CodePermissionDenied) {
				t.Fatalf("a QA receipt key signed an artifact: %v", err)
			}
			a := signedArtifact(t, tr)
			a.Signature.KeyID = qaKey.KeyID()
			return a
		},
		"field changed after signing": func(t *testing.T, tr *trust) deploy.Artifact {
			a := signedArtifact(t, tr)
			a.SBOMRef = "sbom/forged"
			return a
		},
		"revoked key": func(t *testing.T, tr *trust) deploy.Artifact {
			a := signedArtifact(t, tr)
			list, err := delegation.SignRevocations(ctx, tr.root, delegation.RevocationList{
				FactoryID: testFactory, Sequence: 1, IssuedAt: tr.clock.Now(), KeyIDs: []string{a.Signature.KeyID},
			})
			if err != nil {
				t.Fatalf("SignRevocations: %v", err)
			}
			if err := tr.verifier.ApplyRevocations(ctx, list); err != nil {
				t.Fatalf("ApplyRevocations: %v", err)
			}
			return a
		},
		"expired key": func(t *testing.T, tr *trust) deploy.Artifact {
			a := signedArtifact(t, tr)
			tr.clock.Advance(25 * time.Hour)
			return a
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			tr := newTrust(t)
			a := build(t, tr)
			wantCode(t, VerifyArtifact(ctx, tr.verifier, a, testFactory, a.Digest), core.CodeSignatureInvalid)
		})
	}
}

func TestDigestMismatchedArtifactIsRejected(t *testing.T) {
	t.Parallel()
	tr := newTrust(t)
	a := signedArtifact(t, tr)
	ctx := context.Background()
	wantCode(t, VerifyArtifact(ctx, tr.verifier, a, testFactory, core.ReleaseDigest(otherDigest)), core.CodeTampered)
	wantCode(t, VerifyArtifact(ctx, tr.verifier, a, testFactory, ""), core.CodeTampered)

	// Repinning the artifact to the fetched digest breaks its signature.
	repinned := a
	repinned.Digest = core.ReleaseDigest(otherDigest)
	repinned.Image = "ghcr.io/acme/app@" + string(otherDigest)
	wantCode(t, VerifyArtifact(ctx, tr.verifier, repinned, testFactory, repinned.Digest), core.CodeSignatureInvalid)
}

func TestUnpinnedArtifactIsRejected(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cases := map[string]func(*deploy.Artifact){
		"image by tag":                func(a *deploy.Artifact) { a.Image = "ghcr.io/acme/app:latest" },
		"image without tag or digest": func(a *deploy.Artifact) { a.Image = "ghcr.io/acme/app" },
		"image pinned elsewhere":      func(a *deploy.Artifact) { a.Image = "ghcr.io/acme/app@" + string(otherDigest) },
		"short digest":                func(a *deploy.Artifact) { a.Image = "ghcr.io/acme/app@sha256:1111" },
		"no repository name":          func(a *deploy.Artifact) { a.Image = "@" + string(imageDigest) },
		"no SBOM":                     func(a *deploy.Artifact) { a.SBOMRef = "" },
		"no provenance":               func(a *deploy.Artifact) { a.ProvenanceRef = "" },
	}
	for name, unpin := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			tr := newTrust(t)
			signer := tr.signer(t, delegation.PurposeDeployArtifact, delegation.HolderSupervisor)

			unsigned := pinnedArtifact()
			unpin(&unsigned)
			_, err := SignArtifact(ctx, signer, unsigned)
			wantCode(t, err, core.CodeInvalidArgument)

			// Signed while unpinned by a careless signer, it is still refused.
			sig, err := signer.Sign(ctx, unsigned.SigningPayload())
			if err != nil {
				t.Fatalf("Sign: %v", err)
			}
			unsigned.Signature = sig
			wantCode(t, VerifyArtifact(ctx, tr.verifier, unsigned, testFactory, unsigned.Digest), core.CodeInvalidArgument)
		})
	}
}

func TestArtifactForAnotherFactoryIsRejected(t *testing.T) {
	t.Parallel()
	tr := newTrust(t)
	a := signedArtifact(t, tr)
	wantCode(t, VerifyArtifact(context.Background(), tr.verifier, a, "acme-other", a.Digest), core.CodeInvalidArgument)
}
