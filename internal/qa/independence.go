package qa

import (
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/qa"
)

// SameRouteAction is what the policy requires of a passing receipt whose QA
// ran on the coder's provider and model, because the deployment has one
// model route only.
type SameRouteAction string

const (
	// SameRouteOwnerApproval sends the candidate to the Owner for approval
	// instead of self-publishing. It is the default.
	SameRouteOwnerApproval SameRouteAction = "owner_approval"
	// SameRouteSelfPublish lets the candidate be released as any other,
	// with same_model_route disclosed on the receipt.
	SameRouteSelfPublish SameRouteAction = "self_publish"
)

// Valid reports whether a is a known action.
func (a SameRouteAction) Valid() bool {
	return a == SameRouteOwnerApproval || a == SameRouteSelfPublish
}

// routeChoice is the model route QA runs on and whether it is the coder's.
type routeChoice struct {
	route core.ModelRoute
	same  bool
}

// checkIndependence refuses an evaluation whose evaluator is the coder, or
// that would run in the coder's workspace, or under criteria pinned to the
// candidate's own commit. Each is PERMISSION_DENIED: the agent that wrote a
// candidate never judges it, nor chooses what it is judged by.
func checkIndependence(identity, workspace string, c qa.Candidate) error {
	switch {
	case core.SameIdentity(identity, c.CodingAgentIdentity):
		return denied("agent %q cannot evaluate its own candidate", strings.TrimSpace(identity))
	case c.WorkspaceID != "" && strings.TrimSpace(workspace) == strings.TrimSpace(c.WorkspaceID):
		return denied("the evaluator cannot work in the coder's workspace %q", c.WorkspaceID)
	}
	for _, pinned := range []core.CommitSHA{c.BaselineSHA, c.Policy.PolicySHA, c.Policy.ReviewSHA} {
		if pinned == c.CommitSHA {
			return denied("the candidate's own commit cannot be its baseline, policy or review criteria")
		}
	}
	return nil
}

// chooseRoute picks the model route QA runs on. When the deployment has two
// or more distinct routes (by provider and model), QA must not share the
// coder's: the pinned QA route is kept when it differs, and otherwise the
// first configured route that differs is taken. With a single route QA runs
// on it and the receipt discloses same_model_route, which the Owner must
// have acknowledged; without that acknowledgement QA is on POLICY_HOLD.
func chooseRoute(routes []core.ModelRoute, acknowledged bool, c qa.Candidate) (routeChoice, error) {
	preferred, coder := c.Policy.ModelRoute, c.CodingModelRoute
	if !preferred.SameModel(coder) {
		return routeChoice{route: preferred}, nil
	}
	for _, route := range routes {
		if !route.SameModel(coder) {
			return routeChoice{route: route}, nil
		}
	}
	if !acknowledged {
		return routeChoice{}, core.NewError(core.CodePolicyHold,
			"the deployment has one model route and the Owner has not acknowledged that QA shares it with the coder")
	}
	return routeChoice{route: preferred, same: true}, nil
}

// denied builds the PERMISSION_DENIED error of this package.
func denied(format string, args ...any) *core.Error {
	return core.NewError(core.CodePermissionDenied, sprintf(format, args...))
}
