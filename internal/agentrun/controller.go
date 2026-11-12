package agentrun

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/agent/redact"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// verifyDecision is the standing instruction the verify rung adds to the
// handoff: the agent must be able to check its own work.
const verifyDecision = "Verification is required: run the project's tests and build before finishing, and report the commands you ran and their results as evidence."

// Config builds a Controller.
type Config struct {
	// State stores run states. The controller is a pure function of it and
	// of the backends, so a new process continues any run.
	State  state.Store
	Router *Router
	// Backends maps a model provider (case-insensitive) to the backend that
	// serves it, for example "anthropic" to the Claude backend.
	Backends map[string]agent.Backend
	Clock    core.Clock
}

// Controller runs the model-routing policy over agent backends. It keeps no
// state of its own between calls; everything lives in the store.
type Controller struct {
	state    state.Store
	router   *Router
	backends map[string]agent.Backend
	clock    core.Clock
}

// New builds a controller.
func New(cfg Config) (*Controller, error) {
	if cfg.State == nil || cfg.Router == nil || len(cfg.Backends) == 0 {
		return nil, core.NewError(core.CodeInvalidArgument, "a controller needs a state store, a router and at least one backend")
	}
	c := &Controller{state: cfg.State, router: cfg.Router, backends: map[string]agent.Backend{}, clock: cfg.Clock}
	if c.clock == nil {
		c.clock = core.SystemClock{}
	}
	for provider, backend := range cfg.Backends {
		if backend == nil || strings.TrimSpace(provider) == "" {
			return nil, core.NewError(core.CodeInvalidArgument, "every backend needs a provider name and an implementation")
		}
		c.backends[strings.ToLower(strings.TrimSpace(provider))] = backend
	}
	return c, nil
}

// Job is one run to start.
type Job struct {
	RunID core.RunID
	Issue core.IssueRef
	Step  core.StepKind
	// Policy pins the policy revisions. Its model route is ignored: the route
	// is chosen by code from the step kind.
	Policy    core.PolicyRefs
	Handoff   agent.Handoff
	Limits    agent.Limits
	Workspace agent.WorkspaceRef
	Scopes    []string
}

// Verdict is the independent evaluation of an attempt's candidate.
type Verdict struct {
	Passed bool
	// Summary says why the candidate failed; it goes into the next handoff.
	Summary string
	// Evidence names the receipts or reports behind the verdict.
	Evidence []string
}

// Start starts a run on the route its step kind is configured for. Starting
// a run that already exists returns its state unchanged.
func (c *Controller) Start(ctx context.Context, job Job) (RunState, error) {
	route, err := c.router.Route(job.Step)
	if err != nil {
		return RunState{}, err
	}
	policy := job.Policy
	policy.ModelRoute = route
	st := RunState{
		RunID: job.RunID, Issue: job.Issue, Step: job.Step, Policy: policy, Limits: job.Limits,
		Workspace: job.Workspace, Scopes: slices.Clone(job.Scopes), Handoff: sanitize(job.Handoff),
		Status: StatusRunning,
	}
	st.Attempts = []Attempt{c.newAttempt(st, 1, "initial", route, false)}
	if err := c.task(st).Validate(); err != nil {
		return RunState{}, err
	}
	version, err := save(ctx, c.state, st, 0, c.clock.Now())
	if core.HasCode(err, core.CodeConflict) {
		existing, _, loadErr := load(ctx, c.state, job.RunID)
		if loadErr != nil {
			return RunState{}, loadErr
		}
		if existing.Issue != job.Issue || existing.Step != job.Step {
			return RunState{}, core.NewError(core.CodeConflict, "the run id already belongs to another issue or step")
		}
		return existing, nil
	}
	if err != nil {
		return RunState{}, err
	}
	st, _, err = c.launch(ctx, st, version)
	return st, err
}

// State returns the stored state of a run.
func (c *Controller) State(ctx context.Context, run core.RunID) (RunState, error) {
	st, _, err := load(ctx, c.state, run)
	return st, err
}

// Observe brings the stored state up to date with the backend: a finished
// attempt is recorded with its artifacts and usage, a lost one is marked
// interrupted. It starts an attempt that was recorded but never started.
func (c *Controller) Observe(ctx context.Context, run core.RunID) (RunState, error) {
	st, version, err := load(ctx, c.state, run)
	if err != nil {
		return RunState{}, err
	}
	st, _, err = c.sync(ctx, st, version)
	return st, err
}

// ReportQA records the independent evaluation of the candidate. A pass ends
// the run. A failure is the only thing that runs the work again: the next
// attempt is one rung up the ladder and starts from the updated handoff.
func (c *Controller) ReportQA(ctx context.Context, run core.RunID, verdict Verdict) (RunState, error) {
	st, version, err := c.settled(ctx, run)
	if err != nil {
		return RunState{}, err
	}
	if st.Status != StatusAwaitingQA {
		return st, core.NewError(core.CodeGuardFailed, "the run has no candidate waiting for evaluation")
	}
	cur := st.Current()
	cur.Detail = clip(redact.Text(verdict.Summary))
	if verdict.Passed {
		cur.Outcome, st.Status = OutcomeQAPassed, StatusPassed
		_, err := save(ctx, c.state, st, version, c.clock.Now())
		return st, err
	}
	cur.Outcome = OutcomeQAFailed
	st.QAFailures++
	return c.advance(ctx, st, version, "it failed QA", verdict.Summary, verdict.Evidence)
}

