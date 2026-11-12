package agentrun_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/adapters/agent/fake"
	"github.com/bestagentkits/sisf/internal/agentrun"
	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

const sha = core.CommitSHA("0123456789abcdef0123456789abcdef01234567")

var issue = core.IssueRef{Owner: "acme", Repo: "app", Number: 7}

// rig is a controller over a migrated store and one fake backend per provider.
type rig struct {
	t         *testing.T
	db        *sqlstoretest.Database
	anthropic *fake.Backend
	openai    *fake.Backend
	ctl       *agentrun.Controller
	seq       atomic.Int64
}

func withRig(t *testing.T, mutate func(*manifest.ModelRoutes), fn func(*rig)) {
	t.Helper()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		anthropic, err := fake.New(fake.Config{Name: "fake-anthropic", Clock: db.Clock, Roles: []agent.Role{agent.RoleCoder, agent.RoleQA}})
		if err != nil {
			t.Fatal(err)
		}
		openai, err := fake.New(fake.Config{Name: "fake-openai", Clock: db.Clock, Roles: []agent.Role{agent.RoleCoder, agent.RoleQA}})
		if err != nil {
			t.Fatal(err)
		}
		r := routes()
		if mutate != nil {
			mutate(&r)
		}
		router, err := agentrun.NewRouter(r)
		if err != nil {
			t.Fatal(err)
		}
		ctl, err := agentrun.New(agentrun.Config{
			State: db.Open(t, core.ZoneAgent), Router: router, Clock: db.Clock,
			Backends: map[string]agent.Backend{"anthropic": anthropic, "OpenAI": openai},
		})
		if err != nil {
			t.Fatal(err)
		}
		fn(&rig{t: t, db: db, anthropic: anthropic, openai: openai, ctl: ctl})
	})
}

func (r *rig) job(step core.StepKind) agentrun.Job {
	return agentrun.Job{
		RunID: core.RunID("run-" + string(rune('a'+r.seq.Add(1)))), Issue: issue, Step: step,
		Policy: core.PolicyRefs{PolicySHA: sha, ReviewSHA: sha, DecisionSHA: sha, FeatureCatalogSHA: sha,
			// Whatever the caller puts here is replaced by the routed model.
			ModelRoute: route("anthropic", "caller-chosen", "max")},
		Handoff:   agent.Handoff{SoW: "Add GET /version.", DoD: "GET /version returns the build revision."},
		Limits:    agent.Limits{Timeout: time.Hour},
		Workspace: agent.WorkspaceRef{ID: "ws", Path: "/work/ws"},
	}
}

func (r *rig) start(job agentrun.Job) agentrun.RunState {
	r.t.Helper()
	st, err := r.ctl.Start(context.Background(), job)
	if err != nil {
		r.t.Fatalf("start: %v", err)
	}
	return st
}

// observe reconciles the run and returns its state.
func (r *rig) observe(id core.RunID) agentrun.RunState {
	r.t.Helper()
	st, err := r.ctl.Observe(context.Background(), id)
	if err != nil {
		r.t.Fatalf("observe: %v", err)
	}
	return st
}

func TestARunStartsOnTheRouteItsStepKindIsConfiguredForNotOnOneTheCallerNames(t *testing.T) {
	t.Parallel()
	withRig(t, nil, func(r *rig) {
		st := r.start(r.job(core.StepCoding))
		tasks := r.anthropic.Started(issue)
		if len(tasks) != 1 || tasks[0].Policy.ModelRoute != route("anthropic", "mid", "low") {
			t.Fatalf("started %+v, want one task on anthropic/mid@low", tasks)
		}
		if got := st.Current().Route; got != route("anthropic", "mid", "low") || st.Policy.ModelRoute != got {
			t.Errorf("stored route %v / %v", got, st.Policy.ModelRoute)
		}
		if st.Status != agentrun.StatusRunning || st.Current().Agent == nil || st.Current().Backend != "fake-anthropic" {
			t.Errorf("state %+v", st)
		}
		qa := r.start(r.job(core.StepQA))
		if got := r.openai.Started(issue); len(got) != 1 || got[0].Role != agent.RoleQA || got[0].Policy.ModelRoute != route("openai", "mid", "low") || qa.Current().Route.Provider != "openai" {
			t.Errorf("the QA step must run on its own provider as the evaluator: %+v", got)
		}
	})
}

