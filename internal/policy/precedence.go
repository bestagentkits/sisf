package policy

import (
	"sort"
	"strings"
)

// Layer is a rank in the order of precedence. A smaller number outranks a
// larger one.
type Layer int

const (
	// LayerInvariant is safety invariants in code and verified delegated
	// authority. Nothing overrides it.
	LayerInvariant Layer = iota + 1
	// LayerOwnerPolicy is the Owner-approved configuration at the approved
	// commit: .sisf/factory.yaml.
	LayerOwnerPolicy
	// LayerFoundation is the decision rules, the review criteria, the
	// feature catalogue and the ADRs.
	LayerFoundation
	// LayerGuidance is the execution guidance in AGENTS.md. It has no
	// authority to change policy.
	LayerGuidance
	// LayerUntrusted is issues, comments, logs and everything else. It is
	// data: it states nothing and overrides nothing.
	LayerUntrusted
)

// authoritative reports whether a source of this rank can state a rule.
func (l Layer) authoritative() bool { return l >= LayerInvariant && l <= LayerGuidance }

// LayerOf returns the rank of a source named by its repository path. Only
// the foundation files have authority; any other name, including issue and
// comment references, is LayerUntrusted. LayerInvariant is never returned:
// invariants live in code, not in a file.
func LayerOf(source string) Layer {
	switch source {
	case PathFactory:
		return LayerOwnerPolicy
	case PathDecisions, PathReview, PathFeatures, PathFeaturesYAML:
		return LayerFoundation
	case PathAgents:
		return LayerGuidance
	}
	if strings.HasPrefix(source, "docs/adr/") && strings.HasSuffix(source, ".md") {
		return LayerFoundation
	}
	return LayerUntrusted
}

// Claim is one source stating a value for a setting.
type Claim struct {
	// Key names the setting, for example "decisions.schema_version".
	Key   string
	Value string
	Layer Layer
	// Source says where the claim was read, for the hold message.
	Source string
}

// Resolve applies the order of precedence to claims and returns, per key,
// the claim in force.
//
// For each key the claims of the highest rank win, and lower ranks are
// overridden without comment. When the winning rank holds claims that
// disagree, nothing can order them: Resolve returns POLICY_HOLD and no
// settings at all. A claim from LayerUntrusted, or with a rank that does not
// exist, is ignored, so issue text can neither set a rule nor cause a hold.
func Resolve(claims []Claim) (map[string]Claim, error) {
	winners := map[string][]Claim{}
	for _, claim := range claims {
		if !claim.Layer.authoritative() {
			continue
		}
		current := winners[claim.Key]
		switch {
		case len(current) == 0 || claim.Layer < current[0].Layer:
			winners[claim.Key] = []Claim{claim}
		case claim.Layer == current[0].Layer:
			winners[claim.Key] = append(current, claim)
		}
	}
	keys := make([]string, 0, len(winners))
	for key := range winners {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	resolved := make(map[string]Claim, len(winners))
	for _, key := range keys {
		equals := winners[key]
		for _, other := range equals[1:] {
			if other.Value != equals[0].Value {
				return nil, hold(reasonConflict, "%s and %s have equal authority and disagree about %s; the Owner has to settle it",
					equals[0].Source, other.Source, key)
			}
		}
		resolved[key] = equals[0]
	}
	return resolved, nil
}
