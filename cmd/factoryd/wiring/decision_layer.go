package wiring

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/bestagentkits/sisf/internal/decision"
	"github.com/bestagentkits/sisf/internal/decision/systemone"
	"github.com/bestagentkits/sisf/internal/policy"
	"github.com/bestagentkits/sisf/internal/redact"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/secrets"
	"github.com/bestagentkits/sisf/pkg/state"
)

// The edge the decision layer reads its counters from. The token is the one
// the edge's admin handler checks; the supervisor reads the same variable.
const (
	EnvEdgeAdminURL   = "SISF_EDGE_ADMIN_URL"
	EnvEdgeAdminToken = "EDGE_ADMIN_TOKEN"
)

// DecisionInterval is how often the control zone reads the edge's counters.
// Each read closes one window of the decision rules.
const DecisionInterval = 5 * time.Minute

// DecisionConfig turns the decision layer on in the control zone. Whether
// System One is consulted is the approved manifest's decision.system_one
// section; without it the deterministic rules decide alone.
type DecisionConfig struct {
	Counters decision.CounterSource
	// Secrets resolves a System One credential; nil when none is configured.
	Secrets secrets.SecretProvider
	// HTTP reaches the System One backend; nil means its default.
	HTTP *http.Client
}

// decisionRunner runs the decision layer every DecisionInterval under the
// policy in force, and builds it again when that policy changes: the rules,
// the typed questions and the System One section all come from it.
type decisionRunner struct {
	cfg    DecisionConfig
	policy *policy.Loader
	issues IssueCreator
	repo   github.RepoRef
	store  state.Store
	clock  core.Clock
	log    *slog.Logger

	mu       sync.Mutex
	next     time.Time
	revision core.CommitSHA
	layer    *decision.Layer
}

// runDecisions runs the decision layer when it is configured and due.
func (c *Control) runDecisions(ctx context.Context) error {
	if c.decisions == nil {
		return nil
	}
	return c.decisions.pass(ctx)
}

// pass runs the layer when its interval is over. Nothing runs before a
// policy is approved.
func (r *decisionRunner) pass(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.clock.Now()
	if now.Before(r.next) {
		return nil
	}
	r.next = now.Add(DecisionInterval)
	snapshot, err := r.policy.Current(ctx)
	switch {
	case core.HasCode(err, core.CodeNotFound):
		return nil
	case err != nil:
		return err
	}
	if r.layer == nil || snapshot.Revision != r.revision {
		layer, err := newDecisionLayer(snapshot, r.cfg, decision.Config{
			Source: r.cfg.Counters, Sink: issueProposals{issues: r.issues, repo: r.repo},
			Store: storedProposals{store: r.store}, Redactor: redact.New(redact.Config{}), Clock: r.clock,
		})
		if err != nil {
			return err
		}
		r.layer, r.revision = layer, snapshot.Revision
	}
	outcomes, err := r.layer.Run(ctx)
	for _, o := range outcomes {
		if o.NewProposal {
			r.log.Info("the decision layer proposed work", "fingerprint", o.Fingerprint, "verdict", string(o.Verdict))
		}
	}
	return err
}

// newDecisionLayer completes base with what snapshot approves: the policy
// references, the rules and, when the manifest declares one, the System One
// client and the typed questions of DECISIONS.md at the same revision.
func newDecisionLayer(snapshot *policy.Snapshot, cfg DecisionConfig, base decision.Config) (*decision.Layer, error) {
	refs, err := snapshot.Refs(core.StepIntake)
	if err != nil {
		return nil, err
	}
	base.Rules = decision.DefaultRules()
	if s, ok := snapshot.Manifest.Decision.SystemOneConfig(); ok {
		client, err := systemone.New(systemone.Config{
			Backend: systemone.Backend(s.Backend), Endpoint: s.Endpoint, Version: s.Version,
			Timeout: s.Timeout.Std(), Credential: s.Credential, Model: s.Model,
		}, systemone.Deps{Secrets: cfg.Secrets, HTTP: cfg.HTTP, Clock: base.Clock})
		if err != nil {
			return nil, err
		}
		questions, err := decision.ParseTypedQuestions([]byte(snapshot.Decisions.Content))
		if err != nil {
			return nil, err
		}
		questions.Revision = snapshot.Revision
		// A proposal records the route that judged it.
		refs.ModelRoute = s.Model
		base.SystemOne, base.Questions = client, questions
	}
	base.Policy = refs
	return decision.New(base)
}