func TestARunThatPassesQAIsNotRunAgain(t *testing.T) {
	t.Parallel()
	withRig(t, nil, func(r *rig) {
		job := r.job(core.StepCoding)
		r.start(job)
		st := r.observe(job.RunID)
		if st.Status != agentrun.StatusAwaitingQA || len(st.Current().Artifacts) == 0 || st.Current().Outcome != agentrun.OutcomeSubmitted {
			t.Fatalf("after the agent finished: %+v", st)
		}
		st, err := r.ctl.ReportQA(context.Background(), job.RunID, agentrun.Verdict{Passed: true, Evidence: []string{"receipt-1"}})
		if err != nil || st.Status != agentrun.StatusPassed || st.Ladder.Used != 0 || len(st.Attempts) != 1 {
			t.Fatalf("after a pass: %+v %v", st, err)
		}
		if n := len(r.anthropic.Started(issue)); n != 1 {
			t.Errorf("%d agents were started, want exactly one: a pass must never cost a re-run", n)
		}
	})
}

// Cheap first: only a QA failure re-runs, one rung higher each time, and each
// fresh agent is built from the handoff only.
func TestAQAFailureReRunsOneRungUpTheLadderFromTheHandoffUntilTheCapIsReached(t *testing.T) {
	t.Parallel()
	withRig(t, nil, func(r *rig) {
		job := r.job(core.StepCoding)
		r.start(job)
		wantRoutes := []core.ModelRoute{
			route("anthropic", "mid", "low"), route("anthropic", "mid", "low"),
			route("anthropic", "mid", "high"), route("anthropic", "large", "low"),
		}
		var st agentrun.RunState
		for i, want := range wantRoutes {
			st = r.observe(job.RunID)
			if st.Status != agentrun.StatusAwaitingQA || st.Current().Route != want || len(st.Attempts) != i+1 {
				t.Fatalf("attempt %d: status %s route %v attempts %d, want %v", i+1, st.Status, st.Current().Route, len(st.Attempts), want)
			}
			var err error
			st, err = r.ctl.ReportQA(context.Background(), job.RunID, agentrun.Verdict{
				Summary: "GET /version returns 404 on the preview", Evidence: []string{"qa-report-" + string(rune('1'+i))}})
			if err != nil {
				t.Fatalf("report %d: %v", i+1, err)
			}
		}
		if st.Status != agentrun.StatusExhausted || st.Ladder.Used != 3 || st.QAFailures != 4 {
			t.Errorf("after the cap: status %s used %d failures %d", st.Status, st.Ladder.Used, st.QAFailures)
		}
		tasks := r.anthropic.Started(issue)
		if len(tasks) != 4 {
			t.Fatalf("%d agents started, want 4 (the first run and three escalations)", len(tasks))
		}
		for i, tk := range tasks {
			if tk.Policy.ModelRoute != wantRoutes[i] || tk.RunID != job.RunID {
				t.Errorf("task %d on %v", i+1, tk.Policy.ModelRoute)
			}
		}
		second := tasks[1].Handoff
		if !contains(second.Decisions, "Verification is required") || !contains(second.Evidence, "qa-report-1") || !contains(second.Decisions, "GET /version returns 404") {
			t.Errorf("the second agent's handoff lacks the verification requirement, the QA finding or its evidence: %+v", second)
		}
		last := tasks[3].Handoff
		if len(last.Decisions) <= len(second.Decisions) || !contains(last.Evidence, "qa-report-3") {
			t.Errorf("the handoff must accumulate decisions and evidence across attempts: %+v", last)
		}
		if tasks[3].Handoff.SoW != job.Handoff.SoW || tasks[3].Handoff.DoD != job.Handoff.DoD {
			t.Error("the scope of work and the definition of done must be carried unchanged")
		}
		if tasks[0].IdempotencyKey == tasks[1].IdempotencyKey {
			t.Error("each attempt needs its own idempotency key")
		}
	})
}

