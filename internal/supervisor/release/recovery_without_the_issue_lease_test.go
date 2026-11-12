package release

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
)

// A release left unwatched whose issue lease another holder keeps, as an
// agent-zone role that took it over and renews it, is still rolled back:
// the LKG serves again although the steps cannot be written, the wait
// names the holder, and a second recovery switches nothing again. Once the
// lease can be taken the steps are written and the issue blocked.
func TestAnUnwatchedReleaseIsRolledBackEvenWhenAnotherHolderKeepsItsLease(t *testing.T) {
	t.Parallel()
	for i, left := range []core.State{core.StateCanary, core.StateMonitoring} {
		t.Run(string(left), func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			w := newWorld(t)
			issue, run := issueRef(80+i), core.RunID("run-held-lease")
			w.seedInFlight(issue, run, firstBuild, left)
			w.clock.Advance(3 * DefaultWindow) // the stopped supervisor's lease lapses
			w.holdLease(issue, "agent-intruder")

			p := w.pipeline()
			out, err := p.Recover(ctx, issue, testUnit)
			if !core.HasCode(err, core.CodeLeaseHeld) || out.RestoredTo != lkgDigest || !strings.Contains(out.Waits, "agent-intruder") {
				t.Fatalf("Recover = %+v, %v; want the LKG restored and a wait naming the holder", out, err)
			}
			if _, rollbacks, _, _ := w.backend.snapshot(); len(rollbacks) != 1 || rollbacks[0] != lkgDigest {
				t.Fatalf("rollbacks %v, want one to the LKG", rollbacks)
			}
			w.requireState(issue, left)
			if again, err := p.Recover(ctx, issue, testUnit); !core.HasCode(err, core.CodeLeaseHeld) || again.RestoredTo != lkgDigest {
				t.Fatalf("a second recovery = %+v, %v", again, err)
			}
			if _, rollbacks, _, _ := w.backend.snapshot(); len(rollbacks) != 1 {
				t.Fatalf("the LKG already serving was switched again: %v", rollbacks)
			}

			w.clock.Advance(time.Hour) // the holder stops renewing
			out, err = p.Recover(ctx, issue, testUnit)
			if err != nil || out.Result != ResultBlocked || out.Cause != statemachine.BlockReleaseInterrupted {
				t.Fatalf("Recover once the lease is free = %+v, %v; want blocked as interrupted", out, err)
			}
			requireBlockedWith(t, w, issue, run, statemachine.BlockReleaseInterrupted)
		})
	}
}

// An emergency stop in force with the issue lease of an unwatched release
// held by the stop, as an earlier build of the stop held it, still lets the
// recovery roll the release back: a rollback is never frozen.
func TestAnEmergencyStopThatHoldsTheLeaseDoesNotKeepAnUnjudgedReleaseServing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t)
	issue, run := issueRef(85), core.RunID("run-stopped")
	w.seedInFlight(issue, run, firstBuild, core.StateMonitoring)
	w.clock.Advance(3 * DefaultWindow)
	w.holdLease(issue, "killswitch")
	w.freeze.frozen.Store(true)
	out, err := w.pipeline().Recover(ctx, issue, testUnit)
	if !core.HasCode(err, core.CodeLeaseHeld) || out.RestoredTo != lkgDigest || !strings.Contains(out.Waits, "killswitch") {
		t.Fatalf("Recover during the stop = %+v, %v; want the LKG restored and a wait naming the stop", out, err)
	}
	if _, rollbacks, _, _ := w.backend.snapshot(); len(rollbacks) != 1 || rollbacks[0] != lkgDigest {
		t.Fatalf("rollbacks %v, want one to the LKG", rollbacks)
	}
}
