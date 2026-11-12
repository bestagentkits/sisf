// Package agentjudge is the production qa.Judge: it scores a candidate by
// running a QA agent (Claude, Codex or Pi, through their agent adapters) in
// the QA role and reading one strictly checked judgment from the agent's
// final message.
//
// The judge only scores. It never totals, weighs or decides: the evaluator
// applies the rubric weights, the pass score and the hard gates. Every way
// a judgment can go wrong (an agent that fails, hangs or exits non-zero, a
// judgment that is malformed, incomplete, out of range, repeated or too
// large) is a typed error, never a default score, so no receipt follows.
//
// Independence: the agent runs as the evaluator's identity, never the
// coder's, on the evaluator's model route, in a fresh read-only workspace
// that holds only the candidate's diff and files, with the environment the
// adapter is configured with. The coder's session, workspace and secrets
// are never part of it.
package agentjudge

import (
	"context"
	"crypto/rand"
	"path/filepath"
	"strings"
	"time"

	evaluator "github.com/bestagentkits/sisf/internal/qa"
	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/qa"
)

// Defaults of a judge.
const (
	DefaultTimeout = 15 * time.Minute
	// cancelGrace bounds the wait for a timed-out run's process to go.
	cancelGrace = 10 * time.Second
)

// Runner is an agent backend whose runs can be waited for; the built-in
// Claude, Codex and Pi adapters are runners.
type Runner interface {
	agent.Backend
	Wait(ctx context.Context, run agent.RunRef) (agent.Status, error)
}

// UsageSink records what a judgment consumed, as the budget ledger does.
type UsageSink interface {
	AppendUsage(ctx context.Context, u core.Usage) error
}

// Config builds a Judge.
type Config struct {
	// Agent runs the QA agent. Required.
	Agent Runner
	// Materials gathers what the agent is shown of a candidate. Required.
	Materials MaterialsSource
	// WorkspaceRoot holds the fresh workspace of each judgment. Required,
	// absolute; never inside a coder's workspace or the trust volume.
	WorkspaceRoot string
	// EvidenceDir keeps the judgment of each run, the evidence its scores
	// cite; <WorkspaceRoot>/evidence when empty.
	EvidenceDir string
	// Model, when set, is the model the agent runs instead of the model of
	// the evaluator's route; its provider and effort are kept. It is refused
	// when it would put QA on the coder's provider and model.
	Model string
	// Timeout bounds one judgment; DefaultTimeout when zero.
	Timeout time.Duration
	// Usage, when set, records what each judgment consumed.
	Usage UsageSink
	// FactoryID is recorded on usage.
	FactoryID core.FactoryID
	// Clock dates the evidence; the wall clock when nil.
	Clock core.Clock
}

// Judge is the agent-backed qa.Judge. It is safe for concurrent use when
// its agent and materials source are.
type Judge struct{ cfg Config }

var (
	_ qa.Judge                = (*Judge)(nil)
	_ evaluator.RouteResolver = (*Judge)(nil)
)

