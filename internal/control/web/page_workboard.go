package web

import (
	"net/http"
	"time"

	"github.com/bestagentkits/sisf/internal/actions/runactions"
	"github.com/bestagentkits/sisf/pkg/core"
)

type boardRow struct {
	Issue   string
	State   core.State
	Attempt int
	RunID   core.RunID
	Updated time.Time
	Age     string
	Stopped bool
}

type workboardView struct {
	Frame
	List     Panel
	States   []core.State
	Selected string
	Total    int
	Shown    int
	Rows     []boardRow
}

func (h *handler) workboard(w http.ResponseWriter, r *http.Request, s *session) {
	selected := r.URL.Query()["state"]
	input := map[string]any{}
	if len(selected) > 0 {
		input["states"] = selected
	}
	list, panel := decode[runactions.IssueList](h.read(r, s, runactions.ActionIssuesList, input))
	view := workboardView{Frame: h.frame(s, "workboard", "/workboard"), List: panel, States: core.States(), Total: list.Total, Shown: len(list.Issues)}
	if len(selected) == 1 {
		view.Selected = selected[0]
	}
	now := h.cfg.Clock.Now()
	for _, rec := range list.Issues {
		view.Rows = append(view.Rows, boardRow{Issue: rec.IssueID.String(), State: rec.State, Attempt: rec.Attempt, RunID: rec.RunID,
			Updated: rec.UpdatedAt, Age: age(now.Sub(rec.UpdatedAt)), Stopped: rec.State == core.StateBlocked || rec.State == core.StateRolledBack})
	}
	status := http.StatusOK
	if !panel.OK() && panel.Code == core.CodeInvalidArgument {
		status = http.StatusBadRequest
	}
	h.render(w, r, status, pageWorkboard, view)
}

// age shows how long ago something happened, in its largest whole unit.
func age(d time.Duration) string {
	switch {
	case d < 0:
		return "just now"
	case d < time.Minute:
		return "under a minute"
	case d < time.Hour:
		return plural(int(d/time.Minute), "minute")
	case d < 48*time.Hour:
		return plural(int(d/time.Hour), "hour")
	}
	return plural(int(d/(24*time.Hour)), "day")
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return itoa(n) + " " + unit + "s"
}
