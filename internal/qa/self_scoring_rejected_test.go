package qa_test

import (
	"context"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/qa"
)

func TestTheCoderCannotScoreItsOwnCandidate(t *testing.T) {
	t.Parallel()
	for name, coder := range map[string]string{
		"same identity":            qaIdentity,
		"respelled in upper case":  "AGENT:QA-1",
		"padded with white space":  "  agent:qa-1 ",
		"respelled and padded too": " Agent:Qa-1\t",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			c := candidate()
			c.CodingAgentIdentity = coder
			result, err := e.build(t).Run(context.Background(), c)
			wantCode(t, err, core.CodePermissionDenied)
			if !result.Receipt.Signature.IsZero() || len(e.judge.Judged(issue)) != 0 {
				t.Fatal("a self-scoring evaluation must issue no receipt and ask no judge")
			}
		})
	}
}

func TestTheEvaluatorCannotWorkInTheCodersWorkspace(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	c := candidate()
	c.WorkspaceID = qaWorkspace
	_, err := e.build(t).Evaluate(context.Background(), c)
	wantCode(t, err, core.CodePermissionDenied)

	// A baseline mounted outside the evaluator's own workspace could be the
	// coder's checkout.
	e = newEnv(t)
	e.baselines.first.WorkspaceID = candidate().WorkspaceID
	_, err = e.build(t).Evaluate(context.Background(), candidate())
	wantCode(t, err, core.CodePermissionDenied)
	if len(e.judge.Judged(issue)) != 0 {
		t.Fatal("no judge may be asked from the coder's workspace")
	}
}

func TestACandidateCannotPinItsOwnJudgingCriteria(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*qa.Candidate){
		"review criteria at the candidate": func(c *qa.Candidate) { c.Policy.ReviewSHA = c.CommitSHA },
		"policy at the candidate":          func(c *qa.Candidate) { c.Policy.PolicySHA = c.CommitSHA },
		"baseline at the candidate":        func(c *qa.Candidate) { c.BaselineSHA = c.CommitSHA },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			c := candidate()
			mutate(&c)
			_, err := e.build(t).Evaluate(context.Background(), c)
			wantCode(t, err, core.CodePermissionDenied)
		})
	}
}

func TestAReceiptNamingTheCoderAsEvaluatorDoesNotVerify(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	receipt, err := e.build(t).Evaluate(context.Background(), candidate())
	if err != nil {
		t.Fatal(err)
	}
	forged := receipt
	forged.QAAgentIdentity = forged.CodingAgentIdentity
	if sig, err := e.signer.Sign(context.Background(), forged.SigningPayload()); err == nil {
		forged.Signature = sig
	}
	// Even signed by the trusted key, a receipt where the coder judged itself
	// is not a receipt.
	if err := forged.Validate(); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Fatalf("a self-scored receipt validated: %v", err)
	}
}
