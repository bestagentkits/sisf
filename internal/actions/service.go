package actions

import (
	"context"
	"time"

	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
)

// ApprovalDecisionAction is the action that records an approval. It is
// sensitive and needs step-up, but it is the approval step itself, so it
// never waits for an approval of its own.
const ApprovalDecisionAction = "approvals.decide"

// Defaults applied when the configuration leaves a duration at zero.
const (
	DefaultRequestTTL = 24 * time.Hour
)

// ApprovalStore is the part of the approval store the pipeline uses;
// *approvals.KVStore is one.
type ApprovalStore interface {
	Create(ctx context.Context, r approvals.Request) (approvals.Request, error)
	Get(ctx context.Context, id string) (approvals.Request, error)
	Consume(ctx context.Context, requestID string, subject core.Digest, policy core.CommitSHA) (approvals.Approval, error)
}

// PolicyRevisionSource returns the policy commit in force. Approvals are
// bound to it, so a policy change voids approvals given under the old one.
type PolicyRevisionSource interface {
	PolicyRevision(ctx context.Context) (core.CommitSHA, error)
}

// PolicyRevisionFunc adapts a function to PolicyRevisionSource.
type PolicyRevisionFunc func(ctx context.Context) (core.CommitSHA, error)

// PolicyRevision calls f.
func (f PolicyRevisionFunc) PolicyRevision(ctx context.Context) (core.CommitSHA, error) {
	return f(ctx)
}

// Config is what a Service is built from. Every field but Clock, RequestTTL
// and StepUpMaxAge is required: a service that could not authorize, approve
// or audit would let actions through that nobody can account for.
type Config struct {
	FactoryID  core.FactoryID
	Registry   *Registry
	Authorizer core.Authorizer
	Approvals  ApprovalStore
	Policy     PolicyRevisionSource
	Audit      core.AuditLog
	// Clock is the source of time; nil means the wall clock.
	Clock core.Clock
	// RequestTTL is how long a filed approval request stays open.
	RequestTTL time.Duration
	// StepUpMaxAge is how recently a person must have confirmed their
	// identity to execute an action that needs step-up.
	StepUpMaxAge time.Duration
	// Roles resolves the roles of the caller at the time of each call. When
	// set, the roles a surface put in the actor are replaced by what it
	// returns, so no surface and no caller can claim a role. Nil keeps the
	// actor's roles, for tests that build actors by hand.
	Roles RoleResolver
	// RateLimit bounds how many writes one actor may run in a window; the
	// zero value means DefaultWriteRate.
	RateLimit RateLimit
	// StepUpPage is the dashboard page where a person confirms their
	// identity and decides. A STEP_UP_REQUIRED answer names it, because a
	// token, on any surface, never carries a step-up. Empty names none.
	StepUpPage string
	// StepUp spends the confirmations people make in the dashboard. Nil
	// means no call that needs step-up can run.
	StepUp StepUpProofs
	// Idempotency keeps the answers of writes called with an idempotency
	// key, so a retry gets the first answer instead of a second write. Nil
	// keeps them in this process's memory.
	Idempotency IdempotencyRecords
}

// Service is the one action service behind every surface. Dashboard, REST,
// CLI, MCP and WebMCP all call Invoke, so none of them holds extra power.
type Service struct {
	cfg     Config
	limiter *writeLimiter
	keys    keyedLocks
}

// NewService builds the service, refusing a configuration that lacks a part.
func NewService(cfg Config) (*Service, error) {
	switch {
	case cfg.Registry == nil || cfg.Authorizer == nil || cfg.Approvals == nil || cfg.Policy == nil || cfg.Audit == nil:
		return nil, core.NewError(core.CodeInvalidArgument,
			"the action service needs a registry, an authorizer, an approval store, a policy source and an audit log")
	case cfg.RequestTTL < 0 || cfg.StepUpMaxAge < 0:
		return nil, core.NewError(core.CodeInvalidArgument, "durations of the action service must not be negative")
	case cfg.RateLimit.Writes < 0 || cfg.RateLimit.Window < 0:
		return nil, core.NewError(core.CodeInvalidArgument, "the write rate limit must not be negative")
	}
	if err := cfg.FactoryID.Validate(); err != nil {
		return nil, err
	}
	if cfg.Clock == nil {
		cfg.Clock = core.SystemClock{}
	}
	if cfg.RequestTTL == 0 {
		cfg.RequestTTL = DefaultRequestTTL
	}
	if cfg.StepUpMaxAge == 0 {
		cfg.StepUpMaxAge = approvals.DefaultStepUpMaxAge
	}
	if cfg.Idempotency == nil {
		cfg.Idempotency = &memoryIdempotency{}
	}
	return &Service{cfg: cfg, limiter: newWriteLimiter(cfg.RateLimit)}, nil
}

