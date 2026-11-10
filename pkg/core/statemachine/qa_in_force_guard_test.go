package statemachine

import (
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
)

// A receipt releases a candidate only in the factory that issued it and only
// while the policy, the baseline and the suite it was judged under are still
// the ones in force.

func TestAReceiptIssuedUnderWhatIsNoLongerInForceDoesNotReleaseACandidate(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*QAProof){
		"the policy was revised after the receipt":     func(p *QAProof) { p.PolicyRevision = otherSHA },
		"the baseline moved on after the receipt":      func(p *QAProof) { p.BaselineSHA = otherSHA },
		"the suite changed after the receipt":          func(p *QAProof) { p.SuiteDigest = core.Digest(otherBuild) },
		"the receipt was issued under an older policy": func(p *QAProof) { p.Receipt.PolicySHA = otherSHA },
		"the receipt was judged against an older baseline": func(p *QAProof) {
			p.Receipt.BaselineSHA = otherSHA
		},
		"the receipt was judged with another suite": func(p *QAProof) {
			p.Receipt.SuiteDigest = core.Digest(otherBuild)
		},
		"the receipt was issued by another factory": func(p *QAProof) { p.Receipt.FactoryID = otherFactory },
		"the receipt names no factory":              func(p *QAProof) { p.Receipt.FactoryID = "" },
		"this machine serves another factory":       func(p *QAProof) { p.FactoryID = otherFactory },
	}
	for name, mutate := range cases {
		requireGuardFailed(t, name, checkQAWith(mutate), GuardQAPass)
	}
}

func TestAReceiptDoesNotPassWhenWhatIsInForceIsUnknown(t *testing.T) {
	t.Parallel()
	// The values in force are not optional: leaving one out, or naming it by
	// something that can move, never makes the comparison pass.
	cases := map[string]func(*QAProof){
		"the factory in force is not stated":  func(p *QAProof) { p.FactoryID = "" },
		"the policy in force is not stated":   func(p *QAProof) { p.PolicyRevision = "" },
		"the policy in force is a branch":     func(p *QAProof) { p.PolicyRevision = "main" },
		"the baseline in force is not stated": func(p *QAProof) { p.BaselineSHA = "" },
		"the baseline in force is a tag":      func(p *QAProof) { p.BaselineSHA = "v1.2.3" },
		"the suite in force is not stated":    func(p *QAProof) { p.SuiteDigest = "" },
		"the suite in force is not a digest":  func(p *QAProof) { p.SuiteDigest = "suite-latest" },
		"nothing in force is stated": func(p *QAProof) {
			p.FactoryID, p.PolicyRevision, p.BaselineSHA, p.SuiteDigest = "", "", "", ""
		},
		"the receipt and what is in force are equally unpinned": func(p *QAProof) {
			p.PolicyRevision, p.Receipt.PolicySHA = "main", "main"
		},
	}
	for name, mutate := range cases {
		requireGuardFailed(t, name, checkQAWith(mutate), GuardQAPass)
	}
}
