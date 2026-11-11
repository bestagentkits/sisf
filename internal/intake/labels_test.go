package intake_test

import (
	"slices"
	"testing"

	"github.com/bestagentkits/sisf/internal/intake"
	"github.com/bestagentkits/sisf/internal/policy"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/decision"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// named builds the label list of an issue.
func named(names ...string) []github.Label {
	labels := make([]github.Label, 0, len(names))
	for _, name := range names {
		labels = append(labels, github.Label{Name: name})
	}
	return labels
}

// withAlias is a manifest whose intake extension sets the alias to value.
func withAlias(value any) *manifest.Manifest {
	return &manifest.Manifest{Extensions: map[string]any{"intake": map[string]any{"ready_label_alias": value}}}
}

func TestOnlyTheReadyLabelAsksForIntakeByDefault(t *testing.T) {
	t.Parallel()
	rules, err := intake.RulesFrom(&manifest.Manifest{})
	if err != nil || rules.ReadyAlias != "" {
		t.Fatalf("a manifest without the setting must define no alias: %+v, %v", rules, err)
	}
	for _, name := range []string{"ai-handle", "ready", "sisf:approved", "sisf:running", "sisf:ready "} {
		if intake.ReadLabels(named(name), rules).Ready {
			t.Errorf("label %q asked for intake", name)
		}
	}
	if !intake.ReadLabels(named("bug", github.LabelReady), rules).Ready {
		t.Fatal("sisf:ready did not ask for intake")
	}
}

func TestTheOwnerCanMapAnExistingLabelToIntake(t *testing.T) {
	t.Parallel()
	rules, err := intake.RulesFrom(withAlias(" ai-handle "))
	if err != nil || rules.ReadyAlias != "ai-handle" {
		t.Fatalf("rules = %+v, %v", rules, err)
	}
	got := intake.ReadLabels(named("ai-handle", github.LabelKindBug), rules)
	if !got.Ready || got.Kind != decision.KindBug {
		t.Fatalf("the mapped label must ask for intake and nothing else: %+v", got)
	}
	if intake.ReadLabels(named("ai-handled"), rules).Ready {
		t.Fatal("a label that merely resembles the alias asked for intake")
	}
}

func TestAnAliasThatCannotBeHonoredIsRefused(t *testing.T) {
	t.Parallel()
	for name, value := range map[string]any{
		"empty":             "  ",
		"not text":          true,
		"a list":            []any{"ai-handle"},
		"a status label":    "sisf:approved",
		"a kind label":      "kind:bug",
		"an agent label":    "agent:claude",
		"a deployment name": "deployment:local",
		// GitHub compares label names without regard to case, so a
		// reserved family under any casing is the same family.
		"a status label in capitals": "SISF:Report",
		"a mixed-case status label":  "Sisf:approved",
		"a kind label in capitals":   "KIND:bug",
		"an agent label in capitals": "Agent:claude",
		"a deployment in capitals":   "DEPLOYMENT:local",
	} {
		_, err := intake.RulesFrom(withAlias(value))
		wantCode(t, err, core.CodeInvalidArgument, name)
	}
	_, err := intake.RulesFrom(nil)
	wantCode(t, err, core.CodeInvalidArgument, "no manifest")
}

func TestContradictingKindLabelsAreReadTheCautiousWay(t *testing.T) {
	t.Parallel()
	cases := []struct {
		labels []string
		want   decision.Kind
	}{
		{[]string{github.LabelKindBug}, decision.KindBug},
		{[]string{github.LabelKindBug, github.LabelKindImprovement}, decision.KindExistingImprovement},
		{[]string{github.LabelKindFeature, github.LabelKindBug}, decision.KindNewFeature},
		{[]string{github.LabelKindBug, github.LabelKindFeature, github.LabelKindImprovement}, decision.KindNewFeature},
		{[]string{github.LabelKindQuestion, github.LabelKindBug}, decision.KindBug},
		{[]string{"kind:hotfix", "bug"}, ""},
	}
	for _, c := range cases {
		if got := intake.ReadLabels(named(c.labels...), intake.LabelRules{}).Kind; got != c.want {
			t.Errorf("labels %v read as %q, want %q", c.labels, got, c.want)
		}
	}
	only := intake.ReadLabels(named(github.LabelKindQuestion), intake.LabelRules{})
	mixed := intake.ReadLabels(named(github.LabelKindQuestion, github.LabelKindBug), intake.LabelRules{})
	if !only.QuestionOnly || mixed.QuestionOnly {
		t.Fatalf("question only = %v, with a bug label = %v", only.QuestionOnly, mixed.QuestionOnly)
	}
}

func TestAnAgentLabelIsARoutingWishAndNothingElse(t *testing.T) {
	t.Parallel()
	one := intake.ReadLabels(named("agent:claude"), intake.LabelRules{})
	if one.AgentPreference != "claude" || one.Ready || one.Kind != "" {
		t.Fatalf("an agent label must only state a preference: %+v", one)
	}
	several := intake.ReadLabels(named("agent:claude", "agent:codex"), intake.LabelRules{})
	if several.AgentPreference != "" {
		t.Fatalf("contradicting agent labels state no preference, got %q", several.AgentPreference)
	}
}

func TestADeploymentLabelMustNameThisDeployment(t *testing.T) {
	t.Parallel()
	cases := []struct {
		labels []string
		id     core.DeploymentID
		want   bool
	}{
		{nil, "local", true},
		{[]string{"deployment:local"}, "local", true},
		{[]string{"deployment:staging"}, "local", false},
		{[]string{"deployment:Local"}, "local", true},
		{[]string{"deployment:"}, "local", false},
		{[]string{"deployment:local", "deployment:staging"}, "local", false},
		{[]string{"deployment:"}, "", false},
	}
	for _, c := range cases {
		got := intake.ReadLabels(named(c.labels...), intake.LabelRules{})
		if got.AddressedTo(c.id) != c.want {
			t.Errorf("labels %v addressed to %q = %v, want %v", c.labels, c.id, !c.want, c.want)
		}
	}
	read := intake.ReadLabels(named("deployment:a", "deployment:b"), intake.LabelRules{})
	if !slices.Equal(read.Deployments, []string{"a", "b"}) {
		t.Fatalf("deployments = %v", read.Deployments)
	}
}

func TestAnIssueForAnotherDeploymentOrWithoutTheReadyLabelIsLeftAlone(t *testing.T) {
	t.Parallel()
	cases := map[string][]string{
		"no ready label":         {github.LabelKindBug},
		"only a status label":    {github.LabelApproved, github.LabelKindBug},
		"an unmapped alias":      {"ai-handle", github.LabelKindBug},
		"another deployment":     {github.LabelReady, github.LabelKindBug, "deployment:staging"},
		"two deployments":        {github.LabelReady, github.LabelKindBug, "deployment:local", "deployment:staging"},
		"a question and nothing": {github.LabelReady, github.LabelKindQuestion},
	}
	for name, labels := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newFactory(t)
			f.open(member, completeBody, labels...)
			result := f.reconcile()
			if result.Skipped == "" || len(result.Events) != 0 || f.stateOf() != "" {
				t.Fatalf("result = %+v, state %q", result, f.stateOf())
			}
			if len(f.state.Outbox()) != 0 || len(f.inbox()) != 0 {
				t.Fatal("an issue that was not taken in caused an effect or an approval request")
			}
		})
	}
}