// Retry moves a run whose agent failed up the ladder.
func (c *Controller) Retry(ctx context.Context, run core.RunID) (RunState, error) {
	st, version, err := c.settled(ctx, run)
	if err != nil {
		return RunState{}, err
	}
	if st.Status != StatusFailed {
		return st, core.NewError(core.CodeGuardFailed, "only a run whose agent failed can be retried; an interrupted run is resumed")
	}
	return c.advance(ctx, st, version, "the agent failed", st.Current().Detail, nil)
}

// Resume starts a fresh agent for a run whose attempt was interrupted. The
// route and the ladder position stay as they were, since an interruption is
// not a shortfall. The new agent gets only the handoff and the workspace.
func (c *Controller) Resume(ctx context.Context, run core.RunID) (RunState, error) {
	st, version, err := c.settled(ctx, run)
	if err != nil {
		return RunState{}, err
	}
	switch st.Status {
	case StatusInterrupted:
	case StatusRunning:
		return st, core.NewError(core.CodeConflict, "the attempt is still running, so it cannot be resumed")
	default:
		return st, core.NewError(core.CodeGuardFailed, "only an interrupted run can be resumed")
	}
	prev := *st.Current()
	st.Handoff = extend(st.Handoff,
		[]string{fmt.Sprintf("Attempt %d was interrupted before it finished. A new agent continues on the same route.", prev.Number)},
		[]string{"The interrupted agent's unfinished changes remain in the workspace. Inspect them, keep what is correct and finish the work."})
	st.Attempts = append(st.Attempts, c.newAttempt(st, prev.Number+1, "resume", prev.Route, prev.Verify))
	st.Status = StatusRunning
	version, err = save(ctx, c.state, st, version, c.clock.Now())
	if err != nil {
		return RunState{}, err
	}
	st, _, err = c.launch(ctx, st, version)
	return st, err
}

// settled loads a run and brings it up to date, so a model or effort can
// change only once the current attempt has ended.
func (c *Controller) settled(ctx context.Context, run core.RunID) (RunState, int64, error) {
	st, version, err := load(ctx, c.state, run)
	if err != nil {
		return RunState{}, 0, err
	}
	return c.sync(ctx, st, version)
}

// advance takes the run one rung up the ladder after a shortfall, or marks it
// exhausted when none is left. The handoff gains the decision taken and the
// evidence gathered; the conversation of the attempt that fell short is not
// part of it.
func (c *Controller) advance(ctx context.Context, st RunState, version int64, reason, summary string, evidence []string) (RunState, error) {
	if st.Status == StatusRunning {
		return st, core.NewError(core.CodeGuardFailed, "a model or effort changes only at a state boundary, and the attempt is still running")
	}
	prev := *st.Current()
	rung, pos, err := c.router.Escalate(st.Step, prev.Route, prev.Verify, st.Ladder)
	if core.HasCode(err, core.CodeLimitExceeded) {
		st.Status = StatusExhausted
		_, err := save(ctx, c.state, st, version, c.clock.Now())
		return st, err
	}
	if err != nil {
		return st, err
	}
	decisions := []string{
		fmt.Sprintf("Attempt %d fell short because %s: %s", prev.Number, reason, summaryOrNone(summary)),
		fmt.Sprintf("Attempt %d runs on %s (%s).", prev.Number+1, rung.Route, rung.Kind),
	}
	if rung.Verify && !prev.Verify {
		decisions = append(decisions, verifyDecision)
	}
	for _, a := range prev.Artifacts {
		evidence = append(evidence, fmt.Sprintf("attempt %d left %s %s (%s)", prev.Number, a.Kind, a.Name, a.Digest))
	}
	st.Handoff = extend(st.Handoff, decisions, evidence)
	st.Ladder = pos
	st.Policy.ModelRoute = rung.Route
	st.Attempts = append(st.Attempts, c.newAttempt(st, prev.Number+1, string(rung.Kind), rung.Route, rung.Verify))
	st.Status = StatusRunning
	version, err = save(ctx, c.state, st, version, c.clock.Now())
	if err != nil {
		return RunState{}, err
	}
	st, _, err = c.launch(ctx, st, version)
	return st, err
}

func summaryOrNone(s string) string {
	if s = strings.TrimSpace(s); s != "" {
		return s
	}
	return "no detail was reported"
}

func clip(s string) string {
	if len(s) > maxItemLen {
		return s[:maxItemLen] + "..."
	}
	return s
}

func (c *Controller) newAttempt(st RunState, number int, rung string, route core.ModelRoute, verify bool) Attempt {
	return Attempt{
		Number: number, Rung: rung, Route: route, Verify: verify,
		HandoffDigest: digest(st.Handoff), StartedAt: c.clock.Now(),
	}
}

