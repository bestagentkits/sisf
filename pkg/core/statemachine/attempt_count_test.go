package statemachine

import (
	"context"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
)

// ledger is the record of the test issue, written the way the Records
// contract prescribes: the machine judges each step, then the store checks
// the edge, the lease and the binding to the stored record, and the record
// written carries the attempt number of the step. The attempts guard reads
// the count from the stored record, as a real guard must.
type ledger struct {
	record  core.StateRecord
	machine *Machine
}

// newLedger is an issue that has no record yet.
func newLedger() *ledger {
	l := &ledger{record: core.StateRecord{FactoryID: testFactory, IssueID: testIssue, UpdatedAt: testNow}}
	guards := passingGuards()
	guards.Attempts = AttemptsGuardFunc(func(context.Context, core.Transition) (AttemptsProof, error) {
		return AttemptsProof{
			IssueID:     l.record.IssueID,
			RunID:       l.record.RunID,
			Attempt:     l.record.Attempt,
			MaxAttempts: testMaxAttempts,
		}, nil
	})
	l.machine = machineWith(guards)
	return l
}

// next builds the honest step on event: it continues the stored run, the
// stored candidate and release and the stored attempt number, except where
// the step itself sets one of them.
func (l *ledger) next(event core.Event, to core.State) core.Transition {
	t := core.Transition{
		IssueID:         testIssue,
		From:            l.record.State,
		To:              to,
		Event:           event,
		ExpectedVersion: l.record.Version,
		IdempotencyKey:  "step:" + string(event),
		LeaseOwner:      "dispatcher-1",
		FencingToken:    5,
		RunID:           l.record.RunID,
		Attempt:         l.record.Attempt,
		ActorID:         "user:owner-1",
		CandidateDigest: l.record.CandidateDigest,
		ReleaseDigest:   l.record.ReleaseDigest,
	}
	switch event {
	case core.EventClaimed:
		t.RunID, t.Attempt, t.CandidateDigest, t.ReleaseDigest = testRun, 1, "", ""
	case core.EventCandidateSubmitted:
		t.CandidateDigest = testDigest
	case core.EventQAFailed:
		t.Attempt = l.record.Attempt + 1
	case core.EventRolloutStarted:
		t.ReleaseDigest = released
	}
	return t
}

// write takes t through the machine and the store checks and, when all of
// them accept it, stores the record it leads to.
func (l *ledger) write(t core.Transition) error {
	if err := l.machine.Check(context.Background(), t); err != nil {
		return err
	}
	if err := CheckEdge(t); err != nil {
		return err
	}
	if t.ExpectedVersion != l.record.Version || t.From != l.record.State {
		return core.NewError(core.CodeConflict, "the record moved on")
	}
	if err := CheckLease(t); err != nil {
		return err
	}
	if err := CheckRecordBinding(l.record, t); err != nil {
		return err
	}
	next := l.record
	next.State, next.Version, next.IdempotencyKey = t.To, l.record.Version+1, t.IdempotencyKey
	next.LeaseOwner, next.FencingToken, next.Attempt = t.LeaseOwner, t.FencingToken, t.Attempt
	switch t.Event {
	case core.EventClaimed:
		next.RunID, next.CandidateDigest, next.ReleaseDigest = t.RunID, "", ""
	case core.EventCandidateSubmitted:
		next.CandidateDigest = t.CandidateDigest
	case core.EventRolloutStarted:
		next.ReleaseDigest = t.ReleaseDigest
	}
	l.record = next
	return nil
}

// walk writes the honest step on each event in turn and fails the test on
// the first one refused.
func (l *ledger) walk(t *testing.T, steps ...Edge) {
	t.Helper()
	for _, s := range steps {
		if err := l.write(l.next(s.Event, s.To)); err != nil {
			t.Fatalf("%s --%s--> %s at attempt %d was refused: %v", l.record.State, s.Event, s.To, l.record.Attempt, err)
		}
	}
}

func step(event core.Event, to core.State) Edge { return Edge{Event: event, To: to} }

// The steps of the lifecycle the walks below are made of.
var (
	toReady = []Edge{
		step(core.EventIssueReceived, core.StateIntake),
		step(core.EventInfoSufficient, core.StateScopeDraft),
		step(core.EventScopePermitted, core.StateReady),
	}
	claimAndDevelop = []Edge{
		step(core.EventClaimed, core.StateClaimed),
		step(core.EventDevelopmentStarted, core.StateDeveloping),
	}
	submit   = step(core.EventCandidateSubmitted, core.StateQAEvaluation)
	failQA   = step(core.EventQAFailed, core.StateDeveloping)
	toCanary = []Edge{
		step(core.EventQAPassed, core.StateReleaseCandidate),
		step(core.EventRolloutStarted, core.StateCanary),
	}
)

// developingLedger is an issue whose first run has just started developing.
func developingLedger(t *testing.T) *ledger {
	t.Helper()
	l := newLedger()
	l.walk(t, toReady...)
	if l.record.Attempt != 0 {
		t.Fatalf("an issue that was never claimed counts attempt %d", l.record.Attempt)
	}
	l.walk(t, claimAndDevelop...)
	return l
}

func TestAnHonestRunWithARetryReachesClosedThroughMachineAndStore(t *testing.T) {
	t.Parallel()
	l := developingLedger(t)
	l.walk(t, submit, failQA, submit)
	l.walk(t, toCanary...)
	l.walk(t,
		step(core.EventRolloutHealthy, core.StateMonitoring),
		step(core.EventPostDeployVerified, core.StateVerified),
		step(core.EventIssueClosed, core.StateClosed),
	)
	if l.record.State != core.StateClosed || l.record.Attempt != 2 {
		t.Fatalf("the run ended in %s at attempt %d, want Closed at attempt 2", l.record.State, l.record.Attempt)
	}
}

