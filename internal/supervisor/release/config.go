// Package release is the trusted release pipeline of the supervisor:
//
//	verify receipt and artifact -> preflight rollback -> stage and preview
//	-> canary 5/25/100 or blue-green -> observed revision and metrics
//	-> promote, record the LKG, publish and close, or restore the LKG
//
// A release is judged only by what is observed running and measured. A
// release that cannot be observed or undone holds; missing metrics are never
// read as success. A candidate that touches the trusted control surface is a
// governance change and is never released by this pipeline.
//
// Every lifecycle step is written to the operational store by the control
// zone, under the issue lease the pipeline holds, after the lifecycle
// machine has judged the proof of its edge.
package release

import (
	"fmt"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/deploy"
	"github.com/bestagentkits/sisf/pkg/state"
)

// Default parameters of a release. They are the brief's starting points and
// are meant to be calibrated on real traffic.
const (
	DefaultWindow             = 10 * time.Minute
	DefaultMinSamples         = 100
	DefaultErrorRateThreshold = 0.02
	DefaultMinDelta           = 0.01
	DefaultConsecutive        = 2
	DefaultPassScore          = 85
	DefaultObservationWindows = 2
	DefaultStepWindows        = 1
	DefaultLowTrafficMinHold  = 30 * time.Minute
	DefaultMaxHoldExtensions  = 3
	// DefaultLeaseOwner is the owner the pipeline holds issue leases as.
	DefaultLeaseOwner = "supervisor"
)

// DefaultCanarySteps returns the traffic shares of a canary rollout.
func DefaultCanarySteps() []int { return []int{5, 25, 100} }

// MonitorConfig is the automatic rollback rule.
type MonitorConfig struct {
	// Window is the length of one observation window.
	Window time.Duration
	// MinSamples is how many responses a window needs before its error rate
	// counts; fewer is low traffic.
	MinSamples int64
	// ErrorRateThreshold and MinDelta are the 5xx rate and its rise over the
	// baseline that both must be exceeded for a window to breach.
	ErrorRateThreshold float64
	MinDelta           float64
	// Consecutive is how many breaching windows in a row roll back.
	Consecutive int
}

// LowTrafficConfig is the synthetic hold of a release whose real traffic
// does not reach MinSamples in a window.
type LowTrafficConfig struct {
	MinHold time.Duration
	// MaxExtensions bounds how often an error of a real request extends the
	// hold by one window before the release is restored instead.
	MaxExtensions int
}

// Config is what a pipeline is built from.
type Config struct {
	FactoryID core.FactoryID
	// State is the operational store, opened for the control zone.
	State   state.Store
	Backend deploy.Backend
	// Verifier trusts the delegated keys that sign QA receipts and deploy
	// artifacts.
	Verifier core.Verifier
	LKG      *LKGBook
	Freeze   FreezeCheck
	// Publisher writes the changelog and the catalog before an issue closes.
	Publisher Publisher
	// Lander is optional; with it a verified release's change is landed on
	// the default branch before the issue closes.
	Lander Lander
	// Synthetic is optional; without it a low-traffic release holds.
	Synthetic SyntheticSuite
	// Approvals and Policy are optional; without them OwnerRollback is
	// NOT_SUPPORTED.
	Approvals ApprovalSpender
	Policy    PolicyInForce
	// Audit is optional; it records each release decision.
	Audit core.AuditLog
	// Budget is optional; with it every rollback reserves through the
	// rollback reserve and records its spend. A refused reservation never
	// blocks the rollback.
	Budget RollbackBudget
	// RollbackCost is what a rollback is reserved and recorded at;
	// DefaultRollbackCost when zero.
	RollbackCost core.Micros
	// RollbackBudgetWait bounds each budget call around a rollback;
	// DefaultRollbackBudgetWait when zero.
	RollbackBudgetWait time.Duration
	Clock              core.Clock

	LeaseOwner string
	LeaseTTL   time.Duration
	PassScore  int
	ExtraGates []string
	// Protected lists the paths of the trusted control surface.
	Protected          []string
	Monitor            MonitorConfig
	CanarySteps        []int
	StepWindows        int
	ObservationWindows int
	LowTraffic         LowTrafficConfig
}

