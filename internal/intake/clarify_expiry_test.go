package intake_test

import (
	"context"
	"testing"
	"time"
)

// The timer parks an overdue issue only once every answer given before its
// deadline was taken in; until then it reports the deadline it waits on.
func TestTheTimerWaitsUntilAnswersBeforeTheDeadlineWereTakenIn(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := asked(t)
	if due, err := f.service.OverdueClarification(ctx); err != nil || !due.IsZero() {
		t.Fatalf("an issue inside its wait is overdue: %s, %v", due, err)
	}
	f.clock.Advance(clarifyTimeout)
	deadline := f.clock.Now()
	due, err := f.service.OverdueClarification(ctx)
	if err != nil || !due.Equal(deadline) {
		t.Fatalf("OverdueClarification = %s, %v; want the deadline %s", due, err, deadline)
	}
	if parked, err := f.service.ExpireClarifications(ctx, deadline.Add(-time.Second)); err != nil || len(parked) != 0 {
		t.Fatalf("an issue whose answers were taken in only up to before its deadline was parked: %v, %v", parked, err)
	}
	if parked, err := f.service.ExpireClarifications(ctx, deadline); err != nil || len(parked) != 1 {
		t.Fatalf("ExpireClarifications = %v, %v; want the overdue issue parked", parked, err)
	}
	if due, err := f.service.OverdueClarification(ctx); err != nil || !due.IsZero() {
		t.Fatalf("a parked issue is still overdue: %s, %v", due, err)
	}
}
