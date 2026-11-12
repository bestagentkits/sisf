package intake

import (
	"strings"

	"github.com/bestagentkits/sisf/internal/policy"
	"github.com/bestagentkits/sisf/pkg/decision"
	"github.com/bestagentkits/sisf/pkg/github"
)

// Reason says why a scope needs the Owner before any work starts.
type Reason string

const (
	// ReasonNewFeature: the work adds functionality nobody approved.
	ReasonNewFeature Reason = "new_feature"
	// ReasonPolicyChange: the work touches the rules the factory follows.
	ReasonPolicyChange Reason = "policy_change"
	// ReasonUnlistedFeature: the work names a feature the approved
	// catalogue does not list.
	ReasonUnlistedFeature Reason = "feature_not_in_catalogue"
	// ReasonRequesterNotAuthorized: the request comes from someone without
	// write access to the repository.
	ReasonRequesterNotAuthorized Reason = "requester_not_authorized"
)

// Classification is the verdict on a drafted scope.
type Classification struct {
	// Kind is the kind of work. It is the label's kind or a more cautious
	// one; classification never makes work look smaller than its label.
	Kind decision.Kind
	// NeedsApproval says the scope waits for the Owner.
	NeedsApproval bool
	Reasons       []Reason
}

// protectedPaths are the files and directories that hold the rules. Work
// that mentions one of them is treated as a change of the rules.
var protectedPaths = []string{
	strings.ToLower(policy.PathAgents), strings.ToLower(policy.PathReview), strings.ToLower(policy.PathDecisions),
	strings.ToLower(policy.PathFeatures), policy.PathFeaturesYAML, ".sisf/", "docs/adr/",
}

// Classify decides whether a drafted scope is inside what agents may do on
// their own. Each rule can only add caution:
//
//   - work that names a file holding the rules is a policy change;
//   - a fix or an improvement of something the approved catalogue does not
//     list is a new feature;
//   - a new feature, a policy change and any kind this code does not know
//     need the Owner;
//   - so does every request from someone without write access to the
//     repository, whatever its kind.
//
// The text of the request is searched and compared here, nothing more. It
// cannot lower the kind, name its own approver or waive a rule.
func Classify(draft Draft, author github.AuthorAssociation, catalog policy.Catalog) Classification {
	c := Classification{Kind: draft.Kind}
	if c.Kind == decision.KindBug || c.Kind == decision.KindExistingImprovement {
		for _, feature := range draft.Features {
			if !catalog.Has(feature) {
				c.Kind = decision.KindNewFeature
				c.Reasons = append(c.Reasons, ReasonUnlistedFeature)
				break
			}
		}
	}
	if mentionsRules(draft) {
		c.Kind = decision.KindPolicyChange
		c.Reasons = append(c.Reasons, ReasonPolicyChange)
	}
	if c.Kind.RequiresOwnerApproval() && len(c.Reasons) == 0 {
		c.Reasons = append(c.Reasons, ReasonNewFeature)
	}
	if !author.IsCollaborator() {
		c.Reasons = append(c.Reasons, ReasonRequesterNotAuthorized)
	}
	c.NeedsApproval = len(c.Reasons) > 0
	return c
}

// mentionsRules reports whether any part of the draft names a protected
// path.
func mentionsRules(draft Draft) bool {
	text := strings.ToLower(strings.Join(append([]string{draft.Title, draft.Scope}, draft.DoD...), "\n"))
	for _, path := range protectedPaths {
		if strings.Contains(text, path) {
			return true
		}
	}
	return false
}

// riskSummary is the one sentence an approver reads first.
func riskSummary(c Classification) string {
	var parts []string
	for _, reason := range c.Reasons {
		switch reason {
		case ReasonNewFeature:
			parts = append(parts, "adds a feature the Owner has not approved")
		case ReasonUnlistedFeature:
			parts = append(parts, "works on something the approved feature catalogue does not list")
		case ReasonPolicyChange:
			parts = append(parts, "touches the rules the factory follows")
		case ReasonRequesterNotAuthorized:
			parts = append(parts, "was requested by someone without write access to the repository")
		}
	}
	if len(parts) == 0 {
		// The scope was edited while it waited: nothing in the new text needs
		// the Owner by itself, but an issue that entered the approval inbox
		// leaves it only through a decision.
		return "This scope was changed while it waited for approval."
	}
	return "This scope " + strings.Join(parts, " and ") + "."
}
