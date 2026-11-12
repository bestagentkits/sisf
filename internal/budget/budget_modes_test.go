package budget_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/budget"
	"github.com/bestagentkits/sisf/internal/dispatcher"
	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
)

func (e *env) reserve(n int, estimate core.Micros) (core.Reservation, error) {
	return e.gate.Reserve(context.Background(), e.ledger, request(n, estimate))
}

func (e *env) mustReserve(n int, estimate core.Micros) core.Reservation {
	e.t.Helper()
	r, err := e.reserve(n, estimate)
	if err != nil {
		e.t.Fatalf("run %d was not admitted: %v", n, err)
	}
	return r
}

func (e *env) status(id string) core.ReservationStatus {
	e.t.Helper()
	r, err := e.ledger.GetReservation(context.Background(), id)
	if err != nil {
		e.t.Fatal(err)
	}
	return r.Status
}

func wantCode(t *testing.T, err error, code core.ErrorCode) {
	t.Helper()
	if !core.HasCode(err, code) {
		t.Fatalf("error = %v, want %s", err, code)
	}
}

func capped(cap, reserve core.Micros) budget.Settings {
	return budget.Settings{Mode: core.BudgetCapped, CapMicros: cap, RollbackReserveMicros: reserve, ProviderHardCap: true}
}

func TestNoClaimIsAdmittedBeforeTheOwnerConfirmedTheBudget(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	_, err := e.reserve(1, 0)
	wantCode(t, err, core.CodeBudgetUnconfirmed)

	incomplete := map[string]dispatcher.BudgetConfirmation{
		"no alert decision":       {Mode: core.BudgetUnlimited, ConfirmedAt: start.Add(-time.Hour)},
		"unknown mode":            {Mode: "free", AlertOff: true, ConfirmedAt: start.Add(-time.Hour)},
		"confirmed in the future": {Mode: core.BudgetUnlimited, AlertOff: true, ConfirmedAt: start.Add(time.Hour)},
	}
	for name, c := range incomplete {
		e.confirm(c)
		_, err := e.reserve(1, 0)
		if !core.HasCode(err, core.CodeBudgetUnconfirmed) {
			t.Errorf("%s: error = %v, want BUDGET_UNCONFIRMED", name, err)
		}
	}
	// Settings alone do not stand in for the install-time confirmation.
	e = newEnv(t)
	e.configure(capped(10*million, 0))
	_, err = e.reserve(1, million)
	wantCode(t, err, core.CodeBudgetUnconfirmed)
	if len(e.ledger.reservations) != 0 {
		t.Error("a refused claim left a reservation behind")
	}

	e.confirmUnlimited(0)
	if r := e.mustReserve(1, million); r.Status != core.ReservationHeld || r.Mode != core.BudgetCapped {
		t.Errorf("after the confirmation: %+v", r)
	}
}

func TestUnlimitedMetersEveryRunAndNeverSuspends(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.confirmUnlimited(0)
	r := e.mustReserve(1, 0)
	if r.Mode != core.BudgetUnlimited || r.HardCapEnforced {
		t.Errorf("unlimited reservation claims a cap: %+v", r)
	}
	if again := e.mustReserve(1, 0); again.ID != r.ID {
		t.Error("a retried claim reserved twice")
	}
	other := request(2, 0)
	other.IdempotencyKey = request(1, 0).IdempotencyKey
	_, err := e.gate.Reserve(context.Background(), e.ledger, other)
	wantCode(t, err, core.CodeConflict)

	if err := e.gate.Settle(context.Background(), e.ledger, r.ID, usage(1, 900*million, false)); err != nil {
		t.Fatal(err)
	}
	if got := e.status(r.ID); got != core.ReservationSettled {
		t.Errorf("unlimited overrun left the reservation %s, want settled", got)
	}
	if totals, _ := e.ledger.BudgetTotals(context.Background(), time.Time{}); totals.SpentUnverified != 900*million {
		t.Errorf("spending was not metered: %+v", totals)
	}
}