func TestAModelOrEffortChangesOnlyAtAStateBoundary(t *testing.T) {
	t.Parallel()
	withRig(t, nil, func(r *rig) {
		job := r.job(core.StepCoding)
		if err := r.anthropic.Script(agent.RoleCoder, issue, fake.Hang()); err != nil {
			t.Fatal(err)
		}
		r.start(job)
		ctx := context.Background()
		if _, err := r.ctl.ReportQA(ctx, job.RunID, agentrun.Verdict{Summary: "no"}); !core.HasCode(err, core.CodeGuardFailed) {
			t.Errorf("a verdict while the agent still works: want GUARD_FAILED, got %v", err)
		}
		if _, err := r.ctl.Retry(ctx, job.RunID); !core.HasCode(err, core.CodeGuardFailed) {
			t.Errorf("a retry while the agent still works: want GUARD_FAILED, got %v", err)
		}
		if _, err := r.ctl.Resume(ctx, job.RunID); !core.HasCode(err, core.CodeConflict) {
			t.Errorf("a resume while the agent still works: want CONFLICT, got %v", err)
		}
		if n := len(r.anthropic.Started(issue)); n != 1 {
			t.Errorf("%d agents started; nothing may change mid-run", n)
		}
		if st := r.observe(job.RunID); st.Current().Route != route("anthropic", "mid", "low") || st.Status != agentrun.StatusRunning {
			t.Errorf("the running attempt changed: %+v", st.Current())
		}
	})
}

func TestAFailedAgentIsRetriedUpTheSameLadder(t *testing.T) {
	t.Parallel()
	withRig(t, nil, func(r *rig) {
		job := r.job(core.StepCoding)
		if err := r.anthropic.Script(agent.RoleCoder, issue, fake.Fail(0, "tool crashed while editing")); err != nil {
			t.Fatal(err)
		}
		r.start(job)
		st := r.observe(job.RunID)
		if st.Status != agentrun.StatusFailed || st.Current().Outcome != agentrun.OutcomeAgentFailed {
			t.Fatalf("state %+v", st)
		}
		if _, err := r.ctl.ReportQA(context.Background(), job.RunID, agentrun.Verdict{Passed: true}); !core.HasCode(err, core.CodeGuardFailed) {
			t.Errorf("a failed attempt has no candidate to evaluate: %v", err)
		}
		st, err := r.ctl.Retry(context.Background(), job.RunID)
		if err != nil || st.Status != agentrun.StatusRunning || len(st.Attempts) != 2 || !st.Attempts[1].Verify {
			t.Fatalf("retry: %+v %v", st, err)
		}
		if h := r.anthropic.Started(issue)[1].Handoff; !contains(h.Decisions, "tool crashed while editing") {
			t.Errorf("the failure reason is part of the next handoff: %+v", h)
		}
	})
}

func TestALostAgentIsResumedOnTheSameRouteWithoutSpendingAnEscalation(t *testing.T) {
	t.Parallel()
	withRig(t, nil, func(r *rig) {
		job := r.job(core.StepCoding)
		if err := r.anthropic.Script(agent.RoleCoder, issue, fake.Crash(0)); err != nil {
			t.Fatal(err)
		}
		r.start(job)
		st := r.observe(job.RunID)
		if st.Status != agentrun.StatusInterrupted || st.Current().Outcome != agentrun.OutcomeInterrupted {
			t.Fatalf("state %+v", st)
		}
		if _, err := r.ctl.Retry(context.Background(), job.RunID); !core.HasCode(err, core.CodeGuardFailed) {
			t.Errorf("an interrupted run is resumed, not retried: %v", err)
		}
		st, err := r.ctl.Resume(context.Background(), job.RunID)
		if err != nil || st.Status != agentrun.StatusRunning || len(st.Attempts) != 2 {
			t.Fatalf("resume: %+v %v", st, err)
		}
		if st.Ladder.Used != 0 || st.Current().Route != route("anthropic", "mid", "low") || st.Current().Verify {
			t.Errorf("an interruption is not a shortfall: %+v ladder %+v", st.Current(), st.Ladder)
		}
		if h := r.anthropic.Started(issue)[1].Handoff; !contains(h.Evidence, "unfinished changes remain in the workspace") {
			t.Errorf("the fresh agent must be told where the work stands: %+v", h)
		}
	})
}