// withDefaults checks the required dependencies and fills in the defaults.
func (c Config) withDefaults() (Config, error) {
	if err := c.FactoryID.Validate(); err != nil {
		return c, err
	}
	switch {
	case c.State == nil || c.Backend == nil || c.Verifier == nil || c.LKG == nil:
		return c, invalid("a release pipeline needs a state store, a deployment backend, a verifier and an LKG book")
	case c.Freeze == nil || c.Publisher == nil:
		return c, invalid("a release pipeline needs the emergency stop and a publisher")
	}
	if c.Clock == nil {
		c.Clock = core.SystemClock{}
	}
	if c.RollbackCost == 0 {
		c.RollbackCost = DefaultRollbackCost
	}
	if c.RollbackBudgetWait == 0 {
		c.RollbackBudgetWait = DefaultRollbackBudgetWait
	}
	if c.LeaseOwner == "" {
		c.LeaseOwner = DefaultLeaseOwner
	}
	if c.PassScore == 0 {
		c.PassScore = DefaultPassScore
	}
	if c.Protected == nil {
		c.Protected = DefaultProtectedPaths()
	}
	c.Monitor = c.Monitor.withDefaults()
	if c.LeaseTTL == 0 {
		c.LeaseTTL = 3 * c.Monitor.Window
	}
	if c.CanarySteps == nil {
		c.CanarySteps = DefaultCanarySteps()
	}
	if c.StepWindows == 0 {
		c.StepWindows = DefaultStepWindows
	}
	if c.ObservationWindows == 0 {
		c.ObservationWindows = DefaultObservationWindows
	}
	if c.LowTraffic.MinHold == 0 {
		c.LowTraffic.MinHold = DefaultLowTrafficMinHold
	}
	if c.LowTraffic.MaxExtensions == 0 {
		c.LowTraffic.MaxExtensions = DefaultMaxHoldExtensions
	}
	return c, c.validate()
}

// withDefaults fills the unset parameters of the rollback rule.
func (m MonitorConfig) withDefaults() MonitorConfig {
	if m.Window == 0 {
		m.Window = DefaultWindow
	}
	if m.MinSamples == 0 {
		m.MinSamples = DefaultMinSamples
	}
	if m.ErrorRateThreshold == 0 {
		m.ErrorRateThreshold = DefaultErrorRateThreshold
	}
	if m.MinDelta == 0 {
		m.MinDelta = DefaultMinDelta
	}
	if m.Consecutive == 0 {
		m.Consecutive = DefaultConsecutive
	}
	return m
}

// validate rejects parameters that would weaken the release rules.
func (c Config) validate() error {
	m := c.Monitor
	switch {
	case m.Window <= 0 || m.MinSamples < 1 || m.Consecutive < 1:
		return invalid("the monitor needs a positive window, at least one sample and one window to roll back on")
	case m.ErrorRateThreshold <= 0 || m.ErrorRateThreshold >= 1 || m.MinDelta <= 0 || m.MinDelta >= 1:
		return invalid("the error rate threshold and its rise over the baseline must lie between 0 and 1")
	case c.LeaseTTL <= m.Window:
		return invalid("the issue lease must outlast one observation window")
	case c.PassScore < 1 || c.PassScore > 100:
		return invalid("the pass score must be within 1-100")
	case c.StepWindows < 1 || c.ObservationWindows < 1:
		return invalid("each traffic step and the observation need at least one window")
	case c.RollbackCost < 0:
		return invalid("the cost of a rollback cannot be negative")
	case c.RollbackBudgetWait < 0:
		return invalid("the wait for the budget around a rollback cannot be negative")
	case c.LowTraffic.MinHold <= 0 || c.LowTraffic.MaxExtensions < 0:
		return invalid("the low-traffic hold must be positive")
	case len(c.Protected) == 0:
		return invalid("the trusted control surface cannot be empty")
	}
	if len(c.CanarySteps) == 0 || c.CanarySteps[len(c.CanarySteps)-1] != 100 {
		return invalid("the canary steps must end at 100 percent")
	}
	for i, pct := range c.CanarySteps {
		if pct < 1 || pct > 100 || (i > 0 && pct <= c.CanarySteps[i-1]) {
			return invalid("the canary steps must rise within 1-100 percent")
		}
	}
	return nil
}

// invalid builds the INVALID_ARGUMENT error of this package.
func invalid(format string, args ...any) *core.Error {
	return core.NewError(core.CodeInvalidArgument, fmt.Sprintf(format, args...))
}

// holdf builds a RELEASE_HOLD refusal: the release waits for a person.
func holdf(format string, args ...any) *core.Error {
	return core.NewError(core.CodeReleaseHold, fmt.Sprintf(format, args...))
}
