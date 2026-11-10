package dispatcher_test

import (
	"context"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/adapters/agent/fake"
	"github.com/bestagentkits/sisf/internal/dispatcher"
	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// A worker that crashes in the middle of a run stops renewing its leases.
// Once they lapse a second worker becomes the active dispatcher and takes
// the run over from the stored state: the same run, the same agent run, no
// new claim. Everything the first worker reports afterwards is refused, so
// its late result can neither overwrite the run nor start another one.
func TestAWorkerThatCrashesMidRunIsFencedAndItsRunResumedOnce(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		f := newFixture(t, db)
		f.confirm()
		issue := f.readyIssue(1)
		if err := f.agent.Script(agent.RoleCoder, issue, fake.Patch(20*time.Minute, "+fix\n")); err != nil {
			t.Fatal(err)
		}
		first := f.dispatcher("worker-a", nil)
		second := f.dispatcher("worker-b", nil)

		if res := tick(t, first); len(res.Claimed) != 1 {
			t.Fatalf("the first worker claimed %v, want the Ready issue", res.Claimed)
		}
		claimed := f.record(issue)
		if claimed.State != core.StateDeveloping || claimed.Attempt != 1 {
			t.Fatalf("after the claim the issue is %s at attempt %d, want Developing at 1", claimed.State, claimed.Attempt)
		}
		run := claimed.RunID
		staleToken := tokenOf(t, first, run)
		if res := tick(t, second); res.Leader || len(res.Resumed)+len(res.Claimed) != 0 {
			t.Fatalf("a second worker acted while the first held the dispatcher lease: %+v", res)
		}

		// The first worker crashes: it never ticks again on its own.
		f.clock.Advance(leaseTTL + time.Second)
		res := tick(t, second)
		if !res.Leader || len(res.Resumed) != 1 || res.Resumed[0] != issue || len(res.Claimed) != 0 {
			t.Fatalf("the second worker did not take the run over: %+v", res)
		}
		resumed := f.record(issue)
		if resumed.RunID != run || resumed.State != core.StateDeveloping || resumed.Attempt != 1 {
			t.Fatalf("the run was not resumed as stored: %+v", resumed)
		}
		if got := tokenOf(t, second, run); got <= staleToken {
			t.Fatalf("the successor holds token %d, not newer than the crashed worker's %d", got, staleToken)
		}

		// The crashed worker's late report is fenced off, whoever relays it.
		late := dispatcher.Report{Issue: issue, RunID: run, LeaseOwner: "worker-a", FencingToken: staleToken,
			Artifacts: []agent.Artifact{patchArtifact("late.log")}}
		for _, d := range []*dispatcher.Dispatcher{first, second} {
			if err := d.Report(context.Background(), late); !core.HasCode(err, core.CodeStaleFencingToken) {
				t.Fatalf("a report under the crashed worker's token was not refused as stale: %v", err)
			}
		}
		// Nor can it write a lifecycle step with its old token.
		err := f.store.Update(context.Background(), func(ctx context.Context, tx state.Tx) error {
			_, err := tx.Transition(ctx, core.Transition{
				IssueID: issue, From: core.StateDeveloping, To: core.StateQAEvaluation, Event: core.EventCandidateSubmitted,
				ExpectedVersion: resumed.Version, IdempotencyKey: "late-candidate", LeaseOwner: "worker-a",
				FencingToken: staleToken, RunID: run, Attempt: 1,
				CandidateDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			})
			return err
		})
		if !core.HasCode(err, core.CodeStaleFencingToken) {
			t.Fatalf("a lifecycle step under the stale token was not refused: %v", err)
		}
		// When the crashed worker comes back it finds itself standing by.
		if res := tick(t, first); res.Leader || len(res.Lost) != 1 || len(res.Claimed)+len(res.Resumed) != 0 {
			t.Fatalf("the crashed worker acted after losing its leases: %+v", res)
		}

		f.tickUntil(second, 60, func(r dispatcher.TickResult) bool { return len(r.Succeeded) == 1 })
		final := f.record(issue)
		record := runRecord(t, second, run)
		switch {
		case final.RunID != run || final.Attempt != 1:
			t.Errorf("the run changed: %+v", final)
		case record.Status != dispatcher.RunSucceeded || record.Attempt != 1:
			t.Errorf("the run ended %s after %d attempts, want succeeded after 1", record.Status, record.Attempt)
		case len(record.Artifacts) != 1 || record.Artifacts[0].Kind != agent.ArtifactPatch:
			t.Errorf("the run kept %+v, want only the agent's patch", record.Artifacts)
		}
		if started := len(f.agent.Started(issue)); started != 1 {
			t.Errorf("the agent was started %d times, want once", started)
		}
		if n := f.runRecords(); n != 1 {
			t.Errorf("%d runs were recorded for the issue, want 1", n)
		}
		if err := second.Report(context.Background(), late); !core.HasCode(err, core.CodeStaleFencingToken) {
			t.Errorf("after the run finished the stale report was still not refused: %v", err)
		}
	})
}
