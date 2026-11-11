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
	// GitHub is how the factory keeps up with GitHub; nil when the
	// surface was built without it.
	GitHub *GitHubSync `json:"github,omitempty"`
}

// GitHubSync is how the factory keeps up with its repository on GitHub:
// the reconciliation that finds the issue changes no webhook delivered and,
// on an install acting with the operator's own token, that token's access.
type GitHubSync struct {
	// Position is where the next poll reads from, in GitHub's time: the
	// newest issue update stored, or the start of a second read again.
	Position time.Time `json:"position,omitzero"`
	// LastPollAt is when GitHub last answered a reconciliation poll.
	LastPollAt time.Time `json:"last_poll_at,omitzero"`
	// Backlog is true when the last poll stopped at its bound with more
	// changes waiting; the next polls continue with them.
	Backlog bool `json:"backlog"`
	// LastError is why the last poll failed; "" when it did not.
	LastError string `json:"last_error,omitempty"`
	// Retrying are issue changes that could not be stored and are read
	// again; Skipped are those given up after a bounded number of polls.
	Retrying []SyncIssue `json:"retrying,omitempty"`
	Skipped  []SyncIssue `json:"skipped,omitempty"`
	// TokenAccess is "confirmed" or "revoked" on an install that acts with
	// the operator's token, "" on one with a GitHub App installation. While
	// it is revoked the factory does nothing on GitHub.
	TokenAccess string `json:"token_access,omitempty"`
	// TokenAccessDetail says why access is revoked, or that the last check
	// could not reach GitHub.
	TokenAccessDetail string    `json:"token_access_detail,omitempty"`
	TokenCheckedAt    time.Time `json:"token_checked_at,omitzero"`
	// ParkingDeferredSince is when the earliest unanswered issue became
	// due to be parked, while its parking waits until every answer given
	// on GitHub is known to be taken in; ParkingDeferredReason says why it
	// waits. Zero and "" when no parking waits.
	ParkingDeferredSince  time.Time `json:"parking_deferred_since,omitzero"`
	ParkingDeferredReason string    `json:"parking_deferred_reason,omitempty"`
	// ReportsWaiting counts the bug reports from the public report route
	// not yet published as GitHub issues for triage, ReportsFailed those
	// given up after GitHub refused them; ReportsPublishPausedUntil is when
	// publishing goes on while the daily budget of report issues is spent,
	// zero while it is not; ReportPublishError says why the last
	// publication failed, "" when it did not.
	ReportsWaiting            int       `json:"reports_waiting"`
	ReportsFailed             int       `json:"reports_failed"`
	ReportsPublishPausedUntil time.Time `json:"reports_publish_paused_until,omitzero"`
	ReportPublishError        string    `json:"report_publish_error,omitempty"`
}

// SyncIssue is one issue change reconciliation could not store.
type SyncIssue struct {
	Issue     int       `json:"issue"`
	UpdatedAt time.Time `json:"updated_at"`
	Attempts  int       `json:"attempts"`
	Reason    string    `json:"reason"`
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
	if h.GitHub != nil {
		sync, err := h.GitHub(ctx)
		if err != nil {
			return core.ActionResult{}, err
		}
		out.GitHub = &sync
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
