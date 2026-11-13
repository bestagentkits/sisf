package state_test

import (
	"context"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
	"github.com/bestagentkits/sisf/pkg/state"
)

func TestIssueLeaseResourceIsStableAndDistinctPerIssue(t *testing.T) {
	a := core.IssueRef{Owner: "acme", Repo: "app", Number: 7}
	b := core.IssueRef{Owner: "acme", Repo: "app", Number: 8}
	if state.IssueLeaseResource(a) != "issue:acme/app#7" {
		t.Fatalf("IssueLeaseResource = %q", state.IssueLeaseResource(a))
	}
	if state.IssueLeaseResource(a) == state.IssueLeaseResource(b) {
		t.Fatal("two issues must not share a lease")
	}
}

func TestLeaseIsHeldOnlyBeforeItsExpiry(t *testing.T) {
	expires := time.Date(2026, 11, 10, 6, 0, 0, 0, time.UTC)
	lease := state.Lease{Resource: "dispatcher", Owner: "dispatcher-1", Token: 3, ExpiresAt: expires}
	if lease.Expired(expires.Add(-time.Nanosecond)) {
		t.Error("the lease is held just before its expiry")
	}
	if !lease.Expired(expires) || !lease.Expired(expires.Add(time.Hour)) {
		t.Error("the lease is lost at and after its expiry")
	}
	if !(state.Lease{}).Expired(expires) {
		t.Error("a lease that was never granted is not held")
	}
}

func TestMigrationsMustBeNumberedWithoutGaps(t *testing.T) {
	good := []state.Migration{
		{Version: 1, Name: "initial schema", BackwardCompatible: true},
		{Version: 2, Name: "add outbox retry columns", BackwardCompatible: true},
		{Version: 3, Name: "drop legacy column"},
	}
	if err := state.ValidateMigrations(good); err != nil {
		t.Fatalf("valid migrations rejected: %v", err)
	}
	if err := state.ValidateMigrations(nil); err != nil {
		t.Fatalf("an empty list is valid: %v", err)
	}
	for name, ms := range map[string][]state.Migration{
		"starts at zero": {{Version: 0, Name: "x"}},
		"gap":            {{Version: 1, Name: "a"}, {Version: 3, Name: "c"}},
		"repeat":         {{Version: 1, Name: "a"}, {Version: 1, Name: "b"}},
		"out of order":   {{Version: 2, Name: "b"}, {Version: 1, Name: "a"}},
		"unnamed":        {{Version: 1}},
	} {
		if core.CodeOf(state.ValidateMigrations(ms)) != core.CodeInvalidArgument {
			t.Errorf("%s: want %s, got %v", name, core.CodeInvalidArgument, state.ValidateMigrations(ms))
		}
	}
	incompatible := state.Incompatible(good)
	if len(incompatible) != 1 || incompatible[0].Version != 3 {
		t.Fatalf("Incompatible = %+v", incompatible)
	}
	if len(state.Incompatible(good[:2])) != 0 {
		t.Fatal("compatible migrations must not be reported")
	}
}

// txStub proves a transaction can be implemented with the exported types
// alone and that it serves every narrow role, including the budget ledger.
type txStub struct{ deliveries map[string]bool }

