package statemachine

import (
	"context"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/core"
)

// checkClaimWith runs the claim step after mutate has changed the request,
// the proof, or both.
func checkClaimWith(mutate func(*core.Transition, *ClaimProof)) error {
	request := claimRequest()
	proof := validClaim(request)
	mutate(&request, &proof)
	guards := Guards{Claim: claimWith(func(core.Transition) ClaimProof { return proof })}
	return machineWith(guards).Check(context.Background(), request)
}

func TestAnIssueIsClaimedUnderALiveLeaseAndAHeldReservation(t *testing.T) {
	if err := checkClaimWith(func(*core.Transition, *ClaimProof) {}); err != nil {
		t.Fatalf("a claim with a live lease and a held reservation was refused: %v", err)
	}
}

func TestUnlimitedBudgetStillClaimsThroughAReservation(t *testing.T) {
	// Unlimited mode sets no monetary limit, so the reserved amount may be
	// zero, yet the reservation itself must exist so spending stays metered.
	err := checkClaimWith(func(_ *core.Transition, p *ClaimProof) {
		p.Reservation.Mode = core.BudgetUnlimited
		p.Reservation.Amount = 0
	})
	if err != nil {
		t.Fatalf("a zero-amount reservation in unlimited mode was refused: %v", err)
	}
	capped := checkClaimWith(func(_ *core.Transition, p *ClaimProof) {
		p.Reservation.Mode = core.BudgetCapped
		p.Reservation.Amount = 2_500_000
		p.Reservation.HardCapEnforced = true
	})
	if capped != nil {
		t.Fatalf("a held reservation in capped mode was refused: %v", capped)
	}
}

func TestAClaimWithoutALiveLeaseIsRefused(t *testing.T) {
	cases := map[string]func(*core.Transition, *ClaimProof){
		"request is not written under a lease": func(r *core.Transition, _ *ClaimProof) {
			r.LeaseOwner, r.FencingToken = "", 0
		},
		"request carries an owner but no fencing token": func(r *core.Transition, _ *ClaimProof) {
			r.FencingToken = 0
		},
		"request names no run": func(r *core.Transition, p *ClaimProof) {
			r.RunID, p.Reservation.RunID = "", ""
		},
		"no lease was presented": func(_ *core.Transition, p *ClaimProof) {
			p.LeaseOwner, p.FencingToken, p.LeaseExpiresAt = "", 0, time.Time{}
		},
		"lease belongs to another owner": func(_ *core.Transition, p *ClaimProof) {
			p.LeaseOwner = "dispatcher-2"
		},
		"lease carries an older fencing token": func(_ *core.Transition, p *ClaimProof) {
			p.FencingToken = 4
		},
		"lease carries a newer fencing token": func(_ *core.Transition, p *ClaimProof) {
			p.FencingToken = 6
		},
		"lease has no expiry": func(_ *core.Transition, p *ClaimProof) {
			p.LeaseExpiresAt = time.Time{}
		},
		"lease expired a moment ago": func(_ *core.Transition, p *ClaimProof) {
			p.LeaseExpiresAt = testNow.Add(-time.Nanosecond)
		},
		"lease expires at this very instant": func(_ *core.Transition, p *ClaimProof) {
			p.LeaseExpiresAt = testNow
		},
	}
	for name, mutate := range cases {
		requireGuardFailed(t, name, checkClaimWith(mutate), GuardClaim)
	}
}

func TestAClaimWithoutAHeldReservationIsRefused(t *testing.T) {
	cases := map[string]func(*core.Transition, *ClaimProof){
		"no reservation was presented": func(_ *core.Transition, p *ClaimProof) {
			p.Reservation = core.Reservation{}
		},
		"reservation has no identity": func(_ *core.Transition, p *ClaimProof) {
			p.Reservation.ID = ""
		},
		"reservation was released": func(_ *core.Transition, p *ClaimProof) {
			p.Reservation.Status = core.ReservationReleased
		},
		"reservation was already settled": func(_ *core.Transition, p *ClaimProof) {
			p.Reservation.Status = core.ReservationSettled
		},
		"reservation is suspended": func(_ *core.Transition, p *ClaimProof) {
			p.Reservation.Status = core.ReservationSuspended
		},
		"reservation has no status": func(_ *core.Transition, p *ClaimProof) {
			p.Reservation.Status = ""
		},
		"reservation belongs to another issue": func(_ *core.Transition, p *ClaimProof) {
			p.Reservation.IssueID.Number = 8
		},
		"reservation belongs to another repository": func(_ *core.Transition, p *ClaimProof) {
			p.Reservation.IssueID.Repo = "other-app"
		},
		"reservation belongs to another run": func(_ *core.Transition, p *ClaimProof) {
			p.Reservation.RunID = "run-0002"
		},
		"reservation states no budget mode": func(_ *core.Transition, p *ClaimProof) {
			p.Reservation.Mode = ""
		},
		"reservation states an unknown budget mode": func(_ *core.Transition, p *ClaimProof) {
			p.Reservation.Mode = "free"
		},
		"reservation amount is negative": func(_ *core.Transition, p *ClaimProof) {
			p.Reservation.Amount = -1
		},
	}
	for name, mutate := range cases {
		requireGuardFailed(t, name, checkClaimWith(mutate), GuardClaim)
	}
}

func TestAnEmptyClaimProofIsRefused(t *testing.T) {
	guards := Guards{Claim: claimWith(func(core.Transition) ClaimProof { return ClaimProof{} })}
	err := machineWith(guards).Check(context.Background(), claimRequest())
	requireGuardFailed(t, "empty proof", err, GuardClaim)
}
