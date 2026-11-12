package decision

import "fmt"

// Identifiers of the options a proposal lists. Every option is written as
// "<identifier>: <description>", and doing nothing is always one of them.
const (
	OptionFix       = "fix"
	OptionRollback  = "rollback"
	OptionDoNothing = "do_nothing"
)

// options lists what could be done about the regression of one endpoint,
// the recommended option first. The endpoint name is quoted data.
func options(name string) []string {
	return []string{
		fmt.Sprintf("%s: Find and fix the cause of the server errors of endpoint %q, with a regression test (recommended).", OptionFix, name),
		fmt.Sprintf("%s: Roll back the latest release that touched endpoint %q, if the regression started with it.", OptionRollback, name),
		fmt.Sprintf("%s: Change nothing and keep observing endpoint %q; the server errors continue until the condition clears.", OptionDoNothing, name),
	}
}

// Chosen is the option the layer chose for the outcome: the recommended
// fix when it handed out or holds a proposal, and doing nothing otherwise,
// which is the result whenever the evidence is thin.
func (o Outcome) Chosen() string {
	if o.Proposal != nil {
		return OptionFix
	}
	return OptionDoNothing
}