func (txStub) GetRecord(context.Context, core.IssueRef) (core.StateRecord, error) {
	return core.StateRecord{}, core.NewError(core.CodeNotFound, "no record")
}
func (txStub) ListRecords(context.Context, state.RecordFilter) ([]core.StateRecord, error) {
	return nil, nil
}
func (txStub) Transition(_ context.Context, t core.Transition) (core.StateRecord, error) {
	if t.ExpectedVersion != 0 {
		return core.StateRecord{}, core.NewError(core.CodeConflict, "version moved on")
	}
	return core.StateRecord{IssueID: t.IssueID, State: t.To, Version: 1}, nil
}
func (txStub) AcquireLease(_ context.Context, resource, owner string, _ time.Duration) (state.Lease, error) {
	return state.Lease{Resource: resource, Owner: owner, Token: 1}, nil
}
func (txStub) RenewLease(_ context.Context, l state.Lease, _ time.Duration) (state.Lease, error) {
	return l, nil
}
func (txStub) ReleaseLease(context.Context, state.Lease) error { return nil }
func (txStub) CheckFencing(_ context.Context, _ string, token core.FencingToken) error {
	if token < 2 {
		return core.NewError(core.CodeStaleFencingToken, "a newer holder exists")
	}
	return nil
}
func (s txStub) AcceptDelivery(_ context.Context, e state.InboxEntry) (bool, error) {
	key := e.Source + "/" + e.DeliveryID
	if s.deliveries[key] {
		return false, nil
	}
	s.deliveries[key] = true
	return true, nil
}
func (txStub) PendingDeliveries(context.Context, int) ([]state.InboxEntry, error) { return nil, nil }
func (txStub) MarkDeliveryProcessed(context.Context, string, string) error        { return nil }
func (txStub) Enqueue(context.Context, state.OutboxMessage) error                 { return nil }
func (txStub) PendingOutbox(context.Context, int) ([]state.OutboxMessage, error)  { return nil, nil }
func (txStub) MarkOutboxSent(context.Context, int64) error                        { return nil }
func (txStub) MarkOutboxFailed(context.Context, int64, time.Time, string) error   { return nil }
func (txStub) PutReservation(context.Context, core.Reservation) error             { return nil }
func (txStub) GetReservation(context.Context, string) (core.Reservation, error) {
	return core.Reservation{}, core.NewError(core.CodeNotFound, "no reservation")
}
func (txStub) AppendUsage(context.Context, core.Usage) error { return nil }
func (txStub) RunUsage(context.Context, core.RunID) (core.RunUsage, error) {
	return core.RunUsage{}, nil
}
func (txStub) BudgetTotals(context.Context, time.Time) (core.BudgetTotals, error) {
	return core.BudgetTotals{}, nil
}
func (txStub) GetKV(context.Context, string, string) (state.KVEntry, error) {
	return state.KVEntry{}, core.NewError(core.CodeNotFound, "no entry")
}
func (txStub) PutKV(_ context.Context, e state.KVEntry) (state.KVEntry, error) {
	e.Version++
	return e, nil
}
func (txStub) ListKV(context.Context, string, string, int) ([]state.KVEntry, error) { return nil, nil }
func (txStub) DeleteKV(context.Context, string, string, int64) error                { return nil }

type storeStub struct{ tx txStub }

func (s storeStub) Update(ctx context.Context, fn func(context.Context, state.Tx) error) error {
	return fn(ctx, s.tx)
}
func (s storeStub) View(ctx context.Context, fn func(context.Context, state.Tx) error) error {
	return fn(ctx, s.tx)
}
func (storeStub) Close() error { return nil }

func TestTransactionServesEveryNarrowRole(t *testing.T) {
	var store state.Store = storeStub{tx: txStub{deliveries: map[string]bool{}}}
	issue := core.IssueRef{Owner: "acme", Repo: "app", Number: 7}
	err := store.Update(context.Background(), func(ctx context.Context, tx state.Tx) error {
		var (
			records state.Records     = tx
			leases  state.Leases      = tx
			inbox   state.Inbox       = tx
			_       state.Outbox      = tx
			_       state.KV          = tx
			_       core.BudgetLedger = tx
		)
		first, err := inbox.AcceptDelivery(ctx, state.InboxEntry{Source: "github", DeliveryID: "d-1"})
		if err != nil || !first {
			t.Errorf("first delivery = %v, %v", first, err)
		}
		again, err := inbox.AcceptDelivery(ctx, state.InboxEntry{Source: "github", DeliveryID: "d-1"})
		if err != nil || again {
			t.Errorf("a repeated delivery must be reported as already seen, got %v, %v", again, err)
		}
		lease, err := leases.AcquireLease(ctx, state.IssueLeaseResource(issue), "dispatcher-1", time.Minute)
		if err != nil {
			return err
		}
		if err := leases.CheckFencing(ctx, lease.Resource, lease.Token); !core.HasCode(err, core.CodeStaleFencingToken) {
			t.Errorf("a superseded holder must be fenced off, got %v", err)
		}
		_, err = records.Transition(ctx, core.Transition{IssueID: issue, To: core.StateIntake, ExpectedVersion: 4})
		return err
	})
	if !core.HasCode(err, core.CodeConflict) {
		t.Fatalf("a stale version must surface as CONFLICT, got %v", err)
	}
	var _ state.Migrator = migratorStub{}
}

type migratorStub struct{}

func (migratorStub) SchemaVersion(context.Context) (int, error)         { return 0, nil }
func (migratorStub) Pending(context.Context) ([]state.Migration, error) { return nil, nil }
func (migratorStub) Migrate(context.Context, state.MigrateOptions) error {
	return core.NewError(core.CodeApprovalRequired, "a migration that is not backward compatible needs the Owner")
}

// heldIssue is the record store of one issue, written the way the Records
// contract prescribes: every check is one call into the lifecycle package.
// It shows that the contract can be met and what a conforming store refuses.
type heldIssue struct {
	txStub
	record core.StateRecord
	// latest is the newest fencing token issued for the lease on the issue.
	latest core.FencingToken
}

