package qa_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/adapters/agent/fake"
	"github.com/bestagentkits/sisf/internal/policy"
	evaluator "github.com/bestagentkits/sisf/internal/qa"
	"github.com/bestagentkits/sisf/internal/signing"
	"github.com/bestagentkits/sisf/internal/signing/delegation"
	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/qa"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

const (
	factory      core.FactoryID = "acme-hello"
	candidateSHA core.CommitSHA = "cccccccccccccccccccccccccccccccccccccccc"
	baselineSHA  core.CommitSHA = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	policySHA    core.CommitSHA = "dddddddddddddddddddddddddddddddddddddddd"
	buildDigest  core.Digest    = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	otherDigest  core.Digest    = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	suiteDigest  core.Digest    = "sha256:3333333333333333333333333333333333333333333333333333333333333333"
	qaIdentity                  = "agent:qa-1"
	qaWorkspace                 = "ws-qa-1"
)

var (
	issue      = core.IssueRef{Owner: "acme", Repo: "hello-app", Number: 7}
	coderRoute = core.ModelRoute{Provider: "anthropic", Model: "mid"}
	qaRoute    = core.ModelRoute{Provider: "openai", Model: "mid"}
	start      = time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)
)

// candidate is a built candidate written by another agent than the evaluator.
func candidate() qa.Candidate {
	return qa.Candidate{
		IssueID: issue, RunID: "run-0001", CommitSHA: candidateSHA, Digest: buildDigest,
		BaselineSHA: baselineSHA, SuiteDigest: suiteDigest,
		Policy: core.PolicyRefs{PolicySHA: policySHA, ReviewSHA: policySHA, DecisionSHA: policySHA,
			FeatureCatalogSHA: policySHA, ModelRoute: qaRoute},
		CodingAgentIdentity: "agent:coder-1", CodingModelRoute: coderRoute,
		WorkspaceID: "ws-coder-1", Attempt: 1,
	}
}

// repositoryReview is the review criteria of this repository.
func repositoryReview(t *testing.T) string {
	t.Helper()
	content, err := os.ReadFile("../../REVIEW.md")
	if err != nil {
		t.Fatalf("read REVIEW.md: %v", err)
	}
	return string(content)
}

// reviewFiles serves REVIEW.md per commit.
type reviewFiles map[core.CommitSHA]string

func (f reviewFiles) ReadFile(_ context.Context, revision core.CommitSHA, path string) ([]byte, error) {
	content, ok := f[revision]
	if !ok || path != policy.PathReview {
		return nil, core.NewError(core.CodeNotFound, "no such file")
	}
	return []byte(content), nil
}

// baselines serves the baseline; after is served from the second call on
// when set, as a baseline that changed during the evaluation.
type baselines struct {
	mu     sync.Mutex
	first  qa.Baseline
	after  *qa.Baseline
	err    error
	served int
}

func (b *baselines) Baseline(context.Context, qa.Candidate) (qa.Baseline, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.served++
	if b.after != nil && b.served > 1 {
		return *b.after, b.err
	}
	return b.first, b.err
}

// builds maps a commit to its finished build.
type builds map[core.CommitSHA]core.Digest

func (b builds) FinishedBuild(_ context.Context, commit core.CommitSHA) (core.Digest, error) {
	if d, ok := b[commit]; ok {
		return d, nil
	}
	return "", core.NewError(core.CodeNotFound, "no finished build")
}

// gate is a hard gate with a fixed answer.
type gate struct {
	name   qa.GateName
	passed bool
	err    error
	report qa.GateName
}

func (g gate) Name() qa.GateName { return g.name }

func (g gate) Check(context.Context, qa.Candidate) (qa.GateResult, error) {
	if g.err != nil {
		return qa.GateResult{}, g.err
	}
	name := g.name
	if g.report != "" {
		name = g.report
	}
	return qa.GateResult{Gate: name, Passed: g.passed, Detail: "fixture"}, nil
}

// passingGates returns every gate the evaluator is wired with, all passing.
func passingGates() []qa.HardGate {
	var gates []qa.HardGate
	for _, name := range qa.Gates() {
		if name != qa.GateIndependentEvaluator {
			gates = append(gates, gate{name: name, passed: true})
		}
	}
	return gates
}

// env is one evaluator with its fakes, ready to change before build.
type env struct {
	cfg       evaluator.Config
	clock     *sqlstoretest.Clock
	judge     *fake.Backend
	baselines *baselines
	root      *signing.Signer
	verifier  *delegation.ChainVerifier
	signer    *delegation.DelegatedSigner
}

func newEnv(t *testing.T) *env {
	t.Helper()
	clock := sqlstoretest.NewClock(start)
	judge, err := fake.New(fake.Config{Clock: clock, Roles: []agent.Role{agent.RoleQA}})
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	root, err := signing.NewSigner(priv)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := delegation.NewChainVerifier(delegation.VerifierConfig{FactoryID: factory, Root: pub, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	key, err := delegation.Issue(context.Background(), root, delegation.IssueRequest{FactoryID: factory,
		Purpose: delegation.PurposeQAReceipt, Holder: delegation.HolderSupervisor, NotBefore: start, Lifetime: 7 * 24 * time.Hour}, nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := delegation.Load(context.Background(), verifier, delegation.LoadRequest{
		Holder: delegation.HolderSupervisor, Delegation: key.Delegation, PrivateKey: key.PrivateKey})
	if err != nil {
		t.Fatal(err)
	}
	base := &baselines{first: qa.Baseline{CommitSHA: baselineSHA, PolicySHA: policySHA, SuiteDigest: suiteDigest,
		WorkspaceID: qaWorkspace, ReadOnly: true}}
	return &env{
		cfg: evaluator.Config{
			FactoryID: factory, Identity: qaIdentity, Workspace: qaWorkspace, Gates: passingGates(),
			Judge: judge, Baselines: base, Builds: builds{candidateSHA: buildDigest},
			Review: reviewFiles{policySHA: repositoryReview(t)}, Routes: []core.ModelRoute{coderRoute, qaRoute},
			Signer: signer, Verifier: verifier, Clock: clock,
		},
		clock: clock, judge: judge, baselines: base, root: root, verifier: verifier, signer: signer,
	}
}

func (e *env) build(t *testing.T) *evaluator.Evaluator {
	t.Helper()
	ev, err := evaluator.New(e.cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return ev
}

// revoke revokes the receipt key with a root-signed list.
func (e *env) revoke(t *testing.T) {
	t.Helper()
	list, err := delegation.SignRevocations(context.Background(), e.root, delegation.RevocationList{
		FactoryID: factory, Sequence: 1, IssuedAt: e.clock.Now(), KeyIDs: []string{e.signer.KeyID()}})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.verifier.ApplyRevocations(context.Background(), list); err != nil {
		t.Fatal(err)
	}
}

// scores returns rubric points with correctness set and full marks elsewhere.
func scores(correctness int) map[qa.Category]int {
	return map[qa.Category]int{qa.CategoryCorrectness: correctness, qa.CategorySecurity: 25,
		qa.CategoryContract: 20, qa.CategoryExperience: 15, qa.CategoryObservability: 10}
}

// wantCode fails the test unless err carries code.
func wantCode(t *testing.T, err error, code core.ErrorCode) {
	t.Helper()
	if !core.HasCode(err, code) {
		t.Fatalf("error = %v, want code %s", err, code)
	}
}
