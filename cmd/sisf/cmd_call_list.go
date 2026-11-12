package main

import (
	"encoding/json"
	"fmt"
	"text/tabwriter"

	"github.com/bestagentkits/sisf/internal/actions/apispec"
	"github.com/bestagentkits/sisf/pkg/core"
)

// listedAction is one line of `sisf call --list`: what an action is called,
// what it does, and how carefully it is guarded.
type listedAction struct {
	Name        string           `json:"name"`
	Summary     string           `json:"summary"`
	Sensitivity core.Sensitivity `json:"sensitivity"`
	StepUp      bool             `json:"step_up,omitempty"`
}

// listActions prints the actions this build of sisf describes, which are the
// actions its control serves. It calls nothing, so it works without a
// running factory or a login.
func listActions(env *Env, asJSON bool) error {
	reg, err := apispec.BuiltinRegistry()
	if err != nil {
		return core.NewError(core.CodeInternal, "the action list of this sisf could not be built").WithCause(err)
	}
	specs := reg.Specs()
	listed := make([]listedAction, len(specs))
	for i, s := range specs {
		listed[i] = listedAction{Name: s.Name, Summary: s.Summary, Sensitivity: s.Sensitivity, StepUp: s.StepUp}
	}
	if asJSON {
		enc := json.NewEncoder(env.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(listed)
	}
	w := tabwriter.NewWriter(env.Stdout, 0, 0, 2, ' ', 0)
	for _, a := range listed {
		guard := string(a.Sensitivity)
		if a.StepUp {
			guard += ", step-up"
		}
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\n", a.Name, guard, a.Summary); err != nil {
			return err
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	_, err = fmt.Fprintln(env.Stdout, "\nCall one with: sisf call <name> --input '{...}'. Inputs and outputs: api/openapi.yaml and api/llms.txt in the source checkout.")
	return err
}