func TestStartingARunTwiceReturnsTheFirstAndAnotherIssueCannotTakeItsID(t *testing.T) {
	t.Parallel()
	withRig(t, nil, func(r *rig) {
		job := r.job(core.StepCoding)
		first := r.start(job)
		again := r.start(job)
		if again.Current().Agent == nil || *again.Current().Agent != *first.Current().Agent || len(r.anthropic.Started(issue)) != 1 {
			t.Errorf("a repeated start must not start a second agent")
		}
		other := job
		other.Issue = core.IssueRef{Owner: "acme", Repo: "app", Number: 8}
		if _, err := r.ctl.Start(context.Background(), other); !core.HasCode(err, core.CodeConflict) {
			t.Errorf("run id reused by another issue: want CONFLICT, got %v", err)
		}
		if _, err := r.ctl.State(context.Background(), "run-missing"); !core.HasCode(err, core.CodeNotFound) {
			t.Errorf("unknown run: want NOT_FOUND, got %v", err)
		}
	})
}

func TestAProviderWithoutABackendFailsTheAttemptInsteadOfLosingTheRun(t *testing.T) {
	t.Parallel()
	withRig(t, func(r *manifest.ModelRoutes) { r.Routes[core.StepCoding] = route("mistral", "big", "low") }, func(r *rig) {
		st := r.start(r.job(core.StepCoding))
		if st.Status != agentrun.StatusFailed || !strings.Contains(st.Current().Detail, "no backend serves provider mistral") {
			t.Errorf("state %+v", st)
		}
	})
}

func TestCredentialsInTheBriefOrTheVerdictAreNeverStoredOrHandedOn(t *testing.T) {
	t.Parallel()
	const token = "sk-ant-api03-LEAKEDINTHEBRIEF0042"
	withRig(t, nil, func(r *rig) {
		job := r.job(core.StepCoding)
		job.Handoff.SoW = "Call the API with " + token
		job.Handoff.Evidence = []string{"log: GITHUB_TOKEN=ghp_abcdefghijklmnopqrstuvwxyz0123"}
		r.start(job)
		r.observe(job.RunID)
		if _, err := r.ctl.ReportQA(context.Background(), job.RunID, agentrun.Verdict{Summary: "saw " + token, Evidence: []string{"Authorization: Bearer abcdefghijklmnop12345"}}); err != nil {
			t.Fatal(err)
		}
		st, err := r.ctl.State(context.Background(), job.RunID)
		if err != nil {
			t.Fatal(err)
		}
		seen := []string{st.Handoff.SoW, st.Current().Detail}
		seen = append(seen, st.Handoff.Decisions...)
		seen = append(seen, st.Handoff.Evidence...)
		for _, tk := range r.anthropic.Started(issue) {
			seen = append(seen, tk.Handoff.SoW)
			seen = append(seen, tk.Handoff.Decisions...)
			seen = append(seen, tk.Handoff.Evidence...)
		}
		for _, text := range seen {
			for _, secret := range []string{token, "ghp_abcdefghijklmnopqrstuvwxyz0123", "abcdefghijklmnop12345"} {
				if strings.Contains(text, secret) {
					t.Errorf("secret %q reached %q", secret, text)
				}
			}
		}
	})
}

func TestAHandoffWithoutScopeIsRefusedBeforeAnythingIsStored(t *testing.T) {
	t.Parallel()
	withRig(t, nil, func(r *rig) {
		job := r.job(core.StepCoding)
		job.Handoff.DoD = " "
		if _, err := r.ctl.Start(context.Background(), job); core.CodeOf(err) != core.CodeInvalidArgument {
			t.Errorf("want INVALID_ARGUMENT, got %v", err)
		}
		if _, err := r.ctl.State(context.Background(), job.RunID); !core.HasCode(err, core.CodeNotFound) {
			t.Errorf("a refused job must leave no state: %v", err)
		}
		if _, err := agentrun.New(agentrun.Config{}); core.CodeOf(err) != core.CodeInvalidArgument {
			t.Errorf("a controller needs its parts: %v", err)
		}
	})
}

func contains(list []string, part string) bool {
	for _, s := range list {
		if strings.Contains(s, part) {
			return true
		}
	}
	return false
}