func (h *heldIssue) Transition(_ context.Context, t core.Transition) (core.StateRecord, error) {
	if err := statemachine.CheckEdge(t); err != nil {
		return core.StateRecord{}, err
	}
	if h.record.Version != t.ExpectedVersion || h.record.State != t.From {
		return core.StateRecord{}, core.NewError(core.CodeConflict, "the record moved on")
	}
	if err := statemachine.CheckLease(t); err != nil {
		return core.StateRecord{}, err
	}
	if t.FencingToken != 0 && t.FencingToken != h.latest {
		return core.StateRecord{}, core.NewError(core.CodeStaleFencingToken, "a newer holder exists")
	}
	if err := statemachine.CheckRecordBinding(h.record, t); err != nil {
		return core.StateRecord{}, err
	}
	next := h.record
	next.State, next.Version, next.IdempotencyKey = t.To, h.record.Version+1, t.IdempotencyKey
	next.LeaseOwner, next.FencingToken, next.Attempt = t.LeaseOwner, t.FencingToken, t.Attempt
	switch t.Event {
	case core.EventClaimed:
		next.RunID, next.CandidateDigest, next.ReleaseDigest = t.RunID, "", ""
	case core.EventCandidateSubmitted:
		next.CandidateDigest = t.CandidateDigest
	case core.EventRolloutStarted:
		next.ReleaseDigest = t.ReleaseDigest
	}
	h.record = next
	return next, nil
}

const (
	build      = core.Digest("sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	otherBuild = core.Digest("sha256:fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210")
)

// developing is an issue a run is working on. Its first worker held token 5
// and lost the lease; the current holder has token 6.
func developing() *heldIssue {
	return &heldIssue{latest: 6, record: core.StateRecord{
		FactoryID: "acme-factory", IssueID: core.IssueRef{Owner: "acme", Repo: "app", Number: 7},
		State: core.StateDeveloping, Version: 3, IdempotencyKey: "start:run-0001",
		LeaseOwner: "worker-2", FencingToken: 6, Attempt: 1, RunID: "run-0001",
		UpdatedAt: time.Date(2026, 11, 10, 6, 0, 0, 0, time.UTC),
	}}
}

// stepOf builds the next step of the issue, written by its current holder.
func stepOf(h *heldIssue, event core.Event, to core.State) core.Transition {
	return core.Transition{
		IssueID: h.record.IssueID, From: h.record.State, To: to, Event: event,
		ExpectedVersion: h.record.Version, IdempotencyKey: string(event) + ":" + string(to),
		LeaseOwner: "worker-2", FencingToken: h.latest, RunID: h.record.RunID, Attempt: h.record.Attempt,
	}
}

func TestAWorkerThatLostItsLeaseCannotWriteWithItsOldTokenOrWithNone(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	hostile := map[string]func(*core.Transition){
		"its old token":            func(w *core.Transition) { w.LeaseOwner, w.FencingToken = "worker-1", 5 },
		"no token at all":          func(w *core.Transition) { w.LeaseOwner, w.FencingToken = "", 0 },
		"its name and token zero":  func(w *core.Transition) { w.LeaseOwner, w.FencingToken = "worker-1", 0 },
		"a token not yet issued":   func(w *core.Transition) { w.LeaseOwner, w.FencingToken = "worker-1", 7 },
		"the token without a name": func(w *core.Transition) { w.LeaseOwner = "" },
	}
	for name, mutate := range hostile {
		issue := developing()
		write := stepOf(issue, core.EventCandidateSubmitted, core.StateQAEvaluation)
		write.CandidateDigest = build
		mutate(&write)
		if _, err := issue.Transition(ctx, write); !core.HasCode(err, core.CodeStaleFencingToken) {
			t.Errorf("a write with %s: want %s, got %v", name, core.CodeStaleFencingToken, err)
		}
		if issue.record.State != core.StateDeveloping || issue.record.Version != 3 {
			t.Errorf("a write with %s changed the record: %+v", name, issue.record)
		}
	}

	issue := developing()
	write := stepOf(issue, core.EventCandidateSubmitted, core.StateQAEvaluation)
	write.CandidateDigest = build
	written, err := issue.Transition(ctx, write)
	if err != nil || written.State != core.StateQAEvaluation || written.CandidateDigest != build {
		t.Fatalf("the current holder could not submit its candidate: %+v, %v", written, err)
	}
}

func TestARecordStaysBoundToItsRunCandidateAndRelease(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	issue := developing()
	refused := func(name string, write core.Transition, code core.ErrorCode) {
		t.Helper()
		before := issue.record
		if _, err := issue.Transition(ctx, write); !core.HasCode(err, code) {
			t.Errorf("%s: want %s, got %v", name, code, err)
		}
		if issue.record != before {
			t.Errorf("%s: the refused write changed the record", name)
		}
	}
	accepted := func(name string, write core.Transition) {
		t.Helper()
		if _, err := issue.Transition(ctx, write); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}

	foreign := stepOf(issue, core.EventCandidateSubmitted, core.StateQAEvaluation)
	foreign.RunID, foreign.CandidateDigest = "run-0002", build
	refused("another run submits a candidate", foreign, core.CodeConflict)

	submit := stepOf(issue, core.EventCandidateSubmitted, core.StateQAEvaluation)
	submit.CandidateDigest = build
	accepted("the run submits its candidate", submit)

	swapped := stepOf(issue, core.EventQAPassed, core.StateReleaseCandidate)
	swapped.CandidateDigest = otherBuild
	refused("QA passes another candidate than the one submitted", swapped, core.CodeConflict)

	pass := stepOf(issue, core.EventQAPassed, core.StateReleaseCandidate)
	pass.CandidateDigest = build
	accepted("QA passes the submitted candidate", pass)

	rollout := stepOf(issue, core.EventRolloutStarted, core.StateCanary)
	rollout.CandidateDigest, rollout.ReleaseDigest = build, core.ReleaseDigest(build)
	accepted("the rollout of the candidate starts", rollout)
	if issue.record.CandidateDigest != build || issue.record.ReleaseDigest != core.ReleaseDigest(build) {
		t.Fatalf("the record does not keep the candidate and the release: %+v", issue.record)
	}

	other := stepOf(issue, core.EventRolloutHealthy, core.StateMonitoring)
	other.ReleaseDigest = core.ReleaseDigest(otherBuild)
	refused("another release than the one rolled out is reported healthy", other, core.CodeConflict)

	shortcut := stepOf(issue, core.EventIssueClosed, core.StateClosed)
	refused("the issue is closed from Canary", shortcut, core.CodeInvalidTransition)
}

func TestANewClaimStartsANewRunAndForgetsTheOldCandidate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	issue := developing()
	issue.record.State, issue.record.CandidateDigest = core.StateReady, build
	issue.record.ReleaseDigest = core.ReleaseDigest(build)

	again := stepOf(issue, core.EventClaimed, core.StateClaimed)
	if _, err := issue.Transition(ctx, again); !core.HasCode(err, core.CodeConflict) {
		t.Fatalf("a claim that reuses the run of the last claim: want %s, got %v", core.CodeConflict, err)
	}
	unleased := stepOf(issue, core.EventClaimed, core.StateClaimed)
	unleased.RunID, unleased.LeaseOwner, unleased.FencingToken = "run-0002", "", 0
	if _, err := issue.Transition(ctx, unleased); !core.HasCode(err, core.CodeStaleFencingToken) {
		t.Fatalf("a claim without a lease: want %s, got %v", core.CodeStaleFencingToken, err)
	}

	claim := stepOf(issue, core.EventClaimed, core.StateClaimed)
	claim.RunID = "run-0002"
	written, err := issue.Transition(ctx, claim)
	if err != nil {
		t.Fatalf("a claim that starts a new run was refused: %v", err)
	}
	if written.RunID != "run-0002" || written.CandidateDigest != "" || written.ReleaseDigest != "" {
		t.Fatalf("the new run inherited the old run, candidate or release: %+v", written)
	}
}