// task builds the task of the current attempt from the stored state alone.
func (c *Controller) task(st RunState) agent.Task {
	cur := st.Current()
	role := agent.RoleCoder
	if st.Step == core.StepQA {
		role = agent.RoleQA
	}
	policy := st.Policy
	policy.ModelRoute = cur.Route
	return agent.Task{
		RunID: st.RunID, IssueID: st.Issue, Role: role, Step: st.Step,
		Handoff: st.Handoff, Policy: policy, Limits: st.Limits,
		// The key is per attempt, so a start repeated after a crash returns
		// the process started the first time.
		IdempotencyKey: fmt.Sprintf("%s/attempt/%d", st.RunID, cur.Number),
	}
}

func (c *Controller) backendFor(provider string) (agent.Backend, error) {
	b, ok := c.backends[strings.ToLower(strings.TrimSpace(provider))]
	if !ok {
		return nil, core.NewError(core.CodeNotSupported, "no backend serves provider "+provider)
	}
	return b, nil
}

// launch starts the current attempt on its backend if that has not happened.
// A refusal that no retry can fix fails the attempt; any other error leaves
// the state as it was, so the start is tried again.
func (c *Controller) launch(ctx context.Context, st RunState, version int64) (RunState, int64, error) {
	cur := st.Current()
	if cur.Agent != nil || st.Status != StatusRunning {
		return st, version, nil
	}
	backend, err := c.backendFor(cur.Route.Provider)
	var ref agent.RunRef
	if err == nil {
		ref, err = backend.Start(ctx, c.task(st), st.Workspace, st.Scopes)
	}
	if err != nil {
		switch core.CodeOf(err) {
		case core.CodeNotSupported, core.CodeInvalidArgument, core.CodePermissionDenied:
			cur.Outcome, cur.EndedAt, cur.Detail = OutcomeAgentFailed, c.clock.Now(), clip(redact.Text("the agent did not start: "+err.Error()))
			st.Status = StatusFailed
			version, err = save(ctx, c.state, st, version, c.clock.Now())
			return st, version, err
		}
		return st, version, err
	}
	cur.Agent, cur.Backend = &ref, backend.Name()
	if info, ok := backend.(agent.RunInfoProvider); ok {
		if details, infoErr := info.RunInfo(ctx, ref); infoErr == nil {
			cur.ToolVersion = details.ToolVersion
		}
	}
	version, err = save(ctx, c.state, st, version, c.clock.Now())
	return st, version, err
}

// sync reconciles a running attempt with its backend.
func (c *Controller) sync(ctx context.Context, st RunState, version int64) (RunState, int64, error) {
	if st.Status != StatusRunning {
		return st, version, nil
	}
	cur := st.Current()
	if cur.Agent == nil {
		return c.launch(ctx, st, version)
	}
	backend, err := c.backendFor(cur.Route.Provider)
	if err != nil {
		return st, version, err
	}
	status, err := backend.Status(ctx, *cur.Agent)
	now := c.clock.Now()
	switch {
	case core.HasCode(err, core.CodeNotFound) || core.HasCode(err, core.CodeUnavailable):
		// A backend that does not know the run, or cannot reach it, has lost
		// it, as a restarted process does. The work is continued by a fresh agent.
		cur.Outcome, cur.EndedAt, cur.Detail = OutcomeInterrupted, now, "the agent run was lost"
		st.Status = StatusInterrupted
	case err != nil:
		return st, version, err
	case status.Phase == agent.PhaseSucceeded:
		artifacts, err := backend.CollectArtifacts(ctx, *cur.Agent)
		if err != nil {
			return st, version, err
		}
		usage, err := c.usage(ctx, backend, *cur.Agent, true)
		if err != nil {
			return st, version, err
		}
		cur.Artifacts, cur.Usage, cur.Outcome, cur.EndedAt = artifacts, usage, OutcomeSubmitted, now
		st.Status = StatusAwaitingQA
	case status.Phase == agent.PhaseFailed:
		// What the attempt spent is best effort here: the failure is already
		// known and usage cannot change what happens next.
		cur.Usage, _ = c.usage(ctx, backend, *cur.Agent, false)
		cur.Outcome, cur.EndedAt, cur.Detail = OutcomeAgentFailed, now, clip(redact.Text(status.Detail))
		st.Status = StatusFailed
	case status.Phase == agent.PhaseCancelled:
		cur.Outcome, cur.EndedAt, cur.Detail = OutcomeInterrupted, now, "the agent run was cancelled"
		st.Status = StatusInterrupted
	default:
		return st, version, nil
	}
	version, err = save(ctx, c.state, st, version, now)
	return st, version, err
}

// usage asks a backend what a run consumed. A backend that cannot say is not
// an error; with strict set, any other failure is.
func (c *Controller) usage(ctx context.Context, backend agent.Backend, ref agent.RunRef, strict bool) ([]core.Usage, error) {
	reporter, ok := backend.(agent.UsageReporter)
	if !ok {
		return nil, nil
	}
	usage, err := reporter.Usage(ctx, ref)
	if core.HasCode(err, core.CodeNotSupported) {
		return nil, nil
	}
	if err != nil && !strict {
		return nil, nil
	}
	return usage, err
}
