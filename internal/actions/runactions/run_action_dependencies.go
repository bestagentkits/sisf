package runactions

import (
	"context"

	"github.com/bestagentkits/sisf/internal/dispatcher"
	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// Records reads issue lifecycle records.
type Records interface {
	// Record returns NOT_FOUND when the issue has no record.
	Record(ctx context.Context, issue core.IssueRef) (core.StateRecord, error)
	Records(ctx context.Context, filter state.RecordFilter) ([]core.StateRecord, error)
}

// RunRecords reads the dispatcher's record of a run; *dispatcher.Dispatcher
// is one. NOT_FOUND means the dispatcher holds no record of the run.
type RunRecords interface {
	RunRecord(ctx context.Context, run core.RunID) (dispatcher.RunRecord, error)
}

// ApprovalInbox reads approval requests and their decisions;
// *approvals.KVStore is one.
type ApprovalInbox interface {
	List(ctx context.Context, f approvals.Filter) ([]approvals.Request, error)
	Decision(ctx context.Context, requestID string) (approvals.Approval, error)
}

// Deps is what the actions read from. Every field but Clock is required.
type Deps struct {
	FactoryID core.FactoryID
	Records   Records
	Runs      RunRecords
	Approvals ApprovalInbox
	// Clock stamps the status overview; nil means the wall clock.
	Clock core.Clock
	// GitHub reports how the factory keeps up with GitHub for the status
	// overview; nil leaves it out.
	GitHub func(ctx context.Context) (GitHubSync, error)
}

// New returns the actions of this package over deps.
func New(deps Deps) ([]core.Action, error) {
	if deps.Records == nil || deps.Runs == nil || deps.Approvals == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "the run actions need issue records, run records and the approval inbox")
	}
	if err := deps.FactoryID.Validate(); err != nil {
		return nil, err
	}
	if deps.Clock == nil {
		deps.Clock = core.SystemClock{}
	}
	h := handlers{deps}
	byName := map[string]core.ActionHandler{
		ActionFactoryStatus: h.factoryStatus,
		ActionIssuesList:    h.issuesList,
		ActionRunsInspect:   h.runsInspect,
		ActionRunsExplain:   h.runsExplain,
	}
	specs := Specs()
	out := make([]core.Action, 0, len(specs))
	for _, spec := range specs {
		out = append(out, core.Action{Spec: spec, Handle: byName[spec.Name]})
	}
	return out, nil
}

type handlers struct{ Deps }

// StoreRecords reads issue records from the operational store, one read
// transaction per call.
func StoreRecords(store state.Store) Records { return storeRecords{store} }

type storeRecords struct{ store state.Store }

func (s storeRecords) Record(ctx context.Context, issue core.IssueRef) (core.StateRecord, error) {
	var record core.StateRecord
	err := s.store.View(ctx, func(ctx context.Context, tx state.Tx) error {
		var err error
		record, err = tx.GetRecord(ctx, issue)
		return err
	})
	return record, err
}

func (s storeRecords) Records(ctx context.Context, filter state.RecordFilter) ([]core.StateRecord, error) {
	var records []core.StateRecord
	err := s.store.View(ctx, func(ctx context.Context, tx state.Tx) error {
		var err error
		records, err = tx.ListRecords(ctx, filter)
		return err
	})
	return records, err
}
