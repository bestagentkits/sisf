package qa

import (
	"context"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/qa"
)

// Action is what the lifecycle does next with an evaluated candidate.
type Action string

const (
	// ActionReturnToDevelopment: QA failed; the run records qa_failed and,
	// within its attempts, the candidate goes back to development.
	ActionReturnToDevelopment Action = "return_to_development"
	// ActionRelease: QA passed and the candidate may be released.
	ActionRelease Action = "release"
	// ActionOwnerApproval: QA passed on the coder's own model route and the
	// policy requires the Owner's approval instead of self-publishing.
	ActionOwnerApproval Action = "owner_approval"
)

// Result is a signed receipt with the verdict of the rubric in force.
type Result struct {
	Receipt core.QAReceipt
	// Rubric is the rubric read from the review criteria at the pinned commit.
	Rubric qa.Rubric
	Passed bool
	// Refusal says why the candidate did not pass; nil when it did.
	Refusal error
	Action  Action
}

// Evaluate judges the candidate and returns its signed receipt. A receipt is
// returned whether or not the candidate passed; an error means no receipt.
func (e *Evaluator) Evaluate(ctx context.Context, c qa.Candidate) (core.QAReceipt, error) {
	result, err := e.Run(ctx, c)
	return result.Receipt, err
}

// Run judges the candidate, signs its receipt and decides what happens next.
//
// The candidate must be valid and its digest must be the finished build of
// its commit; the evaluator must be independent of the coder; the rubric is
// read from the review criteria at the pinned commit; the baseline must be
// the read-only one locked to the candidate's baseline and policy, and must
// not change while the candidate is judged. Every hard gate runs, and the
// judge scores every category; a failed gate fails the candidate whatever
// the score.
func (e *Evaluator) Run(ctx context.Context, c qa.Candidate) (Result, error) {
	if err := c.Validate(); err != nil {
		return Result{}, err
	}
	if err := e.checkBuilt(ctx, c); err != nil {
		return Result{}, err
	}
	if err := checkIndependence(e.cfg.Identity, e.cfg.Workspace, c); err != nil {
		return Result{}, err
	}
	route, err := chooseRoute(e.cfg.Routes, e.cfg.SameRouteAcknowledged, c)
	if err != nil {
		return Result{}, err
	}
	rubric, err := LoadRubric(ctx, e.cfg.Review, c.Policy.ReviewSHA)
	if err != nil {
		return Result{}, err
	}
	baseline, err := e.lockedBaseline(ctx, c)
	if err != nil {
		return Result{}, err
	}
	checks := e.runGates(ctx, c, route)
	card, err := e.cfg.Judge.Score(ctx, qa.JudgeRequest{
		Candidate: c, Baseline: baseline, Identity: e.cfg.Identity, ModelRoute: route.route, Rubric: rubric,
	})
	if err != nil {
		return Result{}, err
	}
	if err := card.Validate(rubric); err != nil {
		return Result{}, core.NewError(core.CodeInvalidArgument, "the judge returned a scorecard outside the rubric").WithCause(err)
	}
	after, err := e.cfg.Baselines.Baseline(ctx, c)
	if err != nil {
		return Result{}, err
	}
	if after != baseline {
		return Result{}, core.NewError(core.CodeTampered, "the QA baseline changed while the candidate was judged")
	}
	receipt, err := e.issue(ctx, c, route, append(checks, rubricChecks(card)...))
	if err != nil {
		return Result{}, err
	}
	return e.decide(receipt, rubric, route), nil
}

// checkBuilt refuses a candidate whose digest is not the finished build of
// its commit.
func (e *Evaluator) checkBuilt(ctx context.Context, c qa.Candidate) error {
	built, err := e.cfg.Builds.FinishedBuild(ctx, c.CommitSHA)
	switch {
	case core.HasCode(err, core.CodeNotFound):
		return core.NewError(core.CodeConflict, "the candidate has not finished building, so it cannot be scored").WithCause(err)
	case err != nil:
		return err
	case built != c.Digest:
		return core.NewError(core.CodeTampered, "the candidate's digest is not the finished build of its commit")
	}
	return nil
}

// lockedBaseline returns the baseline after checking that it is read-only
// and locked to the candidate's baseline commit, policy and suite, in the
// evaluator's own workspace.
func (e *Evaluator) lockedBaseline(ctx context.Context, c qa.Candidate) (qa.Baseline, error) {
	b, err := e.cfg.Baselines.Baseline(ctx, c)
	if err != nil {
		return qa.Baseline{}, err
	}
	if err := b.Validate(); err != nil {
		return qa.Baseline{}, denied("the QA baseline is not a read-only pinned checkout: %v", err)
	}
	switch {
	case b.CommitSHA != c.BaselineSHA || b.PolicySHA != c.Policy.PolicySHA:
		return qa.Baseline{}, denied("the QA baseline is not locked to the candidate's baseline commit and policy")
	case b.SuiteDigest != c.SuiteDigest:
		return qa.Baseline{}, denied("the QA baseline holds another suite than the candidate names")
	case b.WorkspaceID != e.cfg.Workspace:
		// The evaluator's workspace already differs from the coder's, so a
		// baseline mounted anywhere else could be the coder's.
		return qa.Baseline{}, denied("the QA baseline is not mounted in the evaluator's own workspace")
	}
	return b, nil
}

// rubricChecks records the scorecard, one check per category in the order of
// the shared rubric.
func rubricChecks(card qa.Scorecard) []core.QACheck {
	var checks []core.QACheck
	for _, category := range core.RubricCategories() {
		name := qa.Category(category.Name)
		checks = append(checks, core.QACheck{
			Name: category.Name, Kind: core.CheckRubric, Passed: true,
			Points: card.Points[name], MaxPoints: category.MaxPoints,
			Detail: card.Details[name], EvidenceRef: card.EvidenceRef,
		})
	}
	return checks
}

// decide applies the shared pass rule and the policy for a shared route.
func (e *Evaluator) decide(receipt core.QAReceipt, rubric qa.Rubric, route routeChoice) Result {
	result := Result{Receipt: receipt, Rubric: rubric}
	if result.Refusal = receipt.CheckPass(rubric.PassScore); result.Refusal != nil {
		result.Action = ActionReturnToDevelopment
		return result
	}
	result.Passed, result.Action = true, ActionRelease
	if route.same && e.cfg.SameRouteAction == SameRouteOwnerApproval {
		result.Action = ActionOwnerApproval
	}
	return result
}