func TestAMappedLabelAndThisDeploymentAreTakenIn(t *testing.T) {
	t.Parallel()
	f := newFactory(t)
	f.policy.set(func(s *policy.Snapshot) {
		s.Manifest.Extensions = map[string]any{"intake": map[string]any{"ready_label_alias": "ai-handle"}}
	}, nil)
	f.open(member, completeBody, "ai-handle", github.LabelKindBug, "deployment:local", "agent:claude")
	f.reconcile()
	f.wantState(core.StateReady)
}

func TestAnUnusableAliasStopsIntakeInsteadOfBeingIgnored(t *testing.T) {
	t.Parallel()
	f := newFactory(t)
	f.policy.set(func(s *policy.Snapshot) {
		s.Manifest.Extensions = map[string]any{"intake": map[string]any{"ready_label_alias": "sisf:approved"}}
	}, nil)
	f.open(member, completeBody, github.LabelReady, github.LabelKindBug)
	_, err := f.service.Reconcile(t.Context(), issueRef)
	wantCode(t, err, core.CodeInvalidArgument, "a reserved alias")
	if f.stateOf() != "" {
		t.Fatal("intake went ahead under label rules it could not read")
	}
}

// GitHub compares label names without regard to case: one repository
// cannot hold two labels that differ only in case, so a label is read the
// same under any casing.
func TestLabelsAreReadWithoutRegardToCase(t *testing.T) {
	t.Parallel()
	rules, err := intake.RulesFrom(withAlias("ai-handle"))
	if err != nil {
		t.Fatal(err)
	}
	got := intake.ReadLabels(named("SISF:READY", "Kind:Bug", "AGENT:claude", "Deployment:local"), intake.LabelRules{})
	if !got.Ready || got.Kind != decision.KindBug || got.AgentPreference != "claude" || !got.AddressedTo("local") {
		t.Fatalf("labels in other casings read as %+v", got)
	}
	if !intake.ReadLabels(named("AI-Handle"), rules).Ready {
		t.Fatal("the alias in another casing did not ask for intake")
	}
	if !intake.HasLabel(named("SISF:Needs-Info"), github.LabelNeedsInfo) || intake.HasLabel(named("sisf:needs"), github.LabelNeedsInfo) {
		t.Fatal("HasLabel must match a label name under any casing, and only the whole name")
	}
}
