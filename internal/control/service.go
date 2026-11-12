package control

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

const (
	// DefaultIssueLimit and MaxIssueLimit bound one workboard query.
	DefaultIssueLimit = 100
	MaxIssueLimit     = 500

	// pendingDeliveryCap bounds the inbox read behind the delivery count.
	pendingDeliveryCap = 1000
)

// Service answers the questions every surface asks. The API and the dashboard
// call these methods and render what they return, so the two cannot disagree.
// It only reads: it holds no write path into the lifecycle.
type Service struct {
	factory core.FactoryID
	store   state.Store
	health  *healthChecker
	clock   core.Clock
}

// NewService builds the read service for one factory. Probes check Zones 1 and
// 2; the control zone is checked through the store the service itself uses.
func NewService(factory core.FactoryID, store state.Store, probes map[core.Zone]HealthProbe, probeTimeout time.Duration, clock core.Clock) (*Service, error) {
	if err := factory.Validate(); err != nil {
		return nil, err
	}
	if store == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "the control service needs a state store")
	}
	if clock == nil {
		clock = core.SystemClock{}
	}
	health, err := newHealthChecker(store, probes, probeTimeout)
	if err != nil {
		return nil, err
	}
	return &Service{factory: factory, store: store, health: health, clock: clock}, nil
}

// Health reports the three zones.
func (s *Service) Health(ctx context.Context) []ZoneHealth { return s.health.check(ctx) }

// FactoryStatus is the overview: zone health, issue counts and the inbox backlog.
func (s *Service) FactoryStatus(ctx context.Context) (FactoryStatus, error) {
	status := FactoryStatus{
		FactoryID: s.factory, Zones: s.Health(ctx), GeneratedAt: s.clock.Now().UTC(),
		Healthy: true,
	}
	for _, z := range status.Zones {
		status.Healthy = status.Healthy && z.Status == HealthOK
	}
	counts := map[core.State]int{}
	err := s.store.View(ctx, func(ctx context.Context, tx state.Tx) error {
		records, err := tx.ListRecords(ctx, state.RecordFilter{})
		if err != nil {
			return err
		}
		for _, r := range records {
			counts[r.State]++
		}
		status.IssueTotal = len(records)
		pending, err := tx.PendingDeliveries(ctx, pendingDeliveryCap)
		status.PendingDeliveries = len(pending)
		return err
	})
	if err != nil {
		return FactoryStatus{}, asServiceError(err, "the factory status could not be read")
	}
	for _, st := range core.States() {
		status.IssueCounts = append(status.IssueCounts, StateCount{State: st, Count: counts[st]})
	}
	return status, nil
}

// IssueQuery selects issues for the workboard. Zero values select everything
// up to the default limit.
type IssueQuery struct {
	States []core.State
	Limit  int
}

// ParseIssueQuery reads the state and limit parameters shared by the API and
// the dashboard, so both accept exactly the same questions.
func ParseIssueQuery(params map[string][]string) (IssueQuery, error) {
	var q IssueQuery
	for _, text := range params["state"] {
		st, err := core.ParseState(text)
		if err != nil {
			return IssueQuery{}, core.NewError(core.CodeInvalidArgument,
				fmt.Sprintf("state %q is not a lifecycle state", clip(text, 40)),
				core.NextAction{Action: "issues.list", Description: "retry with one of: " + joinStates(core.States())})
		}
		if !slices.Contains(q.States, st) {
			q.States = append(q.States, st)
		}
	}
	if values := params["limit"]; len(values) > 0 {
		n, err := strconv.Atoi(values[0])
		if err != nil || n < 1 || n > MaxIssueLimit {
			return IssueQuery{}, core.NewError(core.CodeInvalidArgument,
				fmt.Sprintf("limit must be a whole number from 1 to %d", MaxIssueLimit),
				core.NextAction{Action: "issues.list", Description: "retry with a limit in range"})
		}
		q.Limit = n
	}
	return q, nil
}

func joinStates(states []core.State) string {
	names := make([]string, len(states))
	for i, st := range states {
		names[i] = string(st)
	}
	return strings.Join(names, ", ")
}

// ListIssues returns the issues matching q, most recently changed first.
func (s *Service) ListIssues(ctx context.Context, q IssueQuery) (IssueList, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultIssueLimit
	}
	var list IssueList
	err := s.store.View(ctx, func(ctx context.Context, tx state.Tx) error {
		records, err := tx.ListRecords(ctx, state.RecordFilter{States: q.States})
		if err != nil {
			return err
		}
		slices.SortFunc(records, func(a, b core.StateRecord) int {
			if c := b.UpdatedAt.Compare(a.UpdatedAt); c != 0 {
				return c
			}
			return strings.Compare(a.IssueID.String(), b.IssueID.String())
		})
		list.Total = len(records)
		if len(records) > limit {
			records = records[:limit]
		}
		list.Issues = make([]IssueSummary, 0, len(records))
		for _, r := range records {
			view, _, err := readProjection(ctx, tx, r.IssueID)
			if err != nil {
				return err
			}
			list.Issues = append(list.Issues, summarize(r, view))
		}
		return nil
	})
	if err != nil {
		return IssueList{}, asServiceError(err, "the issues could not be read")
	}
	return list, nil
}

// GetIssue returns one issue by its reference, or NOT_FOUND.
func (s *Service) GetIssue(ctx context.Context, ref core.IssueRef) (IssueDetail, error) {
	var detail IssueDetail
	err := s.store.View(ctx, func(ctx context.Context, tx state.Tx) error {
		record, err := tx.GetRecord(ctx, ref)
		if err != nil {
			return err
		}
		view, _, err := readProjection(ctx, tx, ref)
		if err != nil {
			return err
		}
		detail = IssueDetail{
			IssueSummary: summarize(record, view), FactoryID: record.FactoryID,
			FencingToken: record.FencingToken, Author: view.Author, GitHubState: view.GitHubState,
		}
		for _, c := range view.Comments {
			detail.Comments = append(detail.Comments, IssueComment(c))
		}
		return nil
	})
	if core.HasCode(err, core.CodeNotFound) {
		return IssueDetail{}, core.NewError(core.CodeNotFound, "no issue with this id is tracked",
			core.NextAction{Action: "issues.list", Description: "list the tracked issues to find a valid id"})
	}
	if err != nil {
		return IssueDetail{}, asServiceError(err, "the issue could not be read")
	}
	return detail, nil
}

// summarize joins an operational record with the stored view of its issue.
func summarize(r core.StateRecord, v issueProjection) IssueSummary {
	return IssueSummary{
		ID: r.IssueID.String(), Owner: r.IssueID.Owner, Repo: r.IssueID.Repo, Number: r.IssueID.Number,
		Title: v.Title, State: r.State, Version: r.Version, Attempt: r.Attempt, RunID: r.RunID,
		LeaseOwner: r.LeaseOwner, Labels: v.Labels, UpdatedAt: r.UpdatedAt.UTC(),
	}
}

// asServiceError keeps a typed error as it is and turns anything else into
// an INTERNAL error with a fixed message, so a driver message never reaches a
// caller. The original stays attached for the operator's log.
func asServiceError(err error, message string) error {
	var typed *core.Error
	if errors.As(err, &typed) {
		return err
	}
	return core.NewError(core.CodeInternal, message).WithCause(err)
}