// New checks the configuration and returns the judge.
func New(cfg Config) (*Judge, error) {
	switch {
	case cfg.Agent == nil || cfg.Materials == nil:
		return nil, core.NewError(core.CodeInvalidArgument, "the QA judge needs an agent and a materials source")
	case cfg.WorkspaceRoot == "" || !filepath.IsAbs(cfg.WorkspaceRoot):
		return nil, core.NewError(core.CodeInvalidArgument, "the QA judge needs an absolute workspace root")
	case cfg.EvidenceDir != "" && !filepath.IsAbs(cfg.EvidenceDir):
		return nil, core.NewError(core.CodeInvalidArgument, "the QA judge's evidence directory must be absolute")
	case cfg.Timeout < 0:
		return nil, core.NewError(core.CodeInvalidArgument, "the QA judge's time limit must not be negative")
	case cfg.Model != "" && strings.TrimSpace(cfg.Model) != cfg.Model:
		return nil, core.NewError(core.CodeInvalidArgument, "the QA model must not be padded with white space")
	}
	if cfg.Clock == nil {
		cfg.Clock = core.SystemClock{}
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.EvidenceDir == "" {
		cfg.EvidenceDir = filepath.Join(cfg.WorkspaceRoot, "evidence")
	}
	return &Judge{cfg: cfg}, nil
}

// Score runs the QA agent on the candidate and returns its scores.
func (j *Judge) Score(ctx context.Context, req qa.JudgeRequest) (qa.Scorecard, error) {
	route, err := j.route(req)
	if err != nil {
		return qa.Scorecard{}, err
	}
	materials, err := j.cfg.Materials.Materials(ctx, req.Candidate)
	if err != nil {
		return qa.Scorecard{}, err
	}
	if materials, err = materials.check(); err != nil {
		return qa.Scorecard{}, err
	}
	handoff, err := buildHandoff(req, materials)
	if err != nil {
		return qa.Scorecard{}, err
	}
	workspace, err := newWorkspace(j.cfg.WorkspaceRoot, materials)
	if err != nil {
		return qa.Scorecard{}, err
	}
	defer removeWorkspace(workspace)
	run, err := j.run(ctx, j.task(req, route, handoff), workspace)
	if err != nil {
		return qa.Scorecard{}, err
	}
	judgment, err := ParseJudgment(run.final, req.Rubric)
	if err != nil {
		return qa.Scorecard{}, err
	}
	ref, err := j.keep(ctx, run, req, route, judgment)
	if err != nil {
		return qa.Scorecard{}, err
	}
	card := judgment.Scorecard(ref)
	if err := card.Validate(req.Rubric); err != nil {
		return qa.Scorecard{}, refuse("the scores do not fit the rubric: %v", err)
	}
	return card, nil
}

// route checks the request and returns the route the agent runs on: the
// evaluator's, with the configured model when there is one. The evaluator
// already refused a route shared with the coder unless the Owner
// acknowledged a single route; a configured model must not undo that.
func (j *Judge) route(req qa.JudgeRequest) (core.ModelRoute, error) {
	for _, check := range []func() error{req.Candidate.Validate, req.Baseline.Validate, req.Rubric.Validate, req.ModelRoute.Validate} {
		if err := check(); err != nil {
			return core.ModelRoute{}, err
		}
	}
	switch {
	case strings.TrimSpace(req.Identity) == "":
		return core.ModelRoute{}, core.NewError(core.CodeInvalidArgument, "a judgment needs the evaluator's identity")
	case core.SameIdentity(req.Identity, req.Candidate.CodingAgentIdentity):
		return core.ModelRoute{}, core.NewError(core.CodePermissionDenied, "the coding agent cannot judge its own candidate")
	}
	return j.ResolveRoute(req.ModelRoute, req.Candidate.CodingModelRoute)
}

// ResolveRoute returns the route the agent runs on when the evaluator chose
// chosen: chosen with the configured model, when there is one. The
// evaluator records this route on the receipt. A configured model that
// would put QA on the coder's provider and model, which chosen does not
// share, is refused.
func (j *Judge) ResolveRoute(chosen, coder core.ModelRoute) (core.ModelRoute, error) {
	route := chosen
	if j.cfg.Model != "" {
		route.Model = j.cfg.Model
	}
	if route.SameModel(coder) && !chosen.SameModel(coder) {
		return core.ModelRoute{}, core.NewError(core.CodePermissionDenied,
			"the configured QA model would run QA on the coder's provider and model, which the evaluator's route does not")
	}
	return route, nil
}

// task is the agent task of one judgment. Its idempotency key is new for
// every judgment, so a judgment retried after a failure starts a fresh run.
func (j *Judge) task(req qa.JudgeRequest, route core.ModelRoute, handoff agent.Handoff) agent.Task {
	c := req.Candidate
	policy := c.Policy
	policy.ModelRoute = route
	return agent.Task{
		RunID: c.RunID, IssueID: c.IssueID, Role: agent.RoleQA, Step: core.StepQA, Handoff: handoff,
		// The adapter's own limit is a backstop; the judge's ends the run
		// first, so a hang is reported as a timeout.
		Policy: policy, Limits: agent.Limits{Timeout: j.cfg.Timeout + cancelGrace},
		IdempotencyKey: "qa/" + string(c.RunID) + "/" + itoa(c.Attempt) + "/" + rand.Text(),
	}
}
