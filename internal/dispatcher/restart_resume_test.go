package dispatcher_test

import (
	"context"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/adapters/agent/fake"
	"github.com/bestagentkits/sisf/internal/dispatcher"
	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// A dispatcher process that is killed and started again, under the same
// name and on a freshly opened store, carries on with the run it had: the
// stored brief, pull requests and artifacts survive, the agent run in flight
// is picked up instead of started again, and the previous incarnation's
// token no longer counts.
func TestARestartedDispatcherResumesItsRunFromTheStore(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		f := newFixture(t, db)
		f.confirm()
		issue := f.readyIssue(1)
		if err := f.agent.Script(agent.RoleCoder, issue, fake.Patch(10*time.Minute, "")); err != nil {
			t.Fatal(err)
		}
		before := f.dispatcher("worker-a", nil)
		tick(t, before)
		run := f.record(issue).RunID
		oldToken := tokenOf(t, before, run)
		reported := dispatcher.Report{Issue: issue, RunID: run, LeaseOwner: "worker-a", FencingToken: oldToken,
			Artifacts: []agent.Artifact{patchArtifact("build.log")}, PullRequests: []string{"acme/app#41"}}
		if err := before.Report(context.Background(), reported); err != nil {
			t.Fatalf("the lease holder's report was refused: %v", err)
		}

		// Restart: a new process opens the store again and keeps its name.
		reopened := db.Open(t, core.ZoneAgent)
		after, err := dispatcher.New(f.config("worker-a", reopened))
		if err != nil {
			t.Fatal(err)
		}
		res := tick(t, after)
		if !res.Leader || len(res.Resumed) != 1 || len(res.Claimed) != 0 {
			t.Fatalf("the restarted dispatcher did not resume its run: %+v", res)
		}
		if err := after.Report(context.Background(), reported); !core.HasCode(err, core.CodeStaleFencingToken) {
			t.Fatalf("the previous incarnation's token still counts: %v", err)
		}
		record := runRecord(t, after, run)
		switch {
		case record.Brief.Handoff.SoW != brief().Handoff.SoW || record.Brief.Handoff.DoD != brief().Handoff.DoD || record.Brief.Policy != brief().Policy:
			t.Errorf("the stored brief was lost: %+v", record.Brief)
		case len(record.PullRequests) != 1 || record.PullRequests[0] != "acme/app#41":
			t.Errorf("the pull request reference was lost: %v", record.PullRequests)
		case len(record.Artifacts) != 1 || record.Artifacts[0].Name != "build.log":
			t.Errorf("the reported artifact was lost: %v", record.Artifacts)
		}

		f.tickUntil(after, 30, func(r dispatcher.TickResult) bool { return len(r.Succeeded) == 1 })
		record = runRecord(t, after, run)
		if record.Status != dispatcher.RunSucceeded || len(record.Artifacts) != 2 || len(record.PullRequests) != 1 {
			t.Fatalf("the finished run does not hold what was reported and produced: %+v", record)
		}
		if started := len(f.agent.Started(issue)); started != 1 || f.runRecords() != 1 {
			t.Fatalf("the restart started the agent %d times and recorded %d runs, want once each", started, f.runRecords())
		}
	})
}

func TestAMalformedOrUnleasedReportIsRefused(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		f := newFixture(t, db)
		f.confirm()
		issue := f.readyIssue(1)
		if err := f.agent.Script(agent.RoleCoder, issue, fake.Hang()); err != nil {
			t.Fatal(err)
		}
		d := f.dispatcher("worker-a", nil)
		tick(t, d)
		run := f.record(issue).RunID
		valid := func() dispatcher.Report {
			return dispatcher.Report{Issue: issue, RunID: run, LeaseOwner: "worker-a", FencingToken: tokenOf(t, d, run),
				Artifacts: []agent.Artifact{patchArtifact("a.log")}}
		}
		cases := map[string]struct {
			mutate func(*dispatcher.Report)
			want   core.ErrorCode
		}{
			"no token":              {func(r *dispatcher.Report) { r.FencingToken = 0 }, core.CodeStaleFencingToken},
			"no owner":              {func(r *dispatcher.Report) { r.LeaseOwner = "" }, core.CodeStaleFencingToken},
			"an older token":        {func(r *dispatcher.Report) { r.FencingToken-- }, core.CodeStaleFencingToken},
			"a token never issued":  {func(r *dispatcher.Report) { r.FencingToken += 10 }, core.CodeStaleFencingToken},
			"another owner":         {func(r *dispatcher.Report) { r.LeaseOwner = "worker-b" }, core.CodeStaleFencingToken},
			"another run":           {func(r *dispatcher.Report) { r.RunID = "run-9999" }, core.CodeConflict},
			"nothing to report":     {func(r *dispatcher.Report) { r.Artifacts = nil }, core.CodeInvalidArgument},
			"artifact of no kind":   {func(r *dispatcher.Report) { r.Artifacts[0].Kind = "" }, core.CodeInvalidArgument},
			"artifact by tag":       {func(r *dispatcher.Report) { r.Artifacts[0].Digest = "latest" }, core.CodeInvalidArgument},
			"padded pull request":   {func(r *dispatcher.Report) { r.PullRequests = []string{" acme/app#1"} }, core.CodeInvalidArgument},
			"issue that is not one": {func(r *dispatcher.Report) { r.Issue = core.IssueRef{} }, core.CodeInvalidArgument},
		}
		for name, c := range cases {
			report := valid()
			c.mutate(&report)
			if err := d.Report(context.Background(), report); core.CodeOf(err) != c.want {
				t.Errorf("%s: want %s, got %v", name, c.want, err)
			}
		}
		if record := runRecord(t, d, run); len(record.Artifacts) != 0 {
			t.Fatalf("a refused report was written: %+v", record.Artifacts)
		}
		// A lease that lapsed without a successor is not live either.
		report := valid()
		f.clock.Advance(leaseTTL)
		if err := d.Report(context.Background(), report); !core.HasCode(err, core.CodeExpired) {
			t.Fatalf("a report under a lapsed lease was not refused: %v", err)
		}
	})
}
