package supplychain

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"sync"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/signing"
	"github.com/bestagentkits/sisf/internal/signing/delegation"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/qa"
)

const testFactory core.FactoryID = "acme-hello"

// Fixture hashes. They only need to be well formed.
const (
	imageDigest core.Digest    = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	otherDigest core.Digest    = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	baseSHA     core.CommitSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	headSHA     core.CommitSHA = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	policySHA   core.CommitSHA = "cccccccccccccccccccccccccccccccccccccccc"
)

var start = time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)

// fakeClock is a core.Clock moved only by the test.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{now: start} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	ch := make(chan time.Time, 1)
	ch <- c.now
	return ch
}

func (c *fakeClock) Advance(d time.Duration) { <-c.After(d) }

// trust is one factory's root key and chain verifier, with keys generated
// for the test.
type trust struct {
	root     *signing.Signer
	clock    *fakeClock
	verifier *delegation.ChainVerifier
}

func newTrust(t *testing.T) *trust {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate root key: %v", err)
	}
	root, err := signing.NewSigner(priv)
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	clock := newFakeClock()
	v, err := delegation.NewChainVerifier(delegation.VerifierConfig{FactoryID: testFactory, Root: pub, Clock: clock})
	if err != nil {
		t.Fatalf("NewChainVerifier: %v", err)
	}
	return &trust{root: root, clock: clock, verifier: v}
}

// signer issues and loads a delegated key for purpose.
func (tr *trust) signer(t *testing.T, purpose delegation.Purpose, holder delegation.Holder) *delegation.DelegatedSigner {
	t.Helper()
	ctx := context.Background()
	key, err := delegation.Issue(ctx, tr.root, delegation.IssueRequest{
		FactoryID: testFactory, Purpose: purpose, Holder: holder, NotBefore: tr.clock.Now(), Lifetime: 24 * time.Hour,
	}, nil)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	s, err := delegation.Load(ctx, tr.verifier, delegation.LoadRequest{Holder: holder, Delegation: key.Delegation, PrivateKey: key.PrivateKey})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return s
}

// candidate is a built candidate moving from baseSHA to headSHA.
func candidate() qa.Candidate {
	return qa.Candidate{
		IssueID: core.IssueRef{Owner: "acme", Repo: "app", Number: 7}, RunID: "run-0001",
		CommitSHA: headSHA, Digest: imageDigest, BaselineSHA: baseSHA, SuiteDigest: otherDigest,
		Policy: core.PolicyRefs{PolicySHA: policySHA, ReviewSHA: policySHA, DecisionSHA: policySHA, FeatureCatalogSHA: policySHA,
			ModelRoute: core.ModelRoute{Provider: "openai", Model: "mid"}},
		CodingAgentIdentity: "agent:coder-1",
		CodingModelRoute:    core.ModelRoute{Provider: "anthropic", Model: "mid"},
		Attempt:             1,
	}
}

func wantCode(t *testing.T, err error, code core.ErrorCode) {
	t.Helper()
	if !core.HasCode(err, code) {
		t.Fatalf("error = %v, want code %s", err, code)
	}
}
