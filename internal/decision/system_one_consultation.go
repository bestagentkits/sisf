package decision

import (
	"context"

	"github.com/bestagentkits/sisf/pkg/core"
	pkgdecision "github.com/bestagentkits/sisf/pkg/decision"
)

// Reasons an outcome carries after System One was consulted.
const (
	// ReasonSystemOneFailed: no usable judgment came back, so nothing is
	// proposed and the condition is held for more evidence.
	ReasonSystemOneFailed = "system_one_failed"
	// ReasonSystemOneCaution: System One chose a verdict that creates no
	// work, which downgrades the proposal.
	ReasonSystemOneCaution = "system_one_caution"
	// ReasonSystemOneKindDoubt: System One classified the work differently
	// from the rules; the kind decides scope, so the condition is
	// investigated instead of proposed.
	ReasonSystemOneKindDoubt = "system_one_kind_doubt"
)

// checkSystemOne requires typed questions read at the pinned decision
// commit whenever a System One is configured.
func checkSystemOne(cfg Config) error {
	if cfg.SystemOne == nil {
		return nil
	}
	q := cfg.Questions
	if len(q.Questions) == 0 || q.SchemaVersion < 1 || q.Revision != cfg.Policy.DecisionSHA {
		return hold("unapproved", "system one needs the typed questions of DECISIONS.md read at the pinned decision commit")
	}
	return nil
}

// consult asks System One about a proposal the rules made and applies only
// what raises caution or priority. The rules already vetoed everything else:
// System One is asked only about a proposal, so it can never create one.
// Any failure fails closed: o is held for more evidence and an error is
// returned, so the proposal is offered again on the next run.
func (l *Layer) consult(ctx context.Context, o *Outcome, p *pkgdecision.Proposal) error {
	q := l.cfg.Questions
	req := pkgdecision.Request{Evidence: p.Evidence, Goal: p.Goal, Questions: q.Questions, Policy: p.Policy, SchemaVersion: q.SchemaVersion}
	j, err := l.cfg.SystemOne.Judge(ctx, req)
	if err == nil {
		// The adapter is checked again here: any implementation may sit
		// behind the interface.
		err = j.Validate(req)
	}
	if err == nil && !consistent(j) {
		err = invalid("system one answered the typed questions differently from its typed fields")
	}
	if err != nil {
		o.Verdict, o.Reason, o.Proposal = pkgdecision.VerdictCollectMoreEvidence, ReasonSystemOneFailed, nil
		return core.NewError(core.CodeUnavailable, "system one gave no usable judgment, so nothing was proposed").WithCause(err)
	}
	o.Model, o.Uncertainties = j.Model, append([]string(nil), j.Uncertainties...)
	switch {
	case !j.Verdict.CreatesWork():
		o.Verdict, o.Reason, o.Proposal = j.Verdict, ReasonSystemOneCaution, nil
	case j.Kind != p.Kind:
		o.Verdict, o.Reason, o.Proposal = pkgdecision.VerdictInvestigate, ReasonSystemOneKindDoubt, nil
	default:
		// implement is never more than propose here: the layer only proposes.
		if j.Priority.Rank() > p.Priority.Rank() {
			p.Priority = j.Priority
		}
		p.Policy.ModelRoute = j.Model
	}
	return nil
}

// consistent reports whether the answers to the verdict, kind and priority
// questions match the judgment's typed fields.
func consistent(j pkgdecision.Judgment) bool {
	for _, a := range j.Answers {
		switch a.QuestionID {
		case SystemOneVerdict:
			if a.Value != string(j.Verdict) {
				return false
			}
		case SystemOneKind:
			if j.Kind != "" && a.Value != string(j.Kind) {
				return false
			}
		case SystemOnePriority:
			if a.Value != string(j.Priority) {
				return false
			}
		}
	}
	return true
}
