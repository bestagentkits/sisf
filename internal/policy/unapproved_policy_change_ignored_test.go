package policy_test

import (
	"context"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/policy"
	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
)

// loosenedDecisions is what an agent's pull request would do to the decision
// rules to make its own work pass: a new schema version, and new features no
// longer waiting for the Owner.
func loosenedDecisions(files map[string]string) {
	rules := files[policy.PathDecisions]
	rules = strings.ReplaceAll(rules, "schema version: `1`", "schema version: `2`")
	rules = strings.ReplaceAll(rules, "(schema version 1)", "(schema version 2)")
	rules = strings.ReplaceAll(rules, "New features (always need Owner approval).", "New features (no approval needed).")
	files[policy.PathDecisions] = rules
}

func TestAnUnapprovedChangeToTheDecisionRulesLeavesTheApprovedBaselineInForce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := approved(t)
	before := w.inForce(t)

	// An agent opens a pull request that rewrites DECISIONS.md. The commit
	// exists in the repository, and may even have been merged, but the Owner
	// approved nothing.
	w.repo.commit(revB, loosenedDecisions)

	after := w.inForce(t)
	if after.Revision != revA || after.Digest != before.Digest {
		t.Fatalf("the policy in force moved to %s without approval", after.Revision)
	}
	if after.Decisions.Content != before.Decisions.Content || after.DecisionSchemaVersion != 1 {
		t.Fatal("the decision rules in force are not the approved ones")
	}
	if strings.Contains(after.Decisions.Content, "no approval needed") {
		t.Fatal("text from the unapproved commit reached the policy in force")
	}
	refs, err := after.Refs(core.StepIntake)
	if err != nil {
		t.Fatal(err)
	}
	if refs.DecisionSHA != revA || refs.PolicySHA != revA || refs.ReviewSHA != revA || refs.FeatureCatalogSHA != revA {
		t.Fatalf("a decision would be pinned to something other than the approved commit: %+v", refs)
	}

	// Asking for the policy at the unapproved commit is a hold, and the
	// commit is not even read.
	_, err = w.loader.At(ctx, revB)
	wantCode(t, err, core.CodePolicyHold, "policy at the unapproved commit")
	if n := w.repo.readsOf(revB); n != 0 {
		t.Fatalf("the unapproved commit was read %d times", n)
	}
}

func TestNobodyButTheOwnerCanMakeAChangedPolicyTheStandard(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := approved(t)
	w.repo.commit(revB, loosenedDecisions)

	// The Decision Layer, the agents and every role below the Owner have no
	// authority over the standard they are judged by.
	outsiders := map[string]core.Actor{
		"the decision layer":  {ID: "service:decision-layer", Kind: core.ActorService, Roles: []core.Role{core.RoleMaintainer}, Surface: core.SurfaceAPI},
		"the coding agent":    {ID: "agent:coder", Kind: core.ActorAgent, Roles: []core.Role{core.RoleAgent}, Surface: core.SurfaceMCP},
		"the QA agent":        {ID: "agent:qa", Kind: core.ActorAgent, Roles: []core.Role{core.RoleQA}, Surface: core.SurfaceMCP},
		"a maintainer":        {ID: "user:maintainer", Kind: core.ActorHuman, Roles: []core.Role{core.RoleMaintainer}, Surface: core.SurfaceDashboard},
		"a reporter":          {ID: "user:reporter", Kind: core.ActorHuman, Roles: []core.Role{core.RoleReporter}, Surface: core.SurfaceDashboard},
		"an agent as owner":   {ID: "agent:coder", Kind: core.ActorAgent, Roles: []core.Role{core.RoleOwner}, Surface: core.SurfaceWebMCP},
		"a service as owner":  {ID: "service:dispatcher", Kind: core.ActorService, Roles: []core.Role{core.RoleOwner}, Surface: core.SurfaceAPI},
		"an unidentified one": {Kind: core.ActorHuman, Roles: []core.Role{core.RoleOwner}, Surface: core.SurfaceAPI},
	}
	for name, actor := range outsiders {
		_, err := w.baselines.Approve(ctx, actor, revB, "")
		wantCode(t, err, core.CodePermissionDenied, name)
	}
	if got := w.inForce(t); got.Revision != revA {
		t.Fatalf("the baseline moved to %s", got.Revision)
	}
	denied := w.audit.Find("policy.approve_revision", core.OutcomeDenied)
	if len(denied) != len(outsiders) {
		t.Fatalf("every refused attempt must be audited: %d records for %d attempts", len(denied), len(outsiders))
	}

	// Even the Owner changes the standard only through an approval of the
	// exact new content.
	_, err := w.baselines.Approve(ctx, w.owner(), revB, "")
	wantCode(t, err, core.CodeApprovalRequired, "owner without an approval")

	// An approval request that an agent tried to decide through the Owner's
	// session stays undecided and authorizes nothing.
	submission := w.requestChange(t, "apr-policy-1", revB)
	agent := w.owner()
	agent.Kind = core.ActorAgent
	_, err = w.approvals.Submit(ctx, agent, submission)
	wantCode(t, err, core.CodeStepUpRequired, "agent deciding with the Owner's session")
	_, err = w.baselines.Approve(ctx, w.owner(), revB, "apr-policy-1")
	wantCode(t, err, core.CodeApprovalRequired, "undecided approval")

	if got := w.inForce(t); got.Revision != revA || got.DecisionSchemaVersion != 1 {
		t.Fatalf("the baseline moved to %s", got.Revision)
	}
}

