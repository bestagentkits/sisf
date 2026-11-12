package web

import (
	"net/http"
	"sort"
	"strings"

	"github.com/bestagentkits/sisf/internal/actions/pendingactions"
	"github.com/bestagentkits/sisf/pkg/core"
)

type productSection struct {
	Action string
	Title  string
	Panel  Panel
	Table  Table
}

type productView struct {
	Frame
	Sections []productSection
}

// product lists the feature catalog, the changelog and the docs, guides and
// decision history, each from its own action.
func (h *handler) product(w http.ResponseWriter, r *http.Request, s *session) {
	view := productView{Frame: h.frame(s, "product", "/product")}
	for _, sec := range []productSection{
		{Action: pendingactions.ActionFeaturesList, Title: "Features"},
		{Action: pendingactions.ActionChangelogList, Title: "Changelog"},
		{Action: pendingactions.ActionDocsList, Title: "Docs, guides, ADRs and decisions"},
	} {
		sec.Panel = h.read(r, s, sec.Action, nil)
		if sec.Panel.OK() {
			sec.Table = tableFrom(sec.Panel.Output)
		}
		view.Sections = append(view.Sections, sec)
	}
	h.render(w, r, http.StatusOK, pageProduct, view)
}

type securityView struct {
	Frame
	Budget       Panel
	Money        *moneyView
	Secrets      Panel
	SecretTable  Table
	Capabilities Table
	CanSetBudget bool
}

// security shows the budget, the secret references (never values) and what
// each role may run, read from the registry every surface shares.
func (h *handler) security(w http.ResponseWriter, r *http.Request, s *session) {
	view := securityView{Frame: h.frame(s, "security", "/security"), Capabilities: h.capabilities(),
		CanSetBudget: h.registered(actionBudgetSet)}
	view.Money, view.Budget = h.budgetView(r, s)
	view.Secrets = h.read(r, s, pendingactions.ActionSecretsListRefs, nil)
	if view.Secrets.OK() {
		view.SecretTable = tableFrom(view.Secrets.Output)
	}
	h.render(w, r, http.StatusOK, pageSecurity, view)
}

// capabilities is one row per role: the actions the role may run.
func (h *handler) capabilities() Table {
	byRole := map[core.Role][]string{}
	for _, spec := range h.cfg.Actions.Specs() {
		for _, role := range spec.Roles {
			label := spec.Name
			if spec.Sensitivity.NeedsApproval() {
				label += " (approval)"
			}
			byRole[role] = append(byRole[role], label)
		}
	}
	roles := make([]string, 0, len(byRole))
	for role := range byRole {
		roles = append(roles, string(role))
	}
	sort.Strings(roles)
	t := Table{Cols: []string{"Role", "May run"}}
	for _, role := range roles {
		t.Rows = append(t.Rows, []string{role, strings.Join(byRole[core.Role(role)], ", ")})
	}
	return t
}