func TestAlertThresholdNotifiesOnceWithoutStopping(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.confirmUnlimited(10 * million)
	settle := func(n int, cost core.Micros) core.Reservation {
		r := e.mustReserve(n, 0)
		if err := e.gate.Settle(context.Background(), e.ledger, r.ID, usage(n, cost, true)); err != nil {
			t.Fatal(err)
		}
		return r
	}
	settle(1, 4*million)
	if len(e.notes.kinds()) != 0 {
		t.Fatal("notified below the threshold")
	}
	r := settle(2, 7*million) // crosses 10; the notifier fails and must change nothing
	if got := e.notes.kinds(); len(got) != 1 || got[0] != budget.NoticeAlertThreshold {
		t.Fatalf("notices = %v, want one alert", got)
	}
	if e.status(r.ID) != core.ReservationSettled {
		t.Error("an alert stopped the run")
	}
	settle(3, 5*million)
	if len(e.notes.kinds()) != 1 {
		t.Error("notified again for spending already past the threshold")
	}
	e.mustReserve(4, 0) // still admitted: an alert never stops work
}

func TestCappedRefusesWhatWouldPassTheCap(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.confirmUnlimited(0)
	e.configure(capped(10*million, 0))
	_, err := e.reserve(1, 0)
	wantCode(t, err, core.CodeInvalidArgument) // nothing to set aside
	e.mustReserve(1, 6*million)
	_, err = e.reserve(2, 5*million)
	wantCode(t, err, core.CodeBudgetExceeded)
	var typed *core.Error
	if !errors.As(err, &typed) || len(typed.NextActions) == 0 {
		t.Error("the refusal names no next action")
	}
	if len(e.ledger.reservations) != 1 {
		t.Error("a refused claim left a reservation behind")
	}
	e.mustReserve(2, 4*million) // exactly the cap is allowed
}

func TestCappedSuspendsOnOverrunAndKeepsTheUsage(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.confirmUnlimited(0)
	e.configure(capped(10*million, 0))
	r := e.mustReserve(1, 4*million)
	if !r.HardCapEnforced || r.Mode != core.BudgetCapped {
		t.Errorf("reservation = %+v", r)
	}
	if err := e.gate.Settle(context.Background(), e.ledger, r.ID, usage(1, 9*million, false)); err != nil {
		t.Fatal(err)
	}
	if got := e.status(r.ID); got != core.ReservationSuspended {
		t.Fatalf("status = %s, want suspended", got)
	}
	if got := e.notes.kinds(); len(got) != 1 || got[0] != budget.NoticeRunSuspended {
		t.Errorf("notices = %v", got)
	}
	_, err := e.reserve(2, 2*million)
	wantCode(t, err, core.CodeBudgetExceeded) // the overrun is spent, so little is left
	if len(e.ledger.usage) != 1 {
		t.Error("the overrunning usage was dropped")
	}
	if err := e.gate.Release(context.Background(), e.ledger, r.ID); err != nil || e.status(r.ID) != core.ReservationReleased {
		t.Errorf("release of a suspended reservation: %v", err)
	}
	if totals, _ := e.ledger.BudgetTotals(context.Background(), time.Time{}); totals.SpentUnverified != 9*million {
		t.Errorf("releasing erased spending: %+v", totals)
	}
}

func TestCappedPeriodStartsAgainOnSchedule(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.confirmUnlimited(0)
	s := capped(10*million, 0)
	s.Period = budget.PeriodDay
	e.configure(s)
	r := e.mustReserve(1, 10*million)
	u := usage(1, 10*million, true)
	if err := e.gate.Settle(context.Background(), e.ledger, r.ID, u); err != nil {
		t.Fatal(err)
	}
	_, err := e.reserve(2, million)
	wantCode(t, err, core.CodeBudgetExceeded)
	e.clock.Advance(24 * time.Hour) // the next day
	e.mustReserve(2, 10*million)
}

func TestRollbackReserveIsNeverSpentByNormalRuns(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.confirmUnlimited(0)
	e.configure(capped(10*million, 3*million))
	_, err := e.reserve(1, 8*million)
	wantCode(t, err, core.CodeBudgetExceeded)
	e.mustReserve(1, 7*million)
	_, err = e.reserve(2, million)
	wantCode(t, err, core.CodeBudgetExceeded)
	rb, err := e.gate.ReserveRollback(context.Background(), e.ledger, request(3, 3*million))
	if err != nil || rb.Status != core.ReservationHeld {
		t.Fatalf("the rollback could not use its reserve: %v", err)
	}
	_, err = e.gate.ReserveRollback(context.Background(), e.ledger, request(4, million))
	wantCode(t, err, core.CodeBudgetExceeded) // not even a rollback passes the cap
}

