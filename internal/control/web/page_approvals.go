package web

import (
	"net/http"
	"time"

	"github.com/bestagentkits/sisf/internal/actions/approvalactions"
	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
)

type approvalRow struct {
	ID      string
	Risk    string
	Action  string
	Issue   string
	Status  approvals.Status
	Expires time.Time
}

type approvalsView struct {
	Frame
	List     Panel
	Statuses []approvals.Status
	Selected string
	Rows     []approvalRow
}

var approvalStatuses = []approvals.Status{approvals.StatusPending, approvals.StatusApproved, approvals.StatusRejected,
	approvals.StatusChangesRequested, approvals.StatusExpired, approvals.StatusConsumed}

func (h *handler) approvals(w http.ResponseWriter, r *http.Request, s *session) {
	selected := r.URL.Query()["status"]
	input := map[string]any{}
	if len(selected) > 0 {
		input["statuses"] = selected
	}
	list, panel := decode[approvalactions.RequestList](h.read(r, s, approvalactions.ActionList, input))
	view := approvalsView{Frame: h.frame(s, "approvals", "/approvals"), List: panel, Statuses: approvalStatuses}
	if len(selected) == 1 {
		view.Selected = selected[0]
	}
	for _, req := range list.Requests {
		row := approvalRow{ID: req.ID, Risk: req.RiskSummary, Action: req.Action, Status: req.Status, Expires: req.ExpiresAt}
		if !req.IssueID.IsZero() {
			row.Issue = req.IssueID.String()
		}
		view.Rows = append(view.Rows, row)
	}
	h.render(w, r, statusOfPanels(panel), pageApprovals, view)
}

type requestView struct {
	ID               string
	Action           string
	Status           approvals.Status
	RequestedBy      string
	Issue            string
	ExpiresAt        time.Time
	SubjectDigest    core.Digest
	PolicyRevision   core.CommitSHA
	Nonce            string
	RiskSummary      string
	Diff             string
	AffectedFeatures []string
	EvidenceRefs     []string
	StepUp           bool
}

type decisionView struct {
	Decision    approvals.Decision
	Approver    string
	Reason      string
	At          time.Time
	BudgetCap   string
	CondExpires time.Time
}

type approvalView struct {
	Frame
	Get       Panel
	Request   requestView
	Decision  *decisionView
	Open      bool
	CanDecide bool
}

// approval shows one request with what the Owner needs to decide: the
// one-line risk summary, the expected diff and the affected features. The
// decision form echoes the nonce, digest and policy revision it displays.
func (h *handler) approval(w http.ResponseWriter, r *http.Request, s *session) {
	id := r.PathValue("id")
	here := approvalLink(id)
	proposal, panel := decode[approvalactions.Proposal](h.read(r, s, approvalactions.ActionGet, map[string]string{"id": id}))
	view := approvalView{Frame: h.frame(s, "approvals", here), Get: panel}
	if panel.OK() {
		req := proposal.Request
		view.Request = requestView{ID: req.ID, Action: req.Action, Status: req.Status, RequestedBy: req.RequestedBy, ExpiresAt: req.ExpiresAt,
			SubjectDigest: req.SubjectDigest, PolicyRevision: req.PolicyRevision, Nonce: req.Nonce, RiskSummary: req.RiskSummary,
			Diff: req.Diff, AffectedFeatures: req.AffectedFeatures, EvidenceRefs: req.EvidenceRefs, StepUp: req.StepUp}
		if !req.IssueID.IsZero() {
			view.Request.Issue = req.IssueID.String()
		}
		if d := proposal.Decision; d != nil {
			dv := &decisionView{Decision: d.Decision, Approver: d.ActorID, Reason: d.Reason, At: d.DecidedAt, CondExpires: d.Conditions.ExpiresAt}
			if d.Conditions.BudgetCap > 0 {
				dv.BudgetCap = money(d.Conditions.BudgetCap)
			}
			view.Decision = dv
		}
		view.Open = req.Status.Open()
		view.CanDecide = h.registered(approvalactions.ActionDecide)
	}
	h.render(w, r, statusOfPanels(panel), pageApproval, view)
}
