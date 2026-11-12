//go:build smoke

package smoke

import (
	"testing"

	"github.com/bestagentkits/sisf/internal/intake"
	"github.com/bestagentkits/sisf/internal/policy"
	"github.com/bestagentkits/sisf/pkg/decision"
	"github.com/bestagentkits/sisf/pkg/github"
)

// sandboxCatalog is the feature catalogue of the sandbox's FEATURES.md, as
// seeded: the names intake compares an issue's affected features with.
var sandboxCatalog = policy.Catalog{Features: []policy.Feature{
	{Name: "Visit page", Status: "built"},
	{Name: "Health check", Status: "built"},
	{Name: "Metrics", Status: "built"},
	{Name: "Revision reporting", Status: "built"},
	{Name: "Fault injection", Status: "built"},
	{Name: "Synthetic checks", Status: "built"},
}}

// The run's issue, opened by the operator, who can write to the sandbox,
// goes straight to Ready through the real intake analysis: nothing to ask,
// and nothing for the Owner to approve. Otherwise the run would stall in
// Clarifying or ApprovalPending, which no deadline of the run waits out.
func TestTheRunsIssueNeedsNoClarificationAndNoOwnerApproval(t *testing.T) {
	t.Parallel()
	var labels []github.Label
	for _, name := range issueLabels {
		labels = append(labels, github.Label{Name: name})
	}
	read := intake.ReadLabels(labels, intake.LabelRules{})
	if !read.Ready || read.Kind != decision.KindExistingImprovement {
		t.Fatalf("the labels read as %+v, want a ready improvement", read)
	}
	issue := github.Issue{Title: issueTitle, Body: issueBody, Labels: labels,
		Author: github.User{ID: 1, Login: "operator"}, AuthorAssociation: github.AssociationMember}
	draft := intake.Analyze(issue, nil, read)
	if !draft.Complete() {
		t.Fatalf("intake would ask for %v; draft %+v", draft.Missing, draft)
	}
	if len(draft.Features) != 1 || draft.Features[0] != sandboxFeature || len(draft.DoD) != 3 {
		t.Fatalf("the draft read features %q and Definition of Done %q", draft.Features, draft.DoD)
	}
	for _, association := range []github.AuthorAssociation{github.AssociationOwner, github.AssociationMember, github.AssociationCollaborator} {
		verdict := intake.Classify(draft, association, sandboxCatalog)
		if verdict.NeedsApproval || verdict.Kind != decision.KindExistingImprovement {
			t.Errorf("an issue by a %s needs the Owner: %+v", association, verdict)
		}
	}
}
