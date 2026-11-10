// Package prgate decides how far a pull request may go, following the
// access rules of the GitHub repository.
//
// In a public repository a pull request from a fork or from someone without
// write access is untrusted input: it runs in a sandbox with no secrets and
// no write access to the repository, and enters the pipeline only after a
// Maintainer accepts the exact revision. A private repository accepts pull
// requests only from collaborators and the factory's own agents; a fork is
// sandboxed there too until accepted. What a pull request says about itself
// decides nothing.
package prgate

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/execution"
	"github.com/bestagentkits/sisf/pkg/github"
)

// Visibility is the GitHub visibility of the repository.
type Visibility string

const (
	VisibilityPublic  Visibility = "public"
	VisibilityPrivate Visibility = "private"
)

// Stage is how far a pull request may go.
type Stage string

const (
	// StageRejected: the pull request is not processed at all.
	StageRejected Stage = "rejected"
	// StageSandboxOnly: checks may run in a sandbox without secrets or
	// repository write; it does not enter the pipeline.
	StageSandboxOnly Stage = "sandbox_only"
	// StagePipeline: the pull request enters the factory's pipeline.
	StagePipeline Stage = "pipeline"
)

// Decision is what the gate allows for one revision of a pull request.
type Decision struct {
	Stage           Stage `json:"stage"`
	AllowSecrets    bool  `json:"allow_secrets"`
	AllowRepoWrite  bool  `json:"allow_repo_write"`
	NeedsAcceptance bool  `json:"needs_acceptance"`
	// UntrustedCode marks code from outside the repository's collaborators.
	UntrustedCode bool           `json:"untrusted_code"`
	HeadSHA       core.CommitSHA `json:"head_sha"`
	Reason        string         `json:"reason"`
}

// ApplyTo narrows a sandbox spec to what the decision allows: no sandbox
// for a rejected pull request, no secret grants without AllowSecrets, and
// untrusted trust for code from outside.
func (d Decision) ApplyTo(spec *execution.SandboxSpec) error {
	if d.Stage != StageSandboxOnly && d.Stage != StagePipeline {
		return core.NewError(core.CodePermissionDenied, "this pull request may not run: "+d.Reason)
	}
	if !d.AllowSecrets {
		spec.SecretGrantIDs = nil
	}
	if d.UntrustedCode || !d.AllowSecrets {
		spec.Trust = execution.TrustUntrusted
	}
	return nil
}

// Config configures the gate.
type Config struct {
	Visibility Visibility
	// AgentLogins are the GitHub bot accounts of the factory's own agents.
	AgentLogins []string
	// Acceptances keeps Maintainer acceptances. Nil means in-process.
	Acceptances AcceptanceStore
	// Audit records every acceptance and refused acceptance. Nil skips it.
	Audit core.AuditLog
	// Clock stamps acceptances. Nil means the wall clock.
	Clock core.Clock
}

// Gate applies the rules. It is safe for concurrent use.
type Gate struct{ cfg Config }

// New checks the visibility; an unknown one fails closed.
func New(cfg Config) (*Gate, error) {
	if cfg.Visibility != VisibilityPublic && cfg.Visibility != VisibilityPrivate {
		return nil, core.NewError(core.CodeInvalidArgument, "repository visibility must be public or private")
	}
	if cfg.Acceptances == nil {
		cfg.Acceptances = NewMemoryAcceptances()
	}
	if cfg.Clock == nil {
		cfg.Clock = core.SystemClock{}
	}
	return &Gate{cfg: cfg}, nil
}

// Evaluate decides for the pull request's current head revision.
func (g *Gate) Evaluate(ctx context.Context, pr github.PullRequest) (Decision, error) {
	if err := validPR(pr); err != nil {
		return Decision{}, err
	}
	d := g.classify(pr)
	if !d.NeedsAcceptance {
		return d, nil
	}
	a, ok, err := g.cfg.Acceptances.Get(ctx, pr.Repo, pr.Number)
	if err != nil {
		return Decision{}, core.NewError(core.CodeUnavailable, "pull request acceptances could not be read").WithCause(err)
	}
	if ok && a.HeadSHA == pr.HeadSHA {
		d.Stage, d.AllowSecrets, d.NeedsAcceptance = StagePipeline, true, false
		d.Reason = "accepted by a Maintainer at this revision"
	}
	return d, nil
}

