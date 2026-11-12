package intake_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/bestagentkits/sisf/internal/intake"
	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
)

// supersedeOutage is an approval store whose next failures calls to close a
// replaced request fail, as a store that is briefly unavailable does.
type supersedeOutage struct {
	*approvals.KVStore
	failures *atomic.Int32
}

func (s supersedeOutage) Supersede(ctx context.Context, id, replacement string) (approvals.Request, error) {
	if s.failures.Add(-1) >= 0 {
		return approvals.Request{}, core.NewError(core.CodeUnavailable, "the approval store is unavailable")
	}
	return s.KVStore.Supersede(ctx, id, replacement)
}

// editedDuringAnOutage opens a new-feature request, has its scope edited and
// runs intake while closing the replaced request fails failures times. It
// returns the factory, the replaced request and the one the issue waits on.
func editedDuringAnOutage(t *testing.T, failures int32) (*factory, approvals.Request, approvals.Request) {
	t.Helper()
	outage := &atomic.Int32{}
	f := newFactoryWith(t, func(store *approvals.KVStore) intake.Approvals { return supersedeOutage{store, outage} })
	f.open(member, featureBody, github.LabelReady, github.LabelKindFeature)
	f.reconcile()
	first := f.waiting()

	outage.Store(failures)
	f.rewrite(widened)
	_, err := f.service.Reconcile(context.Background(), issueRef)
	wantCode(t, err, core.CodeUnavailable, "closing the replaced request during the outage")
	f.wantState(core.StateApprovalPending)
	second := f.newestRequest()
	if second.ID == first.ID {
		t.Fatal("the edited scope did not get a request of its own")
	}
	if got := f.statusOf(first.ID); got != approvals.StatusPending {
		t.Fatalf("the replaced request is %s during the outage, the premise of this test", got)
	}
	return f, first, second
}

func TestAReplacedRequestIsClosedOnTheNextPassAfterAFailedClose(t *testing.T) {
	t.Parallel()
	f, first, second := editedDuringAnOutage(t, 1)
	wantEvents(t, f.reconcile())
	if got := f.statusOf(first.ID); got != approvals.StatusExpired {
		t.Fatalf("the replaced request is %s after the outage, want it closed", got)
	}
	if by, err := f.approvals.SupersededBy(context.Background(), first.ID); err != nil || by != second.ID {
		t.Fatalf("the replaced request was closed in favour of %q, %v; want %s", by, err, second.ID)
	}
	_, err := f.approvals.Submit(context.Background(), f.owner(), answer(first, approvals.DecisionApprove))
	wantCode(t, err, core.CodeExpired, "approving the replaced request after the outage")
	f.wantState(core.StateApprovalPending)
}

func TestAReplacedRequestIsClosedEvenOnceTheIssueLeftIntake(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f, first, second := editedDuringAnOutage(t, 1)
	// The Owner approves the edited scope and the issue becomes Ready, by a
	// pass that the outage no longer touches.
	f.decide(second, approvals.DecisionApprove)
	wantEvents(t, f.reconcile(), core.EventOwnerApproved)
	f.wantState(core.StateReady)
	if got := f.statusOf(first.ID); got != approvals.StatusExpired {
		t.Fatalf("the replaced request is %s once the issue is Ready, want it closed", got)
	}
	// A later pass over the Ready issue has nothing left to close.
	result, err := f.service.Reconcile(ctx, issueRef)
	if err != nil || result.Skipped == "" {
		t.Fatalf("a pass over a Ready issue = %+v, %v; want it skipped", result, err)
	}
}

func TestAPersistentOutageKeepsTheCloseOwedUntilItSucceeds(t *testing.T) {
	t.Parallel()
	f, first, _ := editedDuringAnOutage(t, 3)
	for range 2 {
		_, err := f.service.Reconcile(context.Background(), issueRef)
		wantCode(t, err, core.CodeUnavailable, "a pass while closing still fails")
	}
	if got := f.statusOf(first.ID); got != approvals.StatusPending {
		t.Fatalf("the replaced request is %s while closing fails", got)
	}
	f.reconcile()
	if got := f.statusOf(first.ID); got != approvals.StatusExpired {
		t.Fatalf("the replaced request is %s once closing works, want it closed", got)
	}
}