// Specs returns the specs of every registered action, sorted by name.
func (s *Service) Specs() []core.ActionSpec { return s.cfg.Registry.Specs() }

// Invoke runs one action for an authenticated actor. The steps are fixed:
// look the action up, identify the actor, read the input, authorize, then
// run a read, a dry run, a plain write, or a sensitive write through
// plan -> approval -> execute -> audit. Every failure is a *core.Error with
// next_actions.
func (s *Service) Invoke(ctx context.Context, name string, req core.ActionRequest) (core.ActionResult, error) {
	action, ok := s.cfg.Registry.Lookup(name)
	if !ok {
		return core.ActionResult{}, s.unknown(name)
	}
	res, err := s.invoke(ctx, action, req)
	if err != nil {
		return core.ActionResult{}, s.withNextActions(action.Spec, err)
	}
	return res, nil
}

func (s *Service) invoke(ctx context.Context, action core.Action, req core.ActionRequest) (core.ActionResult, error) {
	spec := action.Spec
	if err := authenticated(req.Actor); err != nil {
		return core.ActionResult{}, err
	}
	if err := s.resolveRoles(ctx, &req); err != nil {
		return core.ActionResult{}, err
	}
	canonical, err := canonicalInput(req.Input)
	if err != nil {
		return core.ActionResult{}, err
	}
	req.Input = canonical
	if err := validIdempotencyKey(req.IdempotencyKey); err != nil {
		return core.ActionResult{}, err
	}
	// A step-up time comes only from a confirmation spent for this call,
	// never from the surface or the session.
	req.Actor.StepUpAt = time.Time{}
	call := invocation{spec: spec, handle: action.Handle, req: req}
	effect, err := s.authorize(ctx, &call)
	if err != nil {
		s.audit(ctx, call, core.OutcomeDenied, "authorize", binding{}, err)
		return core.ActionResult{}, err
	}
	if s.idempotent(call) {
		return s.once(ctx, call, func() (core.ActionResult, error) { return s.run(ctx, call, effect) })
	}
	return s.run(ctx, call, effect)
}

// run carries an authorized call through the write limit and the pipeline
// its action and the authorizer's answer demand.
func (s *Service) run(ctx context.Context, call invocation, effect core.AuthzEffect) (core.ActionResult, error) {
	spec := call.spec
	if err := s.limitWrites(call); err != nil {
		s.audit(ctx, call, core.OutcomeDenied, "authorize", binding{}, err)
		return core.ActionResult{}, err
	}
	switch {
	case effect == core.EffectRequireApproval && !spec.Mutates():
		// A read has nothing an approval could be bound to; the demand is
		// honoured by refusing, never by ignoring it.
		err := core.NewError(core.CodePermissionDenied, "the authorizer demands an approval that a read cannot carry")
		s.audit(ctx, call, core.OutcomeDenied, "authorize", binding{}, err)
		return core.ActionResult{}, err
	case s.cfg.Registry.IsFallback(spec.Name), !spec.Mutates():
		// A fallback only reports what is missing and a read changes nothing:
		// neither needs a plan, an approval or a dry run.
		call.req.DryRun, call.req.ApprovalID = false, ""
		return call.handle(ctx, call.req)
	case call.req.DryRun:
		return s.dryRun(ctx, call, s.needsApproval(spec, effect))
	case s.needsApproval(spec, effect) && call.req.ApprovalID == "":
		return core.ActionResult{}, s.plan(ctx, call)
	case s.needsApproval(spec, effect):
		return s.executeApproved(ctx, call)
	}
	call.req.ApprovalID = ""
	return s.execute(ctx, call, binding{})
}

// invocation is one call as the pipeline carries it.
type invocation struct {
	spec   core.ActionSpec
	handle core.ActionHandler
	req    core.ActionRequest
}

// needsApproval reports whether the call must spend an approval first.
func (s *Service) needsApproval(spec core.ActionSpec, effect core.AuthzEffect) bool {
	if spec.Name == ApprovalDecisionAction {
		return false
	}
	return spec.Sensitivity.NeedsApproval() || effect == core.EffectRequireApproval
}
