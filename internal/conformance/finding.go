// Package conformance decides whether a factory manifest declares one
// conformant factory deployment.
//
// A manifest is conformant when it declares all three zones and all nine
// runtime units under one identity, keeps the zones isolated from each other,
// keeps QA independent of the coding agent, places the trusted release
// supervisor outside any candidate's reach, selects only backends able to do
// what it asks of them, holds references to secrets and never the secrets,
// and sets no bound to a value that removes it. Every rule that fails yields a finding that names the rule, the place
// in the manifest and the change that fixes it. A finding never repeats a
// value that may be a secret. The checker reads declarations only: it is the
// gate before a deployment starts, not proof of what runs afterwards.
package conformance

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
)

// Finding is one way a manifest fails to be conformant.
type Finding struct {
	Rule Rule `json:"rule"`
	// Path locates the offending value in manifest keys, for example
	// "units.app.mounts[0]".
	Path string `json:"path"`
	// Message says what is wrong and why it matters.
	Message string `json:"message"`
	// Fix says what to change in the manifest.
	Fix string `json:"fix,omitempty"`
}

// Report is the outcome of checking one manifest.
type Report struct {
	// Source names the file that was checked, when there was one.
	Source   string    `json:"source,omitempty"`
	Findings []Finding `json:"findings"`
	// SelfUpdate reports whether a trusted supervisor is declared and all
	// four conditions of its boundary hold. When it is false the factory
	// must not publish by itself.
	SelfUpdate bool `json:"self_update"`
	// TrustedCodeOnly reports that the Owner's acknowledgement is what lets
	// this manifest pass: its execution backend cannot isolate agent runs or
	// enforce the egress policy, so the deployment has reduced assurance and
	// must run only code the Owner trusts.
	TrustedCodeOnly bool `json:"trusted_code_only"`
	// SameModelRoute reports that the Owner's acknowledgement is what lets
	// this manifest pass although QA runs on the coding agent's provider and
	// model: the two share blind spots, so the deployment has reduced
	// assurance.
	SameModelRoute bool `json:"same_model_route"`
	// Risks lists accepted settings that widen what an agent can reach, such
	// as an egress policy that blocks nothing. They are choices the Owner
	// may make; each is shown beside the verdict, never hidden behind it.
	Risks []Risk `json:"risks,omitempty"`
}

// Risk is an accepted setting the Owner is warned about.
type Risk struct {
	// Path locates the setting in manifest keys.
	Path string `json:"path"`
	// Message says what the setting allows.
	Message string `json:"message"`
}

// Conformant reports whether no rule failed.
func (r Report) Conformant() bool { return len(r.Findings) == 0 }

// Has reports whether rule failed at least once.
func (r Report) Has(rule Rule) bool {
	return slices.ContainsFunc(r.Findings, func(f Finding) bool { return f.Rule == rule })
}

// Err returns nil for a conformant manifest. Otherwise it returns a typed
// error that counts the findings, names the rules that failed and says what
// the caller may do next.
func (r Report) Err() error {
	if r.Conformant() {
		return nil
	}
	var failed []string
	for _, finding := range r.Findings {
		if !slices.Contains(failed, string(finding.Rule)) {
			failed = append(failed, string(finding.Rule))
		}
	}
	sort.Strings(failed)
	subject, next := "the manifest", core.NextAction{
		Action:      "conformance.check",
		Description: "correct each finding as its fix describes, then check the manifest again",
	}
	if r.Source != "" {
		subject = "manifest " + r.Source
		next.Params = map[string]string{"file": r.Source}
	}
	noun := "findings"
	if len(r.Findings) == 1 {
		noun = "finding"
	}
	failure := core.NewError(core.CodeInvalidArgument,
		fmt.Sprintf("%s is not conformant: %d %s", subject, len(r.Findings), noun), next)
	failure.Details = map[string]string{"findings": strconv.Itoa(len(r.Findings)), "rules": strings.Join(failed, ",")}
	return failure
}
