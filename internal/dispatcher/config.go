// Package dispatcher hands Ready issues to an agent backend and watches the
// runs it starts.
//
// One dispatcher is active at a time: it works only while it holds the
// dispatcher lease, and a second instance stays on standby until that lease
// lapses. A claim is one store transaction that takes the lease on the
// issue, names a new run, has the budget gate admit it and writes the step
// into Claimed; if any part is refused, none of it is written.
//
// Every report about a run, whether the dispatcher makes it while watching
// the agent or a caller makes it through Report, carries the fencing token
// of the issue lease and is applied only by the latest holder of a lease
// that is still live. A worker that stopped renewing loses its lease, the
// next active dispatcher takes the run over from the stored state, and
// whatever the old worker reports afterwards is refused.
//
// Run timeout, attempt limit and concurrency cap apply in every budget mode:
// an unlimited budget lifts the factory's own monetary limit and nothing else.
package dispatcher

import (
	"context"
	"time"

	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// Defaults for the optional fields of Config.
const (
	DefaultLeaseTTL     = 2 * time.Minute
	DefaultPollInterval = 15 * time.Second
)

// Brief is what a run starts from: the approved scope and definition of
// done, and the policy and model route it runs under. The dispatcher stores
// it with the run when it claims the issue, so a later edit of the issue or
// a restart does not change what the run works on.
type Brief struct {
	Handoff agent.Handoff   `json:"handoff"`
	Policy  core.PolicyRefs `json:"policy"`
	// ScopeDigest identifies the scope version the handoff was taken from.
	ScopeDigest core.Digest `json:"scope_digest,omitempty"`
}

// validate refuses a brief an agent could not work from.
func (b Brief) validate() error {
	probe := agent.Task{
		RunID: "probe", IssueID: core.IssueRef{Owner: "probe", Repo: "probe", Number: 1},
		Role: agent.RoleCoder, Step: core.StepCoding, Handoff: b.Handoff, Policy: b.Policy,
		Limits: agent.Limits{Timeout: time.Second}, IdempotencyKey: "probe",
	}
	if err := probe.Validate(); err != nil {
		return err
	}
	if b.ScopeDigest != "" {
		return b.ScopeDigest.Validate()
	}
	return nil
}

// BriefSource supplies the brief of a Ready issue. It is called outside any
// transaction of the dispatcher, so it may read the store itself. An error
// leaves the issue unclaimed.
type BriefSource interface {
	Brief(ctx context.Context, issue core.IssueRef) (Brief, error)
}

// BriefFunc adapts a function to BriefSource.
type BriefFunc func(ctx context.Context, issue core.IssueRef) (Brief, error)

// Brief calls f.
func (f BriefFunc) Brief(ctx context.Context, issue core.IssueRef) (Brief, error) {
	return f(ctx, issue)
}

// Limits bound every run whatever the budget mode.
type Limits struct {
	// RunTimeout is how long one agent attempt may take before it is
	// cancelled and counted as failed.
	RunTimeout time.Duration
	// MaxAttempts is how many agent attempts one run may make.
	MaxAttempts int
	// MaxConcurrent is how many runs may have an agent working at once.
	MaxConcurrent int
}

// Config builds a dispatcher.
type Config struct {
	// Owner names this dispatcher instance in every lease it takes. A
	// restarted instance may reuse its name: each acquisition issues a new
	// token, which fences off what the earlier incarnation left in flight.
	Owner string
	// State is the operational store, opened for the agent zone.
	State state.Store
	// Agent runs the coding agent.
	Agent agent.Backend
	// Budget admits each claim; see ConfirmedBudgetGate.
	Budget core.BudgetGate
	// Briefs supplies what a run starts from.
	Briefs BriefSource
	Limits Limits
	// Estimate is the amount set aside per run; zero suits an unlimited budget.
	Estimate core.Micros
	// Clock is the source of time; the wall clock when nil.
	Clock core.Clock
	// LeaseTTL is how long a lease lasts without renewal; DefaultLeaseTTL
	// when zero. A dispatcher that stops ticking loses its leases after it.
	LeaseTTL time.Duration
	// PollInterval is the pause between ticks of Run; DefaultPollInterval
	// when zero. It must be shorter than LeaseTTL.
	PollInterval time.Duration
	// NewRunID names a new run; random when nil. Every name must be new.
	NewRunID func() core.RunID
}

// withDefaults fills the optional fields and refuses an unusable config.
func (c Config) withDefaults() (Config, error) {
	if c.Clock == nil {
		c.Clock = core.SystemClock{}
	}
	if c.LeaseTTL == 0 {
		c.LeaseTTL = DefaultLeaseTTL
	}
	if c.PollInterval == 0 {
		c.PollInterval = DefaultPollInterval
	}
	if c.NewRunID == nil {
		c.NewRunID = randomRunID
	}
	switch {
	case c.Owner == "":
		return c, invalid("the dispatcher needs an owner name for its leases")
	case c.State == nil || c.Agent == nil || c.Budget == nil || c.Briefs == nil:
		return c, invalid("the dispatcher needs a state store, an agent backend, a budget gate and a brief source")
	case c.Limits.RunTimeout <= 0 || c.Limits.MaxAttempts < 1 || c.Limits.MaxConcurrent < 1:
		return c, invalid("the dispatcher needs a positive run timeout, attempt limit and concurrency cap in every budget mode")
	case c.Estimate < 0:
		return c, invalid("the reservation estimate must not be negative")
	case c.LeaseTTL < 0 || c.PollInterval < 0 || c.PollInterval >= c.LeaseTTL:
		return c, invalid("the poll interval must be positive and shorter than the lease lifetime")
	}
	return c, nil
}

// invalid builds an INVALID_ARGUMENT error.
func invalid(message string) *core.Error { return core.NewError(core.CodeInvalidArgument, message) }
