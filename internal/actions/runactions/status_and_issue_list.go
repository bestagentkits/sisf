package runactions

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/internal/actions"
	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// Bounds of one issue listing.
const (
	defaultIssueLimit = 100
	maxIssueLimit     = 500
)

// FactoryStatus is the overview every surface shows first.
type FactoryStatus struct {
	FactoryID     core.FactoryID `json:"factory_id"`
	GeneratedAt   time.Time      `json:"generated_at"`
	IssueTotal    int            `json:"issue_total"`
	IssueCounts   []StateCount   `json:"issue_counts"`
	Stopped       int            `json:"stopped"`
	OpenApprovals int            `json:"open_approvals"`
}

// StateCount is how many issues are in one lifecycle state.
type StateCount struct {
	State core.State `json:"state"`
	Count int        `json:"count"`
}

func (h handlers) factoryStatus(ctx context.Context, req core.ActionRequest) (core.ActionResult, error) {
	if _, err := actions.DecodeInput[struct{}](req); err != nil {
		return core.ActionResult{}, err
	}
	records, err := h.Records.Records(ctx, state.RecordFilter{})
	if err != nil {
		return core.ActionResult{}, err
	}
	open, err := h.Approvals.List(ctx, approvals.Filter{Statuses: []approvals.Status{approvals.StatusPending}})
	if err != nil {
		return core.ActionResult{}, err
	}
	counts := map[core.State]int{}
	for _, r := range records {
		counts[r.State]++
	}
	out := FactoryStatus{FactoryID: h.FactoryID, GeneratedAt: h.Clock.Now().UTC(), IssueTotal: len(records),
		Stopped: counts[core.StateBlocked] + counts[core.StateRolledBack], OpenApprovals: len(open)}
	for _, st := range core.States() {
		out.IssueCounts = append(out.IssueCounts, StateCount{State: st, Count: counts[st]})
	}
	return actions.JSONResult(out)
}

type issuesListInput struct {
	States []string `json:"states,omitempty"`
	Limit  int      `json:"limit,omitempty"`
}

// IssueList is one page of the workboard.
type IssueList struct {
	Total  int                `json:"total"`
	Issues []core.StateRecord `json:"issues"`
}

func (h handlers) issuesList(ctx context.Context, req core.ActionRequest) (core.ActionResult, error) {
	in, err := actions.DecodeInput[issuesListInput](req)
	if err != nil {
		return core.ActionResult{}, err
	}
	filter := state.RecordFilter{}
	for _, text := range in.States {
		st, err := core.ParseState(text)
		if err != nil {
			return core.ActionResult{}, err
		}
		if !slices.Contains(filter.States, st) {
			filter.States = append(filter.States, st)
		}
	}
	limit := in.Limit
	switch {
	case limit == 0:
		limit = defaultIssueLimit
	case limit < 0 || limit > maxIssueLimit:
		return core.ActionResult{}, core.NewError(core.CodeInvalidArgument, "limit must be from 1 to 500")
	}
	records, err := h.Records.Records(ctx, filter)
	if err != nil {
		return core.ActionResult{}, err
	}
	slices.SortFunc(records, func(a, b core.StateRecord) int {
		if c := b.UpdatedAt.Compare(a.UpdatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.IssueID.String(), b.IssueID.String())
	})
	out := IssueList{Total: len(records), Issues: records}
	if len(records) > limit {
		out.Issues = records[:limit]
	}
	if out.Issues == nil {
		out.Issues = []core.StateRecord{}
	}
	return actions.JSONResult(out)
}
