package rbac

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/identity"
)

// GitHubAccounts maps a human actor to the GitHub login it signed in with.
// ok is false for an actor that never signed in with GitHub.
type GitHubAccounts interface {
	GitHubLogin(ctx context.Context, actorID string) (login string, ok bool, err error)
}

// SessionChecker reports whether a session or token handle is still live.
type SessionChecker interface {
	Active(ctx context.Context, handle string) bool
}

// InstallationLookup resolves the factory's app installation on a
// repository. identity.InstallationProvider satisfies it.
type InstallationLookup interface {
	Installation(ctx context.Context, owner, repo string) (identity.Installation, error)
}

// Config configures an Authorizer.
type Config struct {
	Grants        *Grants
	Installations InstallationLookup
	Sessions      SessionChecker
	Clock         core.Clock
	// DefaultRepo, "owner/name", is the factory's repository: the target of
	// every action whose resource does not name a repository.
	DefaultRepo string
	// Membership and Accounts add GitHub permission to local grants. Both
	// are set, or neither.
	Membership MembershipSource
	Accounts   GitHubAccounts
	// StepUpMaxAge defaults to the approval service's value.
	StepUpMaxAge time.Duration
}

// Authorizer implements core.Authorizer.
type Authorizer struct {
	cfg                    Config
	defaultOwner, defaultR string
}

var _ core.Authorizer = (*Authorizer)(nil)

// New validates cfg and returns an authorizer.
func New(cfg Config) (*Authorizer, error) {
	owner, repo, ok := splitRepo(cfg.DefaultRepo)
	switch {
	case cfg.Grants == nil || cfg.Installations == nil || cfg.Sessions == nil || cfg.Clock == nil:
		return nil, core.NewError(core.CodeInvalidArgument, "the authorizer needs grants, installations, sessions and a clock")
	case !ok:
		return nil, core.NewError(core.CodeInvalidArgument, "the default repository must be owner/name")
	case (cfg.Membership == nil) != (cfg.Accounts == nil):
		return nil, core.NewError(core.CodeInvalidArgument, "GitHub membership needs both a membership source and an account map")
	case cfg.StepUpMaxAge < 0:
		return nil, core.NewError(core.CodeInvalidArgument, "the step-up age must not be negative")
	}
	if cfg.StepUpMaxAge == 0 {
		cfg.StepUpMaxAge = approvals.DefaultStepUpMaxAge
	}
	return &Authorizer{cfg: cfg, defaultOwner: owner, defaultR: repo}, nil
}

func deny(reason string) core.AuthzDecision {
	return core.AuthzDecision{Effect: core.EffectDeny, Reason: reason}
}

// Authorize decides now, from the actor's current grants and GitHub
// permission on the target repository. The roles carried in req.Actor are
// ignored. An error comes with a denial and means no decision could be made.
func (a *Authorizer) Authorize(ctx context.Context, req core.AuthzRequest) (core.AuthzDecision, error) {
	actor, spec := req.Actor, req.Action
	if err := spec.Validate(); err != nil {
		return deny("the action is not a valid action"), err
	}
	if actor.ID == "" || !actor.Kind.Valid() {
		return deny("no authenticated actor"), nil
	}
	if actor.SessionID == "" || !a.cfg.Sessions.Active(ctx, actor.SessionID) {
		return deny("the session or token is expired or revoked"), nil
	}
	owner, repo, err := a.target(req.Resource)
	if err != nil {
		return deny(err.Error()), nil
	}
	inst, err := a.cfg.Installations.Installation(ctx, owner, repo)
	switch {
	case core.HasCode(err, core.CodeNotFound):
		return deny("the factory is not installed on " + owner + "/" + repo), nil
	case err != nil:
		return deny("the installation could not be checked"), err
	case !inst.Covers(owner, repo):
		return deny("the factory's installation does not cover " + owner + "/" + repo), nil
	}
	roles, err := a.Roles(ctx, actor, owner, repo)
	if err != nil {
		return deny("the actor's roles could not be resolved"), err
	}
	if !slices.ContainsFunc(spec.Roles, func(r core.Role) bool { return slices.Contains(roles, r) }) {
		return deny("the actor holds none of the roles " + joinRoles(spec.Roles) + " on " + owner + "/" + repo), nil
	}
	if len(actor.Scopes) > 0 {
		if len(spec.Scopes) == 0 {
			return deny("a token may run only actions within its scopes; " + spec.Name + " has none"), nil
		}
		for _, sc := range spec.Scopes {
			if !slices.Contains(actor.Scopes, sc) {
				return deny("the token lacks scope " + sc), nil
			}
		}
	}
	if spec.StepUp {
		if actor.Kind != core.ActorHuman {
			return deny("only a person can confirm their identity for " + spec.Name), nil
		}
		now := a.cfg.Clock.Now()
		if at := actor.StepUpAt; at.IsZero() || at.After(now) || now.Sub(at) > a.cfg.StepUpMaxAge {
			return core.AuthzDecision{Effect: core.EffectRequireStepUp, Reason: "confirm your identity again"}, nil
		}
	}
	return core.AuthzDecision{Effect: core.EffectAllow}, nil
}

// Roles returns the actor's effective roles on owner/repo right now: local
// grants for its kind, plus, for a person who signed in with GitHub, the
// role their GitHub permission on that repository maps to.
func (a *Authorizer) Roles(ctx context.Context, actor core.Actor, owner, repo string) ([]core.Role, error) {
	roles := a.cfg.Grants.Roles(actor.ID, actor.Kind)
	if actor.Kind != core.ActorHuman || a.cfg.Membership == nil {
		return roles, nil
	}
	login, ok, err := a.cfg.Accounts.GitHubLogin(ctx, actor.ID)
	if err != nil || !ok {
		return roles, err
	}
	perm, err := a.cfg.Membership.RepoPermission(ctx, owner, repo, login)
	if err != nil {
		return nil, err
	}
	if role, ok := RoleForPermission(perm); ok && !slices.Contains(roles, role) {
		roles = append(roles, role)
	}
	return roles, nil
}

// target returns the repository a resource belongs to. An issue reference
// "owner/repo#n" or "repo:owner/name" names one; any other resource belongs
// to the factory's own repository.
func (a *Authorizer) target(resource string) (owner, repo string, err error) {
	if rest, ok := strings.CutPrefix(resource, "repo:"); ok {
		if owner, repo, ok := splitRepo(rest); ok {
			return owner, repo, nil
		}
		return "", "", core.NewError(core.CodeInvalidArgument, "resource "+resource+" is not repo:owner/name")
	}
	if strings.Contains(resource, "#") {
		ref, err := core.ParseIssueRef(resource)
		if err != nil {
			return "", "", err
		}
		return ref.Owner, ref.Repo, nil
	}
	return a.defaultOwner, a.defaultR, nil
}

func splitRepo(s string) (owner, repo string, ok bool) {
	owner, repo, ok = strings.Cut(s, "/")
	return owner, repo, ok && owner != "" && repo != "" && !strings.ContainsAny(repo, "/#")
}

func joinRoles(roles []core.Role) string {
	parts := make([]string, len(roles))
	for i, r := range roles {
		parts[i] = string(r)
	}
	return strings.Join(parts, ", ")
}
