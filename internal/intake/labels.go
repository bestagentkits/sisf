package intake

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/decision"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// LabelRules is how labels are read for one deployment.
type LabelRules struct {
	// ReadyAlias is an existing label that counts as sisf:ready. It is empty
	// by default: no other label asks for intake unless the Owner says so.
	ReadyAlias string
}

// reservedPrefixes are label families with a meaning of their own; none of
// them can be turned into an intake request, under any casing.
var reservedPrefixes = []string{"sisf:", "kind:", github.LabelAgentPrefix, github.LabelDeploymentPrefix}

// GitHub compares label names without regard to case: one repository cannot
// hold two labels that differ only in case, so every label name is matched
// the same way here.

// sameLabel reports whether a and b name the same label.
func sameLabel(a, b string) bool { return strings.EqualFold(a, b) }

// cutLabelPrefix returns what follows prefix in name, and whether name
// starts with prefix under any casing. It compares rune by rune as
// sameLabel does, so a letter whose case partner is encoded in more bytes,
// such as the Kelvin sign for k, cannot pass for another family's name.
func cutLabelPrefix(name, prefix string) (string, bool) {
	rest := name
	for _, want := range prefix {
		got, size := utf8.DecodeRuneInString(rest)
		if size == 0 || !strings.EqualFold(string(got), string(want)) {
			return "", false
		}
		rest = rest[size:]
	}
	return rest, true
}

// HasLabel reports whether labels hold the label name, under any casing.
func HasLabel(labels []github.Label, name string) bool {
	return slices.ContainsFunc(labels, func(l github.Label) bool { return sameLabel(l.Name, name) })
}

// RulesFrom reads the label rules from the approved manifest. The alias is
// taken from extensions.intake.ready_label_alias, for example "ai-handle".
// An alias that is not plain text, or that names a label with a meaning of
// its own, is INVALID_ARGUMENT: a setting that cannot be honored is refused,
// never ignored.
func RulesFrom(m *manifest.Manifest) (LabelRules, error) {
	if m == nil {
		return LabelRules{}, core.NewError(core.CodeInvalidArgument, "label rules need the approved manifest")
	}
	section, ok := m.Extensions["intake"].(map[string]any)
	if !ok {
		return LabelRules{}, nil
	}
	raw, set := section["ready_label_alias"]
	if !set {
		return LabelRules{}, nil
	}
	alias, isText := raw.(string)
	alias = strings.TrimSpace(alias)
	reserved := slices.ContainsFunc(reservedPrefixes, func(prefix string) bool {
		_, ok := cutLabelPrefix(alias, prefix)
		return ok
	})
	if !isText || alias == "" || reserved {
		return LabelRules{}, core.NewError(core.CodeInvalidArgument,
			fmt.Sprintf("extensions.intake.ready_label_alias must name an ordinary label, got %v", raw))
	}
	return LabelRules{ReadyAlias: alias}, nil
}

// Labels is what the labels of an issue mean to intake. Nothing in it is a
// permission, a role or a budget: whoever applied a label gained nothing.
type Labels struct {
	// Ready says the issue asks for intake. It does not permit execution.
	Ready bool
	// Kind is the kind of work the kind labels state, the most cautious one
	// when several are present, or "" when there is none.
	Kind decision.Kind
	// QuestionOnly says the issue is labelled as a question and nothing
	// else, so there is no work to scope.
	QuestionOnly bool
	// AgentPreference is the value of a single agent: label. It is a routing
	// wish and nothing more; with several such labels there is no wish.
	AgentPreference string
	// Deployments are the values of the deployment: labels.
	Deployments []string
}

// ReadLabels interprets the labels of an issue under rules.
func ReadLabels(labels []github.Label, rules LabelRules) Labels {
	var out Labels
	var agents []string
	question := false
	for _, label := range labels {
		name := label.Name
		agent, isAgent := cutLabelPrefix(name, github.LabelAgentPrefix)
		deployment, isDeployment := cutLabelPrefix(name, github.LabelDeploymentPrefix)
		switch {
		case sameLabel(name, github.LabelReady) || (rules.ReadyAlias != "" && sameLabel(name, rules.ReadyAlias)):
			out.Ready = true
		case sameLabel(name, github.LabelKindFeature):
			out.Kind = moreCautious(out.Kind, decision.KindNewFeature)
		case sameLabel(name, github.LabelKindImprovement):
			out.Kind = moreCautious(out.Kind, decision.KindExistingImprovement)
		case sameLabel(name, github.LabelKindBug):
			out.Kind = moreCautious(out.Kind, decision.KindBug)
		case sameLabel(name, github.LabelKindQuestion):
			question = true
		case isAgent:
			agents = append(agents, agent)
		case isDeployment:
			out.Deployments = append(out.Deployments, deployment)
		}
	}
	out.QuestionOnly = question && out.Kind == ""
	if len(agents) == 1 {
		out.AgentPreference = agents[0]
	}
	return out
}

// caution orders the kinds of work by how much authority they need.
var caution = []decision.Kind{"", decision.KindBug, decision.KindExistingImprovement, decision.KindNewFeature, decision.KindPolicyChange}

// moreCautious returns whichever kind needs more authority. Contradicting
// labels are thereby read the careful way, never the convenient one.
func moreCautious(a, b decision.Kind) decision.Kind {
	if slices.Index(caution, b) > slices.Index(caution, a) {
		return b
	}
	return a
}

// AddressedTo reports whether the issue is meant for deployment id. An
// issue without a deployment label is; one that names exactly this
// deployment, under any casing as the label is, is; one that names another
// deployment, or several, is not.
func (l Labels) AddressedTo(id core.DeploymentID) bool {
	switch len(l.Deployments) {
	case 0:
		return true
	case 1:
		return id != "" && strings.EqualFold(l.Deployments[0], string(id))
	}
	return false
}
