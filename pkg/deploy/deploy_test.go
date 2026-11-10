package deploy_test

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/deploy"
)

const (
	digest      = core.ReleaseDigest("sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	otherDigest = core.ReleaseDigest("sha256:fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210")
)

func TestSelfPublishNeedsASwitchARollbackAndObservation(t *testing.T) {
	blueGreen := core.CapabilitySet{
		core.CapDeployBlueGreen:        core.SupportSupported,
		core.CapDeployRollback:         core.SupportSupported,
		core.CapDeployObservedRevision: core.SupportSupported,
		core.CapDeployMetrics:          core.SupportSupported,
	}
	if !deploy.SelfPublishAllowed(blueGreen) {
		t.Fatal("blue-green with rollback, observed revision and metrics may self-publish")
	}
	canary := core.CapabilitySet{
		core.CapDeployCanary:           core.SupportSupported,
		core.CapDeployRollback:         core.SupportSupported,
		core.CapDeployObservedRevision: core.SupportSupported,
		core.CapDeployMetrics:          core.SupportSupported,
	}
	if !deploy.SelfPublishAllowed(canary) {
		t.Fatal("canary with rollback, observed revision and metrics may self-publish")
	}
	for _, missing := range []core.Capability{core.CapDeployBlueGreen, core.CapDeployRollback,
		core.CapDeployObservedRevision, core.CapDeployMetrics} {
		caps := core.CapabilitySet{}
		for c, level := range blueGreen {
			caps[c] = level
		}
		delete(caps, missing)
		if deploy.SelfPublishAllowed(caps) {
			t.Errorf("without %s a release must hold, never self-publish", missing)
		}
		caps[missing] = core.SupportUnverified
		if deploy.SelfPublishAllowed(caps) {
			t.Errorf("an unverified %s must not allow self-publishing", missing)
		}
	}
	if deploy.SelfPublishAllowed(nil) {
		t.Error("a backend that declares nothing must not self-publish")
	}
}

func TestErrorRateIsUnknownWithoutRequests(t *testing.T) {
	if _, ok := (deploy.Metrics{}).ErrorRate(); ok {
		t.Fatal("no requests means no error rate, not a zero error rate")
	}
	rate, ok := (deploy.Metrics{Requests: 200, ServerErrors: 5}).ErrorRate()
	if !ok || rate != 0.025 {
		t.Fatalf("ErrorRate() = %v, %v", rate, ok)
	}
}

func TestArtifactValidation(t *testing.T) {
	good := deploy.Artifact{
		Unit:      "app",
		Digest:    digest,
		Image:     "ghcr.io/acme/app@" + string(digest),
		Signature: core.Signature{KeyID: "release-1", Algorithm: "ed25519", Value: []byte{1}},
	}
	if err := good.Validate(); err != nil {
		t.Fatalf("valid artifact rejected: %v", err)
	}
	cases := map[string]func(*deploy.Artifact){
		"no unit":       func(a *deploy.Artifact) { a.Unit = "" },
		"mutable tag":   func(a *deploy.Artifact) { a.Digest = "latest" },
		"unsigned":      func(a *deploy.Artifact) { a.Signature = core.Signature{} },
		"no image name": func(a *deploy.Artifact) { a.Image = "" },
	}
	for name, mutate := range cases {
		a := good
		mutate(&a)
		if core.CodeOf(a.Validate()) != core.CodeInvalidArgument {
			t.Errorf("%s: want %s, got %v", name, core.CodeInvalidArgument, a.Validate())
		}
	}
}

func signedArtifact() deploy.Artifact {
	return deploy.Artifact{
		Unit:          "app",
		Digest:        digest,
		Image:         "ghcr.io/acme/app@" + string(digest),
		Signature:     core.Signature{KeyID: "release-1", Algorithm: "ed25519", Value: []byte{1}},
		SBOMRef:       "artifact:sbom-1",
		ProvenanceRef: "artifact:provenance-1",
	}
}

// artifactMutations changes each field of an artifact in turn, keyed by the Go
// field name so a field added later cannot be left out of the payload unseen.
var artifactMutations = map[string]func(*deploy.Artifact){
	"Unit":          func(a *deploy.Artifact) { a.Unit = "control" },
	"Digest":        func(a *deploy.Artifact) { a.Digest = otherDigest },
	"Image":         func(a *deploy.Artifact) { a.Image = "ghcr.io/acme/other@" + string(digest) },
	"SBOMRef":       func(a *deploy.Artifact) { a.SBOMRef = "artifact:sbom-2" },
	"ProvenanceRef": func(a *deploy.Artifact) { a.ProvenanceRef = "" },
}

func TestChangingAnyBoundArtifactFieldChangesTheSigningPayload(t *testing.T) {
	t.Parallel()
	base := signedArtifact().SigningPayload()
	fields := reflect.TypeOf(deploy.Artifact{})
	for i := range fields.NumField() {
		name := fields.Field(i).Name
		if name == "Signature" {
			continue
		}
		mutate, ok := artifactMutations[name]
		if !ok {
			t.Errorf("artifact field %s has no case proving the signature covers it", name)
			continue
		}
		changed := signedArtifact()
		mutate(&changed)
		if bytes.Equal(base, changed.SigningPayload()) {
			t.Errorf("changing %s left the signing payload unchanged", name)
		}
	}
	// The same text split differently over two fields is another artifact.
	shifted := signedArtifact()
	shifted.SBOMRef, shifted.ProvenanceRef = shifted.SBOMRef+"a", shifted.ProvenanceRef[1:]
	if bytes.Equal(base, shifted.SigningPayload()) {
		t.Error("moving text between two references left the signing payload unchanged")
	}
}

func TestTheArtifactPayloadIsStableAndLeavesTheSignatureOut(t *testing.T) {
	t.Parallel()
	artifact := signedArtifact()
	base := artifact.SigningPayload()
	if !bytes.Equal(base, signedArtifact().SigningPayload()) {
		t.Fatal("two equal artifacts produced different payloads")
	}
	if !strings.HasPrefix(string(base), "sisf-signed-record\ndomain=20:sisf.deploy_artifact\nversion=1:1\n") {
		t.Fatalf("payload does not open with the artifact domain and version: %q", base)
	}
	artifact.Signature = core.Signature{KeyID: "another-key", Algorithm: "ed25519", Value: []byte{9}}
	if !bytes.Equal(base, artifact.SigningPayload()) {
		t.Fatal("the payload depends on the signature it is signed with")
	}
	if bytes.Equal(base, core.QAReceipt{}.SigningPayload()) {
		t.Fatal("an artifact payload equals a receipt payload")
	}
}

// holdStub proves the contract can be met with the exported types alone.
type holdStub struct{}

func (holdStub) Name() string { return "stub" }
func (holdStub) Capabilities(context.Context) (core.CapabilitySet, error) {
	return core.CapabilitySet{core.CapDeployCanary: core.SupportNotSupported}, nil
}
func (holdStub) Stage(_ context.Context, a deploy.Artifact) (deploy.Revision, error) {
	return deploy.Revision{Unit: a.Unit, Digest: a.Digest, ID: "green"}, nil
}
func (holdStub) Preview(_ context.Context, r deploy.Revision) (deploy.Preview, error) {
	return deploy.Preview{Revision: r, URL: "http://127.0.0.1:7462"}, nil
}
func (holdStub) Measure(context.Context, deploy.Revision, time.Duration) (deploy.Metrics, error) {
	return deploy.Metrics{}, core.NewError(core.CodeReleaseHold, "metrics endpoint unreachable")
}
func (holdStub) Promote(_ context.Context, _ deploy.Revision, percent int) error {
	if percent != 100 {
		return core.NewError(core.CodeNotSupported, "this backend cannot split traffic")
	}
	return nil
}
func (holdStub) ObservedRevision(_ context.Context, unit string) (deploy.Observed, error) {
	return deploy.Observed{Unit: unit, Digest: digest, Healthy: true}, nil
}
func (holdStub) Rollback(context.Context, deploy.Revision) error { return nil }

func TestBackendContractIsImplementable(t *testing.T) {
	var backend deploy.Backend = holdStub{}
	ctx := context.Background()
	rev, err := backend.Stage(ctx, deploy.Artifact{Unit: "app", Digest: digest})
	if err != nil || rev.Digest != digest {
		t.Fatalf("Stage = %+v, %v", rev, err)
	}
	if err := backend.Promote(ctx, rev, 25); !core.HasCode(err, core.CodeNotSupported) {
		t.Fatalf("a traffic split the backend lacks must be refused, got %v", err)
	}
	if _, err := backend.Measure(ctx, rev, time.Minute); !core.HasCode(err, core.CodeReleaseHold) {
		t.Fatalf("missing metrics must surface as a hold, got %v", err)
	}
	observed, err := backend.ObservedRevision(ctx, "app")
	if err != nil || !observed.Matches(rev) {
		t.Fatalf("ObservedRevision = %+v, %v", observed, err)
	}
	if observed.Matches(deploy.Revision{Unit: "control", Digest: digest}) {
		t.Error("an observation of one unit says nothing about another")
	}
	unhealthy := observed
	unhealthy.Healthy = false
	if unhealthy.Matches(rev) {
		t.Error("an unhealthy revision is not an observed healthy release")
	}
}
