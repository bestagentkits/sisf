package web

import (
	"net/http"
	"strings"

	"github.com/bestagentkits/sisf/internal/actions/pendingactions"
)

// actionSampleIssue runs a sample issue through the lifecycle. The factory
// offers it only when it can really do so.
const actionSampleIssue = "setup.sample_issue"

// Step outcomes of the wizard.
const (
	stepPass        = "pass"
	stepFail        = "fail"
	stepUnavailable = "unavailable"
)

type setupStep struct {
	Key    string
	Title  string
	Status string
	Label  string
	Detail string
	Panel  *Panel
}

type setupView struct {
	Frame
	Steps           []setupStep
	ChecksPass      bool
	SampleAvailable bool
}

// setup is the first-time wizard: it checks the sign-in, the GitHub App and
// the budget confirmation against the live factory, and offers the sample
// issue once every check passes.
func (h *handler) setup(w http.ResponseWriter, r *http.Request, s *session) {
	view := setupView{Frame: h.frame(s, "setup", "/setup"), SampleAvailable: h.registered(actionSampleIssue)}
	view.Steps = []setupStep{
		{Key: "sso", Title: "Single sign-on", Status: stepPass, Label: "passed", Detail: "You are signed in as " + s.who + " through the identity provider."},
		h.githubStep(r, s),
		h.budgetStep(r, s),
	}
	view.ChecksPass = true
	for _, st := range view.Steps {
		view.ChecksPass = view.ChecksPass && st.Status == stepPass
	}
	h.render(w, r, http.StatusOK, pageSetup, view)
}

// githubStep looks for a GitHub App credential among the secret references.
func (h *handler) githubStep(r *http.Request, s *session) setupStep {
	step := setupStep{Key: "github-app", Title: "GitHub App"}
	panel := h.read(r, s, pendingactions.ActionSecretsListRefs, nil)
	if !panel.OK() {
		return failedStep(step, panel)
	}
	t := tableFrom(panel.Output)
	for _, row := range t.Rows {
		if strings.Contains(strings.ToLower(strings.Join(row, " ")), "github") {
			step.Status, step.Label, step.Detail = stepPass, "passed", "A GitHub credential is registered by reference."
			return step
		}
	}
	step.Status, step.Label = stepFail, "missing"
	step.Detail = "No GitHub App credential is registered. Add its secret reference, then check again."
	return step
}

// budgetStep requires the Owner to have confirmed the budget.
func (h *handler) budgetStep(r *http.Request, s *session) setupStep {
	step := setupStep{Key: "budget", Title: "Budget confirmation"}
	m, panel := h.budgetView(r, s)
	if !panel.OK() {
		return failedStep(step, panel)
	}
	if !m.Confirmed {
		step.Status, step.Label, step.Detail = stepFail, "not confirmed", "The Owner has not confirmed the budget, so no work is claimed."
		return step
	}
	step.Status, step.Label, step.Detail = stepPass, "passed", "The budget is confirmed in "+string(m.Mode)+" mode."
	return step
}

func failedStep(step setupStep, panel Panel) setupStep {
	step.Panel = &panel
	step.Status, step.Label = stepFail, "failed"
	step.Detail = "The check could not be made."
	if panel.Unavailable {
		step.Status, step.Label = stepUnavailable, "not available yet"
	}
	return step
}
