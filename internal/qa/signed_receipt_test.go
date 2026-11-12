package qa_test

import (
	"context"
	"strings"
	"testing"
	"time"

	evaluator "github.com/bestagentkits/sisf/internal/qa"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/qa"
)

func TestTheReceiptCarriesEveryFieldAndIsSignedByTheDelegatedKey(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.cfg.ReceiptTTL = 2 * time.Hour
	c := candidate()
	c.Attempt = 2
	r, err := e.build(t).Evaluate(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Validate(); err != nil {
		t.Fatalf("receipt incomplete: %v", err)
	}
	switch {
	case r.FactoryID != factory || r.IssueID != issue || r.RunID != c.RunID:
		t.Errorf("receipt not bound to the factory, issue and run: %+v", r)
	case r.CandidateSHA != candidateSHA || r.CandidateDigest != buildDigest:
		t.Errorf("receipt not bound to the candidate: %s %s", r.CandidateSHA, r.CandidateDigest)
	case r.SuiteDigest != suiteDigest || r.BaselineSHA != baselineSHA || r.PolicySHA != policySHA:
		t.Errorf("receipt not bound to the suite, baseline and policy: %+v", r)
	case r.QAAgentIdentity != qaIdentity || r.CodingAgentIdentity != c.CodingAgentIdentity:
		t.Errorf("receipt does not name both agents: %q %q", r.QAAgentIdentity, r.CodingAgentIdentity)
	case r.QAModelRoute != qaRoute || r.CodingModelRoute != coderRoute || r.SameModelRoute:
		t.Errorf("receipt does not name both routes: %+v", r)
	case !r.IssuedAt.Equal(start) || !r.ExpiresAt.Equal(start.Add(2*time.Hour)) || r.EvaluatorAttempt != 2:
		t.Errorf("timestamp %s, expiry %s, attempt %d", r.IssuedAt, r.ExpiresAt, r.EvaluatorAttempt)
	case r.Score != 100 || len(r.Checks) != len(qa.Gates())+len(core.RubricCategories()):
		t.Errorf("score %d with %d checks", r.Score, len(r.Checks))
	case r.Signature.KeyID != e.signer.KeyID():
		t.Errorf("signed by %q, not the delegated key", r.Signature.KeyID)
	}
	if err := evaluator.VerifyReceipt(context.Background(), e.verifier, r, factory, c, e.clock.Now()); err != nil {
		t.Fatalf("a fresh receipt does not verify: %v", err)
	}
}

func TestAReceiptIsNotBorrowedByAnotherCandidate(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	r, err := e.build(t).Evaluate(context.Background(), candidate())
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*qa.Candidate){
		"another commit":      func(c *qa.Candidate) { c.CommitSHA = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee" },
		"another build":       func(c *qa.Candidate) { c.Digest = otherDigest },
		"another run":         func(c *qa.Candidate) { c.RunID = "run-0002" },
		"another issue":       func(c *qa.Candidate) { c.IssueID.Number = 8 },
		"another suite":       func(c *qa.Candidate) { c.SuiteDigest = otherDigest },
		"another baseline":    func(c *qa.Candidate) { c.BaselineSHA = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee" },
		"another policy":      func(c *qa.Candidate) { c.Policy.PolicySHA = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee" },
		"another coder":       func(c *qa.Candidate) { c.CodingAgentIdentity = "agent:coder-2" },
		"another attempt":     func(c *qa.Candidate) { c.Attempt = 2 },
		"another coder route": func(c *qa.Candidate) { c.CodingModelRoute.Effort = "high" },
	}
	for name, mutate := range cases {
		c := candidate()
		mutate(&c)
		err := evaluator.VerifyReceipt(context.Background(), e.verifier, r, factory, c, e.clock.Now())
		if !core.HasCode(err, core.CodeGuardFailed) {
			t.Errorf("%s: want %s, got %v", name, core.CodeGuardFailed, err)
		}
	}
	err = evaluator.VerifyReceipt(context.Background(), e.verifier, r, "other-factory", candidate(), e.clock.Now())
	wantCode(t, err, core.CodeGuardFailed)
}

func TestAReceiptExpires(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	r, err := e.build(t).Evaluate(context.Background(), candidate())
	if err != nil {
		t.Fatal(err)
	}
	verify := func(at time.Time) error {
		return evaluator.VerifyReceipt(context.Background(), e.verifier, r, factory, candidate(), at)
	}
	if err := verify(start.Add(evaluator.DefaultReceiptTTL - time.Second)); err != nil {
		t.Fatalf("a receipt within its lifetime was refused: %v", err)
	}
	wantCode(t, verify(start.Add(evaluator.DefaultReceiptTTL)), core.CodeExpired)
	wantCode(t, verify(start.Add(-time.Second)), core.CodeGuardFailed)
}

func TestAnAlteredOrUnsignedReceiptIsRefused(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	r, err := e.build(t).Evaluate(context.Background(), candidate())
	if err != nil {
		t.Fatal(err)
	}
	verify := func(r core.QAReceipt) error {
		return evaluator.VerifyReceipt(context.Background(), e.verifier, r, factory, candidate(), e.clock.Now())
	}
	raised := r
	raised.Checks = append([]core.QACheck(nil), r.Checks...)
	raised.Checks[0].Passed, raised.Checks[0].Detail = true, "overridden"
	raised.ExpiresAt = r.ExpiresAt.Add(time.Hour)
	wantCode(t, verify(raised), core.CodeSignatureInvalid)
	unsigned := r
	unsigned.Signature = core.Signature{}
	wantCode(t, verify(unsigned), core.CodeInvalidArgument)
	if err := evaluator.VerifyReceipt(context.Background(), nil, r, factory, candidate(), e.clock.Now()); err == nil {
		t.Fatal("a receipt verified without a verifier")
	}
}

func TestARevokedKeySignsNoReceiptAndItsReceiptsStopVerifying(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ev := e.build(t)
	r, err := ev.Evaluate(context.Background(), candidate())
	if err != nil {
		t.Fatal(err)
	}
	e.revoke(t)
	err = evaluator.VerifyReceipt(context.Background(), e.verifier, r, factory, candidate(), e.clock.Now())
	wantCode(t, err, core.CodeSignatureInvalid)
	if _, err := ev.Evaluate(context.Background(), candidate()); err == nil {
		t.Fatal("a revoked key signed a receipt")
	}
}

func TestAnExpiredKeySignsNoReceipt(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ev := e.build(t)
	e.clock.Advance(8 * 24 * time.Hour)
	if _, err := ev.Evaluate(context.Background(), candidate()); err == nil {
		t.Fatal("an expired delegated key signed a receipt")
	}
}

func TestTheFailedEvaluationKeyIsTheAgentZones(t *testing.T) {
	t.Parallel()
	key := evaluator.QAFailedKey("run-0001", 2)
	if key != "qa/run-0001/2" || !strings.HasPrefix(key, "qa/") {
		t.Fatalf("QAFailedKey = %q", key)
	}
	if evaluator.QAFailedKey("run-0001", 1) == key {
		t.Fatal("two attempts share one key")
	}
}
