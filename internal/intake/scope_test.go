package intake_test

import (
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/intake"
	"github.com/bestagentkits/sisf/internal/policy"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/decision"
	"github.com/bestagentkits/sisf/pkg/github"
)

// digestOf is the scope digest of an issue with body, read as a bug.
func digestOf(body string) core.Digest {
	draft := intake.Analyze(issueBy(member, body), nil, bug)
	return intake.ScopeDigest(issueRef, draft.Kind, draft)
}

func TestReformattingARequestDoesNotChangeItsDigest(t *testing.T) {
	t.Parallel()
	want := digestOf(completeBody)
	if err := want.Validate(); err != nil {
		t.Fatalf("the scope digest is not a full digest: %v", err)
	}
	same := map[string]string{
		"trailing spaces and blank lines": "The health endpoint returns 500 when the database is slow.   \n\n\n\n" +
			"### Definition of Done\n\n- /healthz answers 200 within one second while the database is slow\n\n- a regression test covers it\n\n\n",
		"another list style": "The health endpoint returns 500 when the database is slow.\n\n" +
			"## Definition of Done\n1. /healthz answers 200 within one second while the database is slow\n2. a regression   test covers it\n",
		"ticked checkboxes": "The health endpoint returns 500 when the database is slow.\n\n" +
			"### Acceptance criteria\n- [x] /healthz answers 200 within one second while the database is slow\n- [ ] a regression test covers it\n",
		"windows line endings": strings.ReplaceAll(completeBody, "\n", "\r\n"),
	}
	for name, body := range same {
		if got := digestOf(body); got != want {
			t.Errorf("%s changed the digest", name)
		}
	}
}

func TestEveryEditThatMattersChangesTheDigest(t *testing.T) {
	t.Parallel()
	want := digestOf(completeBody)
	edits := map[string]string{
		"a word in the scope":  strings.Replace(completeBody, "returns 500", "returns 503", 1),
		"more scope":           "Also drop the users table.\n" + completeBody,
		"a criterion loosened": strings.Replace(completeBody, "within one second", "within one minute", 1),
		"a criterion removed":  strings.Replace(completeBody, "- a regression test covers it\n", "", 1),
		"a criterion added":    completeBody + "- deploys to production\n",
		"criteria reordered": "The health endpoint returns 500 when the database is slow.\n\n### Definition of Done\n" +
			"- a regression test covers it\n- /healthz answers 200 within one second while the database is slow\n",
		"a feature named": completeBody + "\n### Affected features\n- Dashboard\n",
	}
	for name, body := range edits {
		if got := digestOf(body); got == want {
			t.Errorf("%s left the digest unchanged", name)
		}
	}
	draft := intake.Analyze(issueBy(member, completeBody), nil, bug)
	retitled := draft
	retitled.Title = "Something else entirely"
	other := issueRef
	other.Number++
	for name, got := range map[string]core.Digest{
		"another title":      intake.ScopeDigest(issueRef, draft.Kind, retitled),
		"another kind":       intake.ScopeDigest(issueRef, decision.KindNewFeature, draft),
		"another issue":      intake.ScopeDigest(other, draft.Kind, draft),
		"text moved between": intake.ScopeDigest(issueRef, draft.Kind, intake.Draft{Kind: draft.Kind, Title: draft.Title, Scope: draft.Scope + "\n" + draft.DoD[0], DoD: draft.DoD[1:]}),
	} {
		if got == want {
			t.Errorf("%s left the digest unchanged", name)
		}
	}
}

func TestTheScopeTextShowsWhatWasAgreedAndShortensWhatIsTooLong(t *testing.T) {
	t.Parallel()
	sow := intake.SoW{Issue: issueRef, Version: 2, Kind: decision.KindBug, Title: "Health check fails",
		Scope: "It returns 500.", DoD: []string{"answers 200", "has a test"}, Features: []string{"Health endpoint"}}
	text := sow.Text()
	for _, part := range []string{"Title: Health check fails", "Kind: bug", "It returns 500.", "1. answers 200", "2. has a test", "Affected features: Health endpoint"} {
		if !strings.Contains(text, part) {
			t.Errorf("the scope text lacks %q:\n%s", part, text)
		}
	}
	// A multi-byte character at the cut is not split.
	sow.Scope = strings.Repeat("é", 6000)
	long := sow.Text()
	if len(long) > 9000 || !strings.Contains(long, "the digest covers the full text") || strings.ContainsRune(long, '\uFFFD') {
		t.Fatalf("a long scope was not shortened cleanly (%d bytes)", len(long))
	}
}

