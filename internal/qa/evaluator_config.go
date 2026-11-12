// Package qa is the independent evaluator. It judges a built candidate from a
// read-only baseline locked to the candidate's baseline commit and policy,
// against the rubric of the review criteria at the pinned commit, runs every
// hard gate, and issues a qa_receipt signed by the trusted supervisor's
// delegated key, bound to the candidate digest and expiring.
//
// The evaluator never holds a private key itself: the supervisor process
// injects its core.Signer. It refuses to judge a candidate written by its own
// identity, to work in the coder's workspace, to share the coder's model
// route when another exists, and to score code that has not finished
// building. A failed hard gate fails the candidate whatever its score.
package qa

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/internal/policy"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/qa"
)

// Receipt lifetimes.
const (
	// DefaultReceiptTTL is how long a receipt authorizes a release when the
	// configuration states nothing.
	DefaultReceiptTTL = 24 * time.Hour
	// MaxReceiptTTL bounds the configured lifetime.
	MaxReceiptTTL = 7 * 24 * time.Hour
)

// BaselineSource provides the evaluator's read-only baseline for a candidate.
type BaselineSource interface {
	// Baseline returns the baseline locked for the candidate. It is asked
	// again after scoring, and must return the same baseline.
	Baseline(ctx context.Context, c qa.Candidate) (qa.Baseline, error)
}

// BuildLedger tells which build of a commit has finished. The candidate's
// own claim of a digest is not evidence that it was built.
type BuildLedger interface {
	// FinishedBuild returns the digest of the finished build of commit, or
	// NOT_FOUND while there is none.
	FinishedBuild(ctx context.Context, commit core.CommitSHA) (core.Digest, error)
}

// Config wires an evaluator. Every field is required unless marked optional.
type Config struct {
	FactoryID core.FactoryID
	// Identity is the evaluator's agent identity.
	Identity string
	// Workspace names the evaluator's own workspace.
	Workspace string
	// Gates are the hard gates other than qa.GateIndependentEvaluator, which
	// the evaluator decides itself; each must be given exactly once.
	Gates     []qa.HardGate
	Judge     qa.Judge
	Baselines BaselineSource
	Builds    BuildLedger
	// Review reads the review criteria at the pinned commit.
	Review policy.Source
	// Routes are the model routes of the deployment, in order of preference.
	Routes []core.ModelRoute
	// SameRouteAcknowledged is the Owner's acknowledgement that the
	// deployment has a single model route shared by coder and QA.
	SameRouteAcknowledged bool
	// SameRouteAction is optional; SameRouteOwnerApproval when empty.
	SameRouteAction SameRouteAction
	// Signer signs receipts: the supervisor's delegated qa_receipt key.
	Signer core.Signer
	// Verifier checks each receipt after signing, so a key that is revoked,
	// expired or not trusted never yields a receipt.
	Verifier core.Verifier
	// ReceiptTTL is optional; DefaultReceiptTTL when zero.
	ReceiptTTL time.Duration
	Clock      core.Clock
}

// Evaluator implements qa.Evaluator. It is safe for concurrent use when its
// dependencies are.
type Evaluator struct {
	cfg   Config
	gates map[qa.GateName]qa.HardGate
}

var _ qa.Evaluator = (*Evaluator)(nil)

// New checks the configuration and returns the evaluator.
func New(cfg Config) (*Evaluator, error) {
	if err := cfg.FactoryID.Validate(); err != nil {
		return nil, err
	}
	switch {
	case strings.TrimSpace(cfg.Identity) == "" || strings.TrimSpace(cfg.Workspace) == "":
		return nil, invalid("the evaluator needs an identity and a workspace of its own")
	case cfg.Judge == nil || cfg.Baselines == nil || cfg.Builds == nil || cfg.Review == nil:
		return nil, invalid("the evaluator needs a judge, a baseline source, a build ledger and the review criteria")
	case cfg.Signer == nil || cfg.Verifier == nil || cfg.Clock == nil:
		return nil, invalid("the evaluator needs a signer, a verifier and a clock")
	case cfg.ReceiptTTL < 0 || cfg.ReceiptTTL > MaxReceiptTTL:
		return nil, invalid("the receipt lifetime must be within 0-%s", MaxReceiptTTL)
	}
	if cfg.ReceiptTTL == 0 {
		cfg.ReceiptTTL = DefaultReceiptTTL
	}
	if cfg.SameRouteAction == "" {
		cfg.SameRouteAction = SameRouteOwnerApproval
	}
	if !cfg.SameRouteAction.Valid() {
		return nil, invalid("unknown same-route action %q", cfg.SameRouteAction)
	}
	for _, route := range cfg.Routes {
		if err := route.Validate(); err != nil {
			return nil, err
		}
	}
	gates, err := indexGates(cfg.Gates)
	if err != nil {
		return nil, err
	}
	cfg.Routes, cfg.Gates = slices.Clone(cfg.Routes), nil
	return &Evaluator{cfg: cfg, gates: gates}, nil
}

// indexGates requires every hard gate but the evaluator's own, once each.
func indexGates(given []qa.HardGate) (map[qa.GateName]qa.HardGate, error) {
	gates := map[qa.GateName]qa.HardGate{}
	for _, gate := range given {
		if gate == nil {
			return nil, invalid("a hard gate is nil")
		}
		name := gate.Name()
		switch {
		case name == qa.GateIndependentEvaluator:
			return nil, invalid("the evaluator decides the %s gate itself", name)
		case !slices.Contains(qa.Gates(), name):
			return nil, invalid("%q is not a hard gate", name)
		case gates[name] != nil:
			return nil, invalid("hard gate %s is given twice", name)
		}
		gates[name] = gate
	}
	for _, name := range qa.Gates() {
		if name != qa.GateIndependentEvaluator && gates[name] == nil {
			return nil, invalid("hard gate %s is not wired", name)
		}
	}
	return gates, nil
}

// sprintf is fmt.Sprintf, named once so the error helpers stay short.
func sprintf(format string, args ...any) string { return fmt.Sprintf(format, args...) }

// invalid builds the INVALID_ARGUMENT error of this package.
func invalid(format string, args ...any) *core.Error {
	return core.NewError(core.CodeInvalidArgument, sprintf(format, args...))
}
