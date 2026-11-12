package web

import (
	"net/http"
	"strings"

	"github.com/bestagentkits/sisf/internal/actions/runactions"
)

type blockedView struct {
	Frame
	Issue       string
	Explain     Panel
	Explanation runactions.Explanation
	Evidence    Table
	Next        []NextView
}

// blocked explains in plain words why an issue's run is stopped, with the
// evidence and the next valid steps that runs.explain returns.
func (h *handler) blocked(w http.ResponseWriter, r *http.Request, s *session) {
	issue := strings.TrimSpace(r.URL.Query().Get("issue"))
	view := blockedView{Frame: h.frame(s, "workboard", "/blocked"), Issue: issue}
	status := http.StatusOK
	if issue != "" {
		view.Explanation, view.Explain = decode[runactions.Explanation](h.read(r, s, runactions.ActionRunsExplain, map[string]string{"issue": issue}))
		view.Evidence = evidenceTable(view.Explanation)
		view.Next = nextViews(view.Explanation.NextActions)
		status = statusOfPanels(view.Explain)
	}
	h.render(w, r, status, pageBlocked, view)
}
