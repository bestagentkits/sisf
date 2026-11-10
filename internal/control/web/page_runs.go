package web

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/internal/actions/runactions"
	"github.com/bestagentkits/sisf/internal/dispatcher"
	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/core"
)

func itoa(n int) string { return strconv.Itoa(n) }

type graphNode struct {
	Name    core.State
	Current bool
}

type runView struct {
	Status       dispatcher.RunStatus
	Attempt      int
	ClaimedAt    time.Time
	UpdatedAt    time.Time
	Agent        string
	Failures     []dispatcher.AttemptFailure
	Artifacts    []agent.Artifact
	PullRequests []string
}

type runsView struct {
	Frame
	Issue       string
	List        Panel
	Recent      []string
	Inspect     Panel
	Record      core.StateRecord
	Run         *runView
	Graph       []graphNode
	Explain     Panel
	Explanation runactions.Explanation
	Evidence    Table
}

func (h *handler) runs(w http.ResponseWriter, r *http.Request, s *session) {
	issue := strings.TrimSpace(r.URL.Query().Get("issue"))
	view := runsView{Frame: h.frame(s, "runs", "/runs"), Issue: issue}
	if issue == "" {
		list, panel := decode[runactions.IssueList](h.read(r, s, runactions.ActionIssuesList, map[string]any{"limit": 25}))
		view.List = panel
		for _, rec := range list.Issues {
			view.Recent = append(view.Recent, rec.IssueID.String())
		}
		h.render(w, r, http.StatusOK, pageRuns, view)
		return
	}
	target := map[string]string{"issue": issue}
	var inspection runactions.Inspection
	inspection, view.Inspect = decode[runactions.Inspection](h.read(r, s, runactions.ActionRunsInspect, target))
	if view.Inspect.OK() {
		view.Record = inspection.Record
		for _, st := range core.States() {
			view.Graph = append(view.Graph, graphNode{Name: st, Current: st == inspection.Record.State})
		}
		if run := inspection.Run; run != nil {
			rv := &runView{Status: run.Status, Attempt: run.Attempt, ClaimedAt: run.ClaimedAt, UpdatedAt: run.UpdatedAt,
				Failures: run.Failures, Artifacts: run.Artifacts, PullRequests: run.PullRequests}
			if run.Agent != nil {
				rv.Agent = run.Agent.Backend + " " + run.Agent.ID
			}
			view.Run = rv
		}
	}
	view.Explanation, view.Explain = decode[runactions.Explanation](h.read(r, s, runactions.ActionRunsExplain, target))
	view.Evidence = evidenceTable(view.Explanation)
	h.render(w, r, statusOfPanels(view.Inspect, view.Explain), pageRuns, view)
}

// statusOfPanels is 404 when an issue is not known, so a mistyped reference
// is not mistaken for an empty page.
func statusOfPanels(panels ...Panel) int {
	for _, p := range panels {
		if p.Code == core.CodeNotFound {
			return http.StatusNotFound
		}
		if p.Code == core.CodeInvalidArgument {
			return http.StatusBadRequest
		}
	}
	return http.StatusOK
}

func evidenceTable(e runactions.Explanation) Table {
	t := Table{Cols: []string{"Kind", "Reference", "Summary", "At"}}
	for _, ev := range e.Evidence {
		at := ""
		if !ev.At.IsZero() {
			at = ev.At.UTC().Format("2006-01-02 15:04:05 UTC")
		}
		t.Rows = append(t.Rows, []string{ev.Kind, ev.Ref, ev.Summary, at})
	}
	return t
}
