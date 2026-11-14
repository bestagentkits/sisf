package core

import (
	"context"
	"slices"
	"testing"
	"time"
)

func validPolicyRefs() PolicyRefs {
	return PolicyRefs{
		PolicySHA:         testSHA,
		ReviewSHA:         testSHA,
		DecisionSHA:       testSHA,
		FeatureCatalogSHA: testSHA,
		ModelRoute:        ModelRoute{Provider: "anthropic", Model: "mid", Effort: "low"},
	}
}

func TestPolicyRefsMustPinEveryFoundationFileAndTheModelRoute(t *testing.T) {
	if err := validPolicyRefs().Validate(); err != nil {
		t.Fatalf("valid refs rejected: %v", err)
	}
	cases := map[string]func(*PolicyRefs){
		"policy sha missing":  func(p *PolicyRefs) { p.PolicySHA = "" },
		"review sha is a ref": func(p *PolicyRefs) { p.ReviewSHA = "main" },
		"decision sha short":  func(p *PolicyRefs) { p.DecisionSHA = "abc123" },
		"catalog sha missing": func(p *PolicyRefs) { p.FeatureCatalogSHA = "" },
		"no model route":      func(p *PolicyRefs) { p.ModelRoute = ModelRoute{} },
		"route without model": func(p *PolicyRefs) { p.ModelRoute.Model = "" },
	}
	for name, mutate := range cases {
		refs := validPolicyRefs()
		mutate(&refs)
		if CodeOf(refs.Validate()) != CodeInvalidArgument {
			t.Errorf("%s: want %s, got %v", name, CodeInvalidArgument, refs.Validate())
		}
	}
}

func TestModelRoutesCompareByProviderAndModelNotEffort(t *testing.T) {
	low := ModelRoute{Provider: "anthropic", Model: "mid", Effort: "low"}
	high := ModelRoute{Provider: "anthropic", Model: "mid", Effort: "high"}
	other := ModelRoute{Provider: "openai", Model: "mid", Effort: "low"}
	if !low.SameModel(high) {
		t.Error("raising effort does not make a different model")
	}
	if low.SameModel(other) || low.SameModel(ModelRoute{Provider: "anthropic", Model: "large"}) {
		t.Error("another provider or model is a different route")
	}
	if low.String() != "anthropic/mid@low" || (ModelRoute{Provider: "p", Model: "m"}).String() != "p/m" {
		t.Errorf("String() = %q", low.String())
	}
}

func TestStepKindsAndDelegationModesAreClosedSets(t *testing.T) {
	wantSteps := []StepKind{StepIntake, StepCoding, StepPlanning, StepReadOnly, StepQA}
	if got := StepKinds(); !slices.Equal(got, wantSteps) {
		t.Fatalf("StepKinds() = %v", got)
	}
	if StepKind("advisor").Valid() || !StepQA.Valid() {
		t.Error("step kind validation is wrong")
	}
	for _, m := range []DelegationMode{DelegationOnDemand, DelegationProposal, DelegationBounded} {
		if !m.Valid() {
			t.Errorf("delegation mode %q must be valid", m)
		}
	}
	if DelegationOnDemand != "A" || DelegationProposal != "B" || DelegationBounded != "C" {
		t.Error("delegation modes must keep the letters A, B and C")
	}
	if DelegationMode("D").Valid() || DelegationMode("").Valid() {
		t.Error("unknown delegation modes must be rejected")
	}
}

func TestBudgetModeIsUnlimitedOrCappedAndNeverImplicit(t *testing.T) {
	if !BudgetUnlimited.Valid() || !BudgetCapped.Valid() {
		t.Fatal("both budget modes must be valid")
	}
	if BudgetUnlimited != "unlimited" || BudgetCapped != "capped" {
		t.Error("budget modes must keep their manifest spelling")
	}
	for _, m := range []BudgetMode{"", "free", "Unlimited"} {
		if m.Valid() {
			t.Errorf("budget mode %q must be rejected", m)
		}
	}
}

func TestReservationRequestValidation(t *testing.T) {
	req := ReservationRequest{
		FactoryID:      "acme-factory",
		IssueID:        IssueRef{Owner: "acme", Repo: "app", Number: 7},
		RunID:          "run-0001",
		Estimate:       2_500_000,
		IdempotencyKey: "claim:run-0001",
	}
	if err := req.Validate(); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	cases := map[string]func(*ReservationRequest){
		"missing factory":    func(r *ReservationRequest) { r.FactoryID = "" },
		"missing issue":      func(r *ReservationRequest) { r.IssueID = IssueRef{} },
		"missing run":        func(r *ReservationRequest) { r.RunID = "" },
		"negative estimate":  func(r *ReservationRequest) { r.Estimate = -1 },
		"no idempotency key": func(r *ReservationRequest) { r.IdempotencyKey = "" },
	}
	for name, mutate := range cases {
		bad := req
		mutate(&bad)
		if CodeOf(bad.Validate()) != CodeInvalidArgument {
			t.Errorf("%s: want %s, got %v", name, CodeInvalidArgument, bad.Validate())
		}
	}
}

// memoryLedger and refusingGate prove the budget contracts can be met with exported types alone.
type memoryLedger struct{ reservations map[string]Reservation }

func (m *memoryLedger) PutReservation(_ context.Context, r Reservation) error {
	m.reservations[r.ID] = r
	return nil
}

func (m *memoryLedger) GetReservation(_ context.Context, id string) (Reservation, error) {
	r, ok := m.reservations[id]
	if !ok {
		return Reservation{}, NewError(CodeNotFound, "no such reservation")
	}
	return r, nil
}
func (m *memoryLedger) AppendUsage(context.Context, Usage) error { return nil }
func (m *memoryLedger) RunUsage(context.Context, RunID, time.Time) (RunUsage, error) {
	return RunUsage{}, nil
}
func (m *memoryLedger) BudgetTotals(context.Context, time.Time) (BudgetTotals, error) {
	return BudgetTotals{}, nil
}

type refusingGate struct{}

func (refusingGate) Reserve(context.Context, BudgetLedger, ReservationRequest) (Reservation, error) {
	return Reservation{}, NewError(CodeBudgetUnconfirmed, "the Owner has not confirmed the budget mode")
}
func (refusingGate) Settle(context.Context, BudgetLedger, string, Usage) error { return nil }
func (refusingGate) Release(context.Context, BudgetLedger, string) error       { return nil }

func TestBudgetGateWorksAgainstTheCallersLedger(t *testing.T) {
	var ledger BudgetLedger = &memoryLedger{reservations: map[string]Reservation{}}
	var gate BudgetGate = refusingGate{}
	if _, err := gate.Reserve(context.Background(), ledger, ReservationRequest{}); CodeOf(err) != CodeBudgetUnconfirmed {
		t.Fatalf("Reserve error = %v", err)
	}
	if _, err := ledger.GetReservation(context.Background(), "missing"); CodeOf(err) != CodeNotFound {
		t.Fatalf("GetReservation error = %v", err)
	}
	for _, s := range []ReservationStatus{ReservationHeld, ReservationSettled, ReservationReleased, ReservationSuspended} {
		if !s.Valid() {
			t.Errorf("reservation status %q must be valid", s)
		}
	}
	if ReservationStatus("spent").Valid() {
		t.Error("unknown reservation status must be rejected")
	}
}
