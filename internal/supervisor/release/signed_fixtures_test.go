package release

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/signing"
	"github.com/bestagentkits/sisf/internal/signing/delegation"
	"github.com/bestagentkits/sisf/internal/supplychain"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/deploy"
	"github.com/bestagentkits/sisf/pkg/qa"
)

const (
	testFactory core.FactoryID     = "acme-hello"
	testUnit                       = "app"
	lkgDigest   core.ReleaseDigest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	firstBuild  core.Digest        = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	secondBuild core.Digest        = "sha256:3333333333333333333333333333333333333333333333333333333333333333"
	otherBuild  core.Digest        = "sha256:4444444444444444444444444444444444444444444444444444444444444444"
	commitSHA   core.CommitSHA     = "0123456789abcdef0123456789abcdef01234567"
	otherSHA    core.CommitSHA     = "89abcdef0123456789abcdef0123456789abcdef"
	suiteDigest core.Digest        = "sha256:5555555555555555555555555555555555555555555555555555555555555555"
)

// startTime is where every fake clock begins.
var startTime = time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)

// keyring is the Owner's root key and the delegated keys the supervisor
// holds: one for QA receipts, one for deploy artifacts.
type keyring struct {
	root     *signing.Signer
	rootPub  ed25519.PublicKey
	verifier *delegation.ChainVerifier
	clock    *fakeClock
	qa       *delegation.DelegatedSigner
	artifact *delegation.DelegatedSigner
}

func newKeyring(t *testing.T, clock *fakeClock) *keyring {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate root key: %v", err)
	}
	root, err := signing.NewSigner(priv)
	if err != nil {
		t.Fatalf("root signer: %v", err)
	}
	verifier, err := delegation.NewChainVerifier(delegation.VerifierConfig{FactoryID: testFactory, Root: pub, Clock: clock})
	if err != nil {
		t.Fatalf("chain verifier: %v", err)
	}
	k := &keyring{root: root, rootPub: pub, verifier: verifier, clock: clock}
	k.qa = k.delegate(t, delegation.PurposeQAReceipt)
	k.artifact = k.delegate(t, delegation.PurposeDeployArtifact)
	return k
}

// delegate issues and loads a supervisor key valid for a day from now.
func (k *keyring) delegate(t *testing.T, purpose delegation.Purpose) *delegation.DelegatedSigner {
	t.Helper()
	return k.delegateTo(t, purpose, delegation.HolderSupervisor)
}

// delegateTo issues and loads a key of holder valid for a day from now.
func (k *keyring) delegateTo(t *testing.T, purpose delegation.Purpose, holder delegation.Holder) *delegation.DelegatedSigner {
	t.Helper()
	issued, err := delegation.Issue(context.Background(), k.root, delegation.IssueRequest{
		FactoryID: testFactory, Purpose: purpose, Holder: holder,
		NotBefore: k.clock.Now(), Lifetime: 24 * time.Hour,
	}, nil)
	if err != nil {
		t.Fatalf("issue %s key: %v", purpose, err)
	}
	signer, err := delegation.Load(context.Background(), k.verifier, delegation.LoadRequest{
		Holder: holder, Delegation: issued.Delegation, PrivateKey: issued.PrivateKey,
	})
	if err != nil {
		t.Fatalf("load %s key: %v", purpose, err)
	}
	return signer
}

// candidate is the candidate of run on issue, built as build.
func candidate(issue core.IssueRef, run core.RunID, build core.Digest) qa.Candidate {
	route := core.ModelRoute{Provider: "anthropic", Model: "mid"}
	return qa.Candidate{
		IssueID: issue, RunID: run, CommitSHA: commitSHA, Digest: build, BaselineSHA: commitSHA, SuiteDigest: suiteDigest,
		Policy: core.PolicyRefs{
			PolicySHA: commitSHA, ReviewSHA: commitSHA, DecisionSHA: commitSHA, FeatureCatalogSHA: commitSHA, ModelRoute: route,
		},
		CodingAgentIdentity: "agent:coder-1", CodingModelRoute: route,
		ChangedPaths: []string{"examples/hello-app/main.go"}, Attempt: 1,
	}
}

// receiptFor is a passing receipt for c, signed with the QA key.
func (k *keyring) receiptFor(t *testing.T, c qa.Candidate, mutate func(*core.QAReceipt)) core.QAReceipt {
	t.Helper()
	var checks []core.QACheck
	for _, gate := range core.RequiredHardGates() {
		checks = append(checks, core.QACheck{Name: gate, Kind: core.CheckHardGate, Passed: true})
	}
	points := map[string]int{
		core.RubricCorrectness: 28, core.RubricSecurity: 23, core.RubricContract: 18, core.RubricExperience: 12, core.RubricObservability: 9,
	}
	score := 0
	for _, category := range core.RubricCategories() {
		checks = append(checks, core.QACheck{Name: category.Name, Kind: core.CheckRubric, Passed: true,
			Points: points[category.Name], MaxPoints: category.MaxPoints})
		score += points[category.Name]
	}
	now := k.clock.Now()
	r := core.QAReceipt{
		FactoryID: testFactory, IssueID: c.IssueID, RunID: c.RunID, CandidateSHA: c.CommitSHA, CandidateDigest: c.Digest,
		SuiteDigest: c.SuiteDigest, BaselineSHA: c.BaselineSHA, PolicySHA: c.Policy.PolicySHA,
		QAAgentIdentity: "agent:qa-1", CodingAgentIdentity: c.CodingAgentIdentity,
		CodingModelRoute: c.CodingModelRoute, QAModelRoute: core.ModelRoute{Provider: "openai", Model: "mid"},
		Checks: checks, Score: score, IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(48 * time.Hour), EvaluatorAttempt: c.Attempt,
	}
	if mutate != nil {
		mutate(&r)
	}
	sig, err := k.qa.Sign(context.Background(), r.SigningPayload())
	if err != nil {
		t.Fatalf("sign receipt: %v", err)
	}
	r.Signature = sig
	return r
}

// artifactFor is the signed artifact of build.
func (k *keyring) artifactFor(t *testing.T, build core.Digest) deploy.Artifact {
	t.Helper()
	a, err := supplychain.SignArtifact(context.Background(), k.artifact, unsignedArtifact(build))
	if err != nil {
		t.Fatalf("sign artifact: %v", err)
	}
	return a
}

// unsignedArtifact is a pinned artifact of build with no signature.
func unsignedArtifact(build core.Digest) deploy.Artifact {
	return deploy.Artifact{
		FactoryID: testFactory, Unit: testUnit, Digest: core.ReleaseDigest(build),
		Image: "registry.local/hello-app@" + string(build), SBOMRef: "sbom:" + string(build), ProvenanceRef: "provenance:" + string(build),
	}
}
