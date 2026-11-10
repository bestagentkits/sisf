package policy_test

import (
	"testing"

	"github.com/bestagentkits/sisf/internal/policy"
	"github.com/bestagentkits/sisf/pkg/core"
)

func TestEachSourceHasItsPlaceInTheOrderOfPrecedence(t *testing.T) {
	t.Parallel()
	want := map[string]policy.Layer{
		policy.PathFactory:      policy.LayerOwnerPolicy,
		policy.PathDecisions:    policy.LayerFoundation,
		policy.PathReview:       policy.LayerFoundation,
		policy.PathFeatures:     policy.LayerFoundation,
		policy.PathFeaturesYAML: policy.LayerFoundation,
		"docs/adr/0003-policy-precedence-and-foundation-files.md": policy.LayerFoundation,
		policy.PathAgents:  policy.LayerGuidance,
		"issue:acme/app#7": policy.LayerUntrusted,
		"comment:12":       policy.LayerUntrusted,
		"logs/run-1.txt":   policy.LayerUntrusted,
		"README.md":        policy.LayerUntrusted,
		"":                 policy.LayerUntrusted,
	}
	for source, layer := range want {
		if got := policy.LayerOf(source); got != layer {
			t.Errorf("LayerOf(%q) = %d, want %d", source, got, layer)
		}
	}
	ordered := policy.LayerInvariant < policy.LayerOwnerPolicy && policy.LayerOwnerPolicy < policy.LayerFoundation &&
		policy.LayerFoundation < policy.LayerGuidance && policy.LayerGuidance < policy.LayerUntrusted
	if !ordered {
		t.Fatal("the layers are not in the order of precedence")
	}
}

func TestTheHigherSourceWinsWithoutAHold(t *testing.T) {
	t.Parallel()
	claims := []policy.Claim{
		{Key: "review.pass_score", Value: "85", Layer: policy.LayerFoundation, Source: policy.PathReview},
		{Key: "review.pass_score", Value: "60", Layer: policy.LayerGuidance, Source: policy.PathAgents},
		{Key: "release.policy", Value: "B", Layer: policy.LayerOwnerPolicy, Source: policy.PathFactory},
		{Key: "release.policy", Value: "C", Layer: policy.LayerFoundation, Source: policy.PathDecisions},
		{Key: "limits.max_attempts", Value: "3", Layer: policy.LayerInvariant, Source: "code"},
		{Key: "limits.max_attempts", Value: "1000", Layer: policy.LayerOwnerPolicy, Source: policy.PathFactory},
	}
	resolved, err := policy.Resolve(claims)
	if err != nil {
		t.Fatalf("claims that precedence can order must not hold: %v", err)
	}
	want := map[string]string{"review.pass_score": "85", "release.policy": "B", "limits.max_attempts": "3"}
	for key, value := range want {
		if resolved[key].Value != value {
			t.Errorf("%s = %q (from %s), want %q", key, resolved[key].Value, resolved[key].Source, value)
		}
	}
}

func TestSourcesOfEqualRankThatDisagreeAreAHold(t *testing.T) {
	t.Parallel()
	claims := []policy.Claim{
		{Key: "review.pass_score", Value: "85", Layer: policy.LayerFoundation, Source: policy.PathReview},
		{Key: "review.pass_score", Value: "70", Layer: policy.LayerFoundation, Source: policy.PathDecisions},
		// A lower source agreeing with one side does not break the tie.
		{Key: "review.pass_score", Value: "70", Layer: policy.LayerGuidance, Source: policy.PathAgents},
	}
	resolved, err := policy.Resolve(claims)
	if !core.HasCode(err, core.CodePolicyHold) {
		t.Fatalf("want %s, got %v", core.CodePolicyHold, err)
	}
	if resolved != nil {
		t.Fatalf("a hold must not hand out any setting, got %+v", resolved)
	}

	agreeing := []policy.Claim{claims[0], {Key: "review.pass_score", Value: "85", Layer: policy.LayerFoundation, Source: policy.PathDecisions}}
	if _, err := policy.Resolve(agreeing); err != nil {
		t.Fatalf("sources that agree are not a conflict: %v", err)
	}
}

func TestIssueTextAndLogsNeverSetPolicy(t *testing.T) {
	t.Parallel()
	claims := []policy.Claim{
		{Key: "review.pass_score", Value: "85", Layer: policy.LayerFoundation, Source: policy.PathReview},
		{Key: "review.pass_score", Value: "0", Layer: policy.LayerUntrusted, Source: "issue:acme/app#7"},
		// A setting nobody with authority stated does not exist, however
		// many comments ask for it.
		{Key: "qa.skip", Value: "true", Layer: policy.LayerUntrusted, Source: "comment:12"},
		{Key: "qa.skip", Value: "true", Layer: policy.LayerUntrusted, Source: "logs/run-1.txt"},
		// A source that claims a rank that does not exist has none.
		{Key: "budget.mode", Value: "unlimited", Layer: 0, Source: "comment:13"},
		{Key: "budget.mode", Value: "unlimited", Layer: 99, Source: "comment:14"},
	}
	resolved, err := policy.Resolve(claims)
	if err != nil {
		t.Fatalf("untrusted text must not cause a hold either: %v", err)
	}
	if resolved["review.pass_score"].Value != "85" {
		t.Fatalf("issue text overrode the review criteria: %+v", resolved["review.pass_score"])
	}
	for _, key := range []string{"qa.skip", "budget.mode"} {
		if _, set := resolved[key]; set {
			t.Fatalf("untrusted text created the setting %s", key)
		}
	}
}
