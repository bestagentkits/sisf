package approvals

import "github.com/bestagentkits/sisf/pkg/core"

// StepUpConfirmed reports whether a, a decision on r, carries the identity
// confirmation r asks for. A request that is not marked StepUp needs none.
// One that is needs a person who confirmed their identity for this decision:
// after the request was made and no later than the decision. A decision that
// came through WebMCP never counts, since there an agent in the page acts
// with the person's ambient session, and an agent or a service never
// confirms anything.
//
// The rule does not judge how recent the confirmation is; the store that
// records the decision holds it to its step-up age as well.
func StepUpConfirmed(r Request, a Approval) bool {
	if !r.StepUp {
		return true
	}
	return a.ActorKind == core.ActorHuman && a.Surface != core.SurfaceWebMCP && !a.StepUpAt.IsZero() &&
		!a.StepUpAt.Before(r.CreatedAt) && !a.StepUpAt.After(a.DecidedAt)
}