func TestTheOwnerApprovingTheExactChangeMakesItTheStandard(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := approved(t)
	w.repo.commit(revB, loosenedDecisions)
	w.repo.commit(revC, func(files map[string]string) {
		loosenedDecisions(files)
		files[policy.PathReview] += "\nDefault pass: score of at least 10.\n"
	})

	submission := w.requestChange(t, "apr-policy-1", revB)
	if _, err := w.approvals.Submit(ctx, w.owner(), submission); err != nil {
		t.Fatal(err)
	}

	// The approval covers the content of revB only. It cannot carry another
	// commit into force.
	_, err := w.baselines.Approve(ctx, w.owner(), revC, "apr-policy-1")
	wantCode(t, err, core.CodeApprovalRequired, "approval used for another commit")
	if got := w.inForce(t); got.Revision != revA {
		t.Fatalf("the baseline moved to %s", got.Revision)
	}

	baseline, err := w.baselines.Approve(ctx, w.owner(), revB, "apr-policy-1")
	if err != nil {
		t.Fatalf("the approved change was refused: %v", err)
	}
	if baseline.Revision != revB || baseline.Previous != revA || baseline.ApprovedBy != "user:owner-1" || baseline.ApprovalID != "apr-policy-1" {
		t.Fatalf("baseline = %+v", baseline)
	}
	now := w.inForce(t)
	if now.Revision != revB || now.DecisionSchemaVersion != 2 {
		t.Fatalf("the approved change is not in force: %s, schema version %d", now.Revision, now.DecisionSchemaVersion)
	}
	if got := w.audit.Find("policy.approve_revision", core.OutcomeExecuted); len(got) != 2 {
		t.Fatalf("the install approval and the change must each be audited, got %d records", len(got))
	}

	// The approval was single use: it does not move the baseline again.
	_, err = w.baselines.Approve(ctx, w.owner(), revC, "apr-policy-1")
	wantCode(t, err, core.CodeReplayed, "approval used a second time")
	// The old revision is no longer the policy either.
	_, err = w.loader.At(ctx, revA)
	wantCode(t, err, core.CodePolicyHold, "policy at the replaced commit")
}

func TestAReaderCannotChangeThePolicyItWasHanded(t *testing.T) {
	t.Parallel()
	w := approved(t)
	handed := w.inForce(t)
	handed.Catalog.Features = append(handed.Catalog.Features[:0], policy.Feature{Name: "Anything an agent wants"})
	handed.Manifest.Release.Policy = core.DelegationBounded
	handed.DecisionSchemaVersion = 99

	fresh := w.inForce(t)
	if fresh.Catalog.Has("Anything an agent wants") || !fresh.Catalog.Has("Decision Layer") {
		t.Fatal("a reader's edit changed the feature catalogue in force")
	}
	if fresh.Manifest.Release.Policy != core.DelegationProposal || fresh.DecisionSchemaVersion != 1 {
		t.Fatal("a reader's edit changed the policy in force")
	}
}

func TestAnApprovalOfAChangeIsVoidOnceThePolicyItWasJudgedUnderIsReplaced(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := approved(t)
	w.repo.commit(revB, loosenedDecisions)
	w.repo.commit(revC, func(files map[string]string) { files[policy.PathAgents] += "\nNote for agents.\n" })

	// Two changes are approved while revA is in force.
	first := w.requestChange(t, "apr-policy-1", revB)
	second := w.requestChange(t, "apr-policy-2", revC)
	for _, submission := range []approvals.Submission{first, second} {
		if _, err := w.approvals.Submit(ctx, w.owner(), submission); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.baselines.Approve(ctx, w.owner(), revB, "apr-policy-1"); err != nil {
		t.Fatal(err)
	}
	// The second approval was given under revA, which is no longer in force.
	_, err := w.baselines.Approve(ctx, w.owner(), revC, "apr-policy-2")
	wantCode(t, err, core.CodeApprovalRequired, "approval given under a replaced policy")
	if got := w.inForce(t); got.Revision != revB {
		t.Fatalf("the baseline moved to %s", got.Revision)
	}
}