func TestClassificationOnlyAddsCaution(t *testing.T) {
	t.Parallel()
	catalog := policy.Catalog{Features: []policy.Feature{{Name: "Health endpoint"}, {Name: "Dashboard"}}}
	draft := func(kind decision.Kind, scope string, features ...string) intake.Draft {
		return intake.Draft{Kind: kind, Title: "A request", Scope: scope, DoD: []string{"done"}, Features: features}
	}
	cases := map[string]struct {
		draft   intake.Draft
		author  github.AuthorAssociation
		kind    decision.Kind
		reasons []intake.Reason
	}{
		"a fix by a maintainer": {draft(decision.KindBug, "It crashes."), github.AssociationMember, decision.KindBug, nil},
		"an improvement of a listed feature": {draft(decision.KindExistingImprovement, "Faster.", "dashboard"),
			github.AssociationCollaborator, decision.KindExistingImprovement, nil},
		"a new feature": {draft(decision.KindNewFeature, "Add export."), github.AssociationOwner,
			decision.KindNewFeature, []intake.Reason{intake.ReasonNewFeature}},
		"a fix of something unlisted": {draft(decision.KindBug, "It crashes.", "Marketplace"), github.AssociationMember,
			decision.KindNewFeature, []intake.Reason{intake.ReasonUnlistedFeature}},
		"one listed and one unlisted feature": {draft(decision.KindExistingImprovement, "Faster.", "Dashboard", "Billing"),
			github.AssociationMember, decision.KindNewFeature, []intake.Reason{intake.ReasonUnlistedFeature}},
		"a change of the review rules": {draft(decision.KindBug, "Lower the pass score in review.md."), github.AssociationMember,
			decision.KindPolicyChange, []intake.Reason{intake.ReasonPolicyChange}},
		"a change of the manifest": {draft(decision.KindBug, "Raise the budget in .sisf/factory.yaml."), github.AssociationMember,
			decision.KindPolicyChange, []intake.Reason{intake.ReasonPolicyChange}},
		"a fix by a passer-by": {draft(decision.KindBug, "It crashes."), github.AssociationNone,
			decision.KindBug, []intake.Reason{intake.ReasonRequesterNotAuthorized}},
		"a fix by a past contributor": {draft(decision.KindBug, "It crashes."), github.AssociationContributor,
			decision.KindBug, []intake.Reason{intake.ReasonRequesterNotAuthorized}},
		"a fix by an unknown association": {draft(decision.KindBug, "It crashes."), "",
			decision.KindBug, []intake.Reason{intake.ReasonRequesterNotAuthorized}},
		"a feature by a passer-by": {draft(decision.KindNewFeature, "Add export."), github.AssociationNone,
			decision.KindNewFeature, []intake.Reason{intake.ReasonNewFeature, intake.ReasonRequesterNotAuthorized}},
		"a kind this code does not know": {draft("refactor", "Tidy up."), github.AssociationMember,
			"refactor", []intake.Reason{intake.ReasonNewFeature}},
	}
	for name, tc := range cases {
		got := intake.Classify(tc.draft, tc.author, catalog)
		if got.Kind != tc.kind || got.NeedsApproval != (len(tc.reasons) > 0) || strings.Join(reasonNames(got.Reasons), ",") != strings.Join(reasonNames(tc.reasons), ",") {
			t.Errorf("%s: %+v, want kind %q and reasons %v", name, got, tc.kind, tc.reasons)
		}
	}
	// A rule about the rules is found in the criteria and the title too.
	inCriteria := intake.Draft{Kind: decision.KindBug, Title: "Tidy", Scope: "Tidy.", DoD: []string{"AGENTS.md no longer forbids force pushes"}}
	inTitle := intake.Draft{Kind: decision.KindBug, Title: "Edit DECISIONS.md", Scope: "Tidy.", DoD: []string{"done"}}
	for name, d := range map[string]intake.Draft{"criteria": inCriteria, "title": inTitle} {
		if got := intake.Classify(d, github.AssociationMember, catalog); got.Kind != decision.KindPolicyChange {
			t.Errorf("a rule change named in the %s was read as %q", name, got.Kind)
		}
	}
}

func reasonNames(reasons []intake.Reason) []string {
	names := make([]string, 0, len(reasons))
	for _, r := range reasons {
		names = append(names, string(r))
	}
	return names
}