// classify applies the repository rules before any acceptance.
func (g *Gate) classify(pr github.PullRequest) Decision {
	member := pr.AuthorAssociation.IsCollaborator()
	agent := pr.Author.Bot && slices.ContainsFunc(g.cfg.AgentLogins, func(l string) bool {
		return strings.EqualFold(l, pr.Author.Login)
	})
	d := Decision{HeadSHA: pr.HeadSHA}
	switch {
	case g.cfg.Visibility == VisibilityPrivate && !member && !agent:
		d.Stage, d.Reason = StageRejected, "a private repository accepts pull requests only from collaborators and the factory's agents"
	case pr.IsFork || (!member && !agent):
		d.Stage, d.NeedsAcceptance, d.UntrustedCode = StageSandboxOnly, true, true
		d.Reason = "untrusted until a Maintainer accepts this revision"
	default:
		d.Stage, d.AllowSecrets, d.AllowRepoWrite = StagePipeline, true, true
		d.Reason = "from a collaborator or the factory's agent"
	}
	return d
}

// Accept lets the pull request's current head into the pipeline. Only a
// human Maintainer or Owner may accept, and only a pull request the
// repository rules allow to wait for acceptance.
func (g *Gate) Accept(ctx context.Context, actor core.Actor, pr github.PullRequest) error {
	if err := validPR(pr); err != nil {
		return err
	}
	resource := fmt.Sprintf("%s#%d", pr.Repo, pr.Number)
	denied := func(message string) error {
		err := core.NewError(core.CodePermissionDenied, message)
		if aerr := g.audit(ctx, actor, resource, pr.HeadSHA, core.OutcomeDenied); aerr != nil {
			return aerr
		}
		return err
	}
	if actor.Kind != core.ActorHuman || (!actor.HasRole(core.RoleMaintainer) && !actor.HasRole(core.RoleOwner)) {
		return denied("only a human Maintainer or Owner may accept an outside pull request")
	}
	if d := g.classify(pr); d.Stage == StageRejected {
		return denied(d.Reason)
	}
	// The authorization is audited before it takes effect, so an audit
	// failure leaves the pull request unaccepted.
	if err := g.audit(ctx, actor, resource, pr.HeadSHA, core.OutcomeAllowed); err != nil {
		return err
	}
	a := Acceptance{Repo: pr.Repo, Number: pr.Number, HeadSHA: pr.HeadSHA, ActorID: actor.ID, At: g.cfg.Clock.Now()}
	if err := g.cfg.Acceptances.Put(ctx, a); err != nil {
		return core.NewError(core.CodeUnavailable, "the acceptance could not be recorded").WithCause(err)
	}
	return g.audit(ctx, actor, resource, pr.HeadSHA, core.OutcomeExecuted)
}

func (g *Gate) audit(ctx context.Context, actor core.Actor, resource string, head core.CommitSHA, outcome core.AuditOutcome) error {
	if g.cfg.Audit == nil {
		return nil
	}
	_, err := g.cfg.Audit.Append(ctx, core.AuditEvent{
		At: g.cfg.Clock.Now(), ActorID: actor.ID, ActorKind: actor.Kind, Surface: actor.Surface,
		Action: "pull_request.accept", Resource: resource, Outcome: outcome,
		Details: map[string]string{"head_sha": string(head)},
	})
	if err != nil {
		return core.NewError(core.CodeUnavailable, "the acceptance could not be audited").WithCause(err)
	}
	return nil
}

func validPR(pr github.PullRequest) error {
	if pr.Repo.Owner == "" || pr.Repo.Name == "" || pr.Number < 1 {
		return core.NewError(core.CodeInvalidArgument, "a pull request needs a repository and a positive number")
	}
	return pr.HeadSHA.Validate()
}
