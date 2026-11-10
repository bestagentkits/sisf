package web

import (
	"fmt"
	"net/http"
	"time"

	"github.com/bestagentkits/sisf/internal/actions/runactions"
	"github.com/bestagentkits/sisf/internal/budget"
	"github.com/bestagentkits/sisf/pkg/core"
)

// Names of the actions the dashboard offers as buttons. The stop and the
// rollback are requested like any sensitive write: planned, approved by the
// Owner, then executed, never done directly.
const (
	actionStop     = "factory.emergency_stop"
	actionRollback = "deployments.rollback"
)

type statsView struct {
	FactoryID     core.FactoryID
	Generated     time.Time
	Total         int
	Stopped       int
	OpenApprovals int
	Counts        []runactions.StateCount
}

// moneyView is the budget as a page shows it.
type moneyView struct {
	Confirmed  bool
	Mode       core.BudgetMode
	Cap        string
	Alert      string
	Reserved   string
	Verified   string
	Unverified string
	Available  string
	Warnings   []string
}

type overviewView struct {
	Frame
	Status      Panel
	Stats       *statsView
	Budget      Panel
	Money       *moneyView
	CanStop     bool
	CanRollback bool
}

func (h *handler) overview(w http.ResponseWriter, r *http.Request, s *session) {
	view := overviewView{Frame: h.frame(s, "overview", "/"), CanStop: h.registered(actionStop), CanRollback: h.registered(actionRollback)}
	var status runactions.FactoryStatus
	status, view.Status = decode[runactions.FactoryStatus](h.read(r, s, runactions.ActionFactoryStatus, nil))
	if view.Status.OK() {
		view.Stats = &statsView{FactoryID: status.FactoryID, Generated: status.GeneratedAt, Total: status.IssueTotal,
			Stopped: status.Stopped, OpenApprovals: status.OpenApprovals, Counts: status.IssueCounts}
	}
	view.Money, view.Budget = h.budgetView(r, s)
	h.render(w, r, http.StatusOK, pageOverview, view)
}

// budgetView reads budget.get once for the pages that show the budget.
func (h *handler) budgetView(r *http.Request, s *session) (*moneyView, Panel) {
	st, panel := decode[budget.Status](h.read(r, s, budget.ActionGet, nil))
	if !panel.OK() {
		return nil, panel
	}
	m := &moneyView{Confirmed: st.Confirmed, Mode: st.Mode, Reserved: money(st.ReservedMicros), Verified: money(st.SpentVerifiedMicros),
		Unverified: money(st.SpentUnverifiedMicros), Warnings: st.Warnings}
	if st.Settings.CapMicros > 0 {
		m.Cap = money(st.Settings.CapMicros)
	}
	if st.Settings.AlertThresholdMicros > 0 {
		m.Alert = money(st.Settings.AlertThresholdMicros)
	}
	if st.AvailableMicros != nil {
		m.Available = money(*st.AvailableMicros)
	}
	return m, panel
}

// money shows an amount held in millionths of a currency unit.
func money(m core.Micros) string {
	return fmt.Sprintf("%d.%06d", int64(m)/1_000_000, int64(m)%1_000_000)
}
