package runactions

import (
	"context"
	"time"

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

// QAHold is a held QA verdict as runs.explain cites it.
type QAHold struct {
	// AwaitsApproval is set when QA passed and the Owner must approve the
	// release; otherwise the run's last attempt failed QA.
	AwaitsApproval bool      `json:"awaits_approval"`
	Summary        string    `json:"summary"`
	Ref            string    `json:"ref"`
	At             time.Time `json:"at"`
}

// ReleaseHold is a held or refused release as runs.inspect and
// runs.explain show it.
type ReleaseHold struct {
	Result string `json:"result"`
	Code   string `json:"code"`
	// Summary is the public sentence the issue's progress comment shows;
	// Reason is the supervisor's own, shown only to a caller who holds the
	// Owner role and empty for every other.
	Summary string `json:"summary"`
	Reason  string `json:"reason,omitempty"`
	// Resumed is set once the Owner's retry let a held release start over.
	Resumed bool      `json:"resumed"`
	Ref     string    `json:"ref"`
	At      time.Time `json:"at"`
}

// ReleaseHolds reads the held or refused release of one evaluator attempt
// of a run, NOT_FOUND when its release was neither.
type ReleaseHolds interface {
	ReleaseHold(ctx context.Context, run core.RunID, attempt int) (ReleaseHold, error)
}

// RunStop is why the control zone blocked an issue whose run stopped
// without a candidate, as runs.explain cites it.
type RunStop struct {
	Cause string `json:"cause"`
	// Summary says why the issue is blocked and WayOut how the Owner gets
	// it going again; both are the sentences the progress comment shows.
	Summary string    `json:"summary"`
	WayOut  string    `json:"way_out"`
	Ref     string    `json:"ref"`
	At      time.Time `json:"at"`
}

// RunStops reads why the control zone blocked the issue of a run,
// NOT_FOUND when it did not block it for that run.
type RunStops interface {
	RunStop(ctx context.Context, run core.RunID) (RunStop, error)
}

// QAHolds reads the hold of one evaluator attempt of a run, NOT_FOUND when
// QA did not hold it.
type QAHolds interface {
	QAHold(ctx context.Context, run core.RunID, attempt int) (QAHold, error)
}

// Deps is what the actions read from. Every field but Clock, GitHub,
// QAHolds, CandidatePRs, ReleaseHolds and RunStops is required.
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
	// QAHolds reads the supervisor's QA holds; nil leaves them out.
	QAHolds QAHolds
	// CandidatePRs reads the candidates' pull requests; nil leaves them out.
	CandidatePRs CandidatePRs
	// ReleaseHolds reads the supervisor's held and refused releases; nil
	// leaves them out.
	ReleaseHolds ReleaseHolds
	// RunStops reads why an issue was blocked when its run stopped; nil
	// leaves it out.
	RunStops RunStops
	// QAReceipts reads the signed QA receipts runs.receipt hands out; nil
	// makes that action NOT_SUPPORTED.
	QAReceipts QAReceipts
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
		ActionRunsReceipt:   h.runsReceipt,
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
