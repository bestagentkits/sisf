package delegation

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"sync"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/signing"
	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/deploy"
)

const testFactory core.FactoryID = "acme-hello"

// start is the instant every fake clock begins at.
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
	ch := make(chan time.Time, 1)
	ch <- c.Advance(d)
	return ch
}

// Set moves the clock to t.
func (c *fakeClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

// Advance moves the clock forward by d and returns the new instant.
func (c *fakeClock) Advance(d time.Duration) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	return c.now
}

// newRoot returns a root signer with a key generated for this test.
func newRoot(t *testing.T) (*signing.Signer, ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate root key: %v", err)
	}
	signer, err := signing.NewSigner(priv)
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	return signer, pub
}

// chain is one factory's root key, verifier and clock.
type chain struct {
	root     *signing.Signer
	rootPub  ed25519.PublicKey
	clock    *fakeClock
	verifier *ChainVerifier
}

func newChain(t *testing.T) *chain {
	t.Helper()
	root, pub := newRoot(t)
	clock := newFakeClock()
	v, err := NewChainVerifier(VerifierConfig{FactoryID: testFactory, Root: pub, Clock: clock})
	if err != nil {
		t.Fatalf("NewChainVerifier: %v", err)
	}
	return &chain{root: root, rootPub: pub, clock: clock, verifier: v}
}

// issue issues a key valid from the clock's now for a day.
func (c *chain) issue(t *testing.T, purpose Purpose, holder Holder) IssuedKey {
	t.Helper()
	key, err := Issue(context.Background(), c.root,
		IssueRequest{FactoryID: testFactory, Purpose: purpose, Holder: holder, NotBefore: c.clock.Now(), Lifetime: 24 * time.Hour}, nil)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	return key
}

// load loads an issued key as its own holder.
func (c *chain) load(t *testing.T, key IssuedKey) *DelegatedSigner {
	t.Helper()
	s, err := Load(context.Background(), c.verifier,
		LoadRequest{Holder: key.Delegation.Delegation.Holder, Delegation: key.Delegation, PrivateKey: key.PrivateKey})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return s
}

// revoke applies a root-signed revocation list.
func (c *chain) revoke(t *testing.T, sequence int64, keyIDs ...string) {
	t.Helper()
	list, err := SignRevocations(context.Background(), c.root,
		RevocationList{FactoryID: testFactory, Sequence: sequence, IssuedAt: c.clock.Now(), KeyIDs: keyIDs})
	if err != nil {
		t.Fatalf("SignRevocations: %v", err)
	}
	if err := c.verifier.ApplyRevocations(context.Background(), list); err != nil {
		t.Fatalf("ApplyRevocations: %v", err)
	}
}

// Signing payloads of each record type a purpose covers.
func receiptPayload() []byte {
	return core.QAReceipt{FactoryID: testFactory, Score: 90}.SigningPayload()
}
func approvalPayload() []byte {
	return approvals.Approval{FactoryID: testFactory, Action: "merge"}.SigningPayload()
}
func artifactPayload() []byte {
	return deploy.Artifact{FactoryID: testFactory, Unit: "app"}.SigningPayload()
}

// wantCode fails the test unless err carries code.
func wantCode(t *testing.T, err error, code core.ErrorCode) {
	t.Helper()
	if !core.HasCode(err, code) {
		t.Fatalf("error = %v, want code %s", err, code)
	}
}