func TestARolledBackIssueIsRetriedAndASecondRunStartsItsOwnCount(t *testing.T) {
	t.Parallel()
	l := developingLedger(t)
	l.walk(t, submit, failQA, submit, failQA, submit)
	l.walk(t, toCanary...)
	l.walk(t,
		step(core.EventErrorThresholdExceeded, core.StateRolledBack),
		step(core.EventRecoveryReported, core.StateBlocked),
		step(core.EventRetryAuthorized, core.StateReady),
	)
	if l.record.Attempt != testMaxAttempts {
		t.Fatalf("the rollback, recovery and retry changed the count to %d, want %d", l.record.Attempt, testMaxAttempts)
	}
	claim := l.next(core.EventClaimed, core.StateClaimed)
	claim.RunID = otherRun
	if err := l.write(claim); err != nil {
		t.Fatalf("the claim of a second run was refused: %v", err)
	}
	if l.record.Attempt != 1 {
		t.Fatalf("the second run starts at attempt %d, want 1", l.record.Attempt)
	}
	l.walk(t, step(core.EventDevelopmentStarted, core.StateDeveloping), submit, failQA)
	if l.record.Attempt != 2 {
		t.Fatalf("the first retry of the second run is attempt %d, want 2", l.record.Attempt)
	}
}

func TestAnHonestWriterIsStoppedExactlyAtTheLimit(t *testing.T) {
	t.Parallel()
	l := developingLedger(t)
	for retry := 1; retry < testMaxAttempts; retry++ {
		l.walk(t, submit, failQA)
	}
	l.walk(t, submit)
	if l.record.Attempt != testMaxAttempts {
		t.Fatalf("the run is at attempt %d, want %d", l.record.Attempt, testMaxAttempts)
	}
	err := l.write(l.next(failQA.Event, failQA.To))
	requireGuardFailed(t, "a retry after the last attempt", err, GuardAttempts)
}

func TestTheAttemptCountCannotBeResetToLoopPastTheLimit(t *testing.T) {
	t.Parallel()
	l := developingLedger(t)
	retries := 0
	for range 25 {
		resubmit := l.next(submit.Event, submit.To)
		resubmit.Attempt = 1
		if l.write(resubmit) != nil {
			continue
		}
		fail := l.next(failQA.Event, failQA.To)
		fail.Attempt = 2
		if l.write(fail) == nil {
			retries++
		}
	}
	if retries >= testMaxAttempts {
		t.Fatalf("a writer that resets the count was retried %d times, the limit allows %d", retries, testMaxAttempts-1)
	}
}

func TestEveryStepCarriesTheAttemptItsEdgeDemands(t *testing.T) {
	t.Parallel()
	for _, e := range Edges() {
		if e.From == "" {
			continue
		}
		stored := storedIn(e.From)
		stored.Attempt = 2
		want := stored.Attempt
		switch e.Event {
		case core.EventClaimed:
			want = 1
		case core.EventQAFailed:
			want = stored.Attempt + 1
		}
		for _, attempt := range []int{0, 1, 2, 3, 4} {
			request := transitionOn(e.From, e.Event, e.To)
			if e.Event == core.EventClaimed {
				request.RunID = otherRun
			}
			request.Attempt = attempt
			err := CheckRecordBinding(stored, request)
			switch {
			case attempt == want && err != nil:
				t.Errorf("%s --%s--> %s at attempt %d after 2 was refused: %v", e.From, e.Event, e.To, attempt, err)
			case attempt != want && !core.HasCode(err, core.CodeConflict):
				t.Errorf("%s --%s--> %s at attempt %d after 2: want %s, got %v", e.From, e.Event, e.To, attempt, core.CodeConflict, err)
			}
		}
	}
}

func TestAWrongAttemptNumberIsRefusedByTheStore(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		request core.Transition
		attempt int
	}{
		"a step mid-run wipes the count":              {candidateRequest(), 0},
		"a step mid-run lowers the count":             {candidateRequest(), 1},
		"a step mid-run raises the count":             {transitionOn(core.StateCanary, core.EventRolloutHealthy, core.StateMonitoring), 3},
		"a failed evaluation keeps the count":         {qaRetryRequest(), 2},
		"a failed evaluation lowers the count":        {qaRetryRequest(), 1},
		"a failed evaluation skips an attempt":        {qaRetryRequest(), 4},
		"a failed evaluation resets the count":        {qaRetryRequest(), 0},
		"a rollback wipes the count":                  {transitionOn(core.StateCanary, core.EventErrorThresholdExceeded, core.StateRolledBack), 0},
		"a claim starts its run at no attempt":        {claimRequest(), 0},
		"a claim starts its run past attempt one":     {claimRequest(), 2},
		"a retry of a blocked issue lowers the count": {retryRequest(), 1},
	}
	for name, tc := range cases {
		stored := storedIn(tc.request.From)
		stored.Attempt = 2
		request := tc.request
		if request.Event == core.EventClaimed {
			request.RunID = otherRun
		}
		request.Attempt = tc.attempt
		if err := CheckRecordBinding(stored, request); !core.HasCode(err, core.CodeConflict) {
			t.Errorf("%s: want %s, got %v", name, core.CodeConflict, err)
		}
	}
}