func TestAReservationIsKeptWhereItMustBe(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.confirmUnlimited(0)
	e.configure(capped(10*million, 0))
	r := e.mustReserve(1, 4*million)

	wrong := usage(2, million, true) // another run's usage is refused
	wantCode(t, e.gate.Settle(context.Background(), e.ledger, r.ID, wrong), core.CodeConflict)
	e.ledger.failAppend = errors.New("disk full")
	if err := e.gate.Settle(context.Background(), e.ledger, r.ID, usage(1, million, true)); err == nil {
		t.Fatal("a ledger failure was swallowed")
	}
	e.ledger.failAppend = nil
	if got := e.status(r.ID); got != core.ReservationHeld {
		t.Fatalf("a failed settlement left the reservation %s, want held", got)
	}
	if err := e.gate.Settle(context.Background(), e.ledger, r.ID, usage(1, million, true)); err != nil {
		t.Fatal(err)
	}
	if err := e.gate.Release(context.Background(), e.ledger, r.ID); err != nil || e.status(r.ID) != core.ReservationSettled {
		t.Errorf("releasing a settled reservation changed it: %v", err)
	}
	wantCode(t, e.gate.Settle(context.Background(), e.ledger, r.ID, usage(1, million, true)), core.CodeConflict)
}

func TestDamagedOrIncompleteSettingsNeverAdmitARun(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.confirmUnlimited(0)
	e.put(budget.SettingsKey, map[string]any{"mode": "capped"}) // no cap: not a valid setting
	_, err := e.reserve(1, million)
	wantCode(t, err, core.CodeTampered)

	e = newEnv(t)
	e.confirm(dispatcher.BudgetConfirmation{Mode: core.BudgetCapped, ConfirmedAt: start.Add(-time.Hour)})
	_, err = e.reserve(1, million)
	wantCode(t, err, core.CodeBudgetUnconfirmed) // capped with no cap set
}

const capInput = `{"mode":"capped","cap_micros":50000000,"period":"month","rollback_reserve_micros":5000000}`

func TestSettingTheBudgetNeedsAnApprovalAndAFreshStepUpFromTheOwner(t *testing.T) {
	t.Parallel()
	p := newPipeline(t)
	owner := p.actor(core.RoleOwner, true)

	_, err := p.invoke(p.actor(core.RoleMaintainer, true), capInput, "")
	wantCode(t, err, core.CodePermissionDenied)
	_, err = p.invoke(owner, capInput, "")
	wantCode(t, err, core.CodeApprovalRequired)
	var typed *core.Error
	if !errors.As(err, &typed) || typed.Details["approval_id"] == "" {
		t.Fatalf("no approval request was filed: %v", err)
	}
	id := typed.Details["approval_id"]
	p.unchanged(t)

	_, err = p.invoke(owner, capInput, id) // filed but not decided
	wantCode(t, err, core.CodeApprovalRequired)
	r, err := p.approvals.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.approvals.Submit(context.Background(), owner, approvals.Submission{RequestID: r.ID, Nonce: r.Nonce,
		SubjectDigest: r.SubjectDigest, PolicyRevision: r.PolicyRevision, Decision: approvals.DecisionApprove, Reason: "checked"}); err != nil {
		t.Fatal(err)
	}

	_, err = p.invoke(p.actor(core.RoleOwner, false), capInput, id) // approved, but no fresh step-up
	wantCode(t, err, core.CodeStepUpRequired)
	p.unchanged(t)
	_, err = p.invoke(owner, `{"mode":"capped","cap_micros":500000000}`, id) // the approval covers other parameters
	if err == nil {
		t.Fatal("an approval covered a different budget")
	}
	p.unchanged(t)

	if _, err := p.invoke(owner, capInput, id); err != nil {
		t.Fatalf("the approved change failed: %v", err)
	}
	got, err := p.svc.Invoke(context.Background(), budget.ActionGet, core.ActionRequest{Actor: p.actor(core.RoleViewer, false)})
	if err != nil {
		t.Fatal(err)
	}
	var st budget.Status
	if err := json.Unmarshal(got.Output, &st); err != nil || st.Mode != core.BudgetCapped || st.Settings.CapMicros != 50*million ||
		st.AvailableMicros == nil || *st.AvailableMicros != 45*million || len(st.Warnings) == 0 {
		t.Fatalf("budget.get = %s, %v", got.Output, err)
	}
}