func TestAStoreRefusesAStepThatLowersOrSkipsTheAttemptCount(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	issue := developing()
	submit := stepOf(issue, core.EventCandidateSubmitted, core.StateQAEvaluation)
	submit.CandidateDigest = build
	if _, err := issue.Transition(ctx, submit); err != nil {
		t.Fatalf("the run could not submit its candidate: %v", err)
	}
	fail := stepOf(issue, core.EventQAFailed, core.StateDeveloping)
	fail.Attempt = 2
	if _, err := issue.Transition(ctx, fail); err != nil {
		t.Fatalf("the retry as attempt 2 was refused: %v", err)
	}

	cases := map[string]int{"resets it to 1": 1, "wipes it": 0, "raises it": 3}
	for name, attempt := range cases {
		reset := stepOf(issue, core.EventCandidateSubmitted, core.StateQAEvaluation)
		reset.CandidateDigest, reset.Attempt = build, attempt
		before := issue.record
		if _, err := issue.Transition(ctx, reset); !core.HasCode(err, core.CodeConflict) {
			t.Errorf("a submission that %s: want %s, got %v", name, core.CodeConflict, err)
		}
		if issue.record != before {
			t.Errorf("a submission that %s changed the record", name)
		}
	}

	submit = stepOf(issue, core.EventCandidateSubmitted, core.StateQAEvaluation)
	submit.CandidateDigest = build
	if _, err := issue.Transition(ctx, submit); err != nil {
		t.Fatalf("the run could not submit at its stored attempt: %v", err)
	}
	skip := stepOf(issue, core.EventQAFailed, core.StateDeveloping)
	skip.Attempt = 4
	if _, err := issue.Transition(ctx, skip); !core.HasCode(err, core.CodeConflict) {
		t.Fatalf("a retry that skips an attempt: want %s, got %v", core.CodeConflict, err)
	}
}
