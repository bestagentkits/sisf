package wiring

import (
	"context"
	"io/fs"
	"time"

	"github.com/bestagentkits/sisf/internal/actions"
	"github.com/bestagentkits/sisf/internal/actions/actionsets"
	"github.com/bestagentkits/sisf/internal/actions/operationsactions"
	"github.com/bestagentkits/sisf/internal/actions/runactions"
	"github.com/bestagentkits/sisf/internal/budget"
	"github.com/bestagentkits/sisf/internal/catalog"
	"github.com/bestagentkits/sisf/internal/control"
	"github.com/bestagentkits/sisf/internal/control/web"
	"github.com/bestagentkits/sisf/internal/dispatcher"
	"github.com/bestagentkits/sisf/internal/mcp"
	"github.com/bestagentkits/sisf/internal/oauthas"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/identity"
	"github.com/bestagentkits/sisf/pkg/secrets"
	"github.com/bestagentkits/sisf/pkg/telemetry"
)

// SurfaceConfig is what the people- and agent-facing surfaces of the
// control zone need beyond the zone itself.
type SurfaceConfig struct {
	Identity IdentityConfig
	// GitHubToken is the control zone's own GitHub token; people's
	// repository permission is read with it.
	GitHubToken core.SecretValue
	// Supervisor is the release supervisor's admin socket, which carries out
	// an emergency stop and an Owner rollback; control never deploys.
	Supervisor operationsactions.Supervisor
	// Telemetry is what telemetry.search reads.
	Telemetry telemetry.Reader
	// Docs is the docs tree docs.list lists, rooted at the docs directory;
	// nil lists nothing.
	Docs fs.FS
	// SecretProviders list secret references for secrets.list_refs.
	SecretProviders []secrets.SecretProvider
	// Version is reported to MCP clients.
	Version string
}

// docsRoot is the directory of the docs tree docs.list lists, and
// catalogBaseBranch the branch the Publisher's pull requests target, as the
// supervisor's Publisher does.
const (
	docsRoot          = "."
	catalogBaseBranch = "main"
)

// publisherActor is the service identity the catalog's pull requests are
// opened as; it is the supervisor's Publisher identity.
func publisherActor() core.Actor {
	return core.Actor{Kind: core.ActorService, ID: "service:publisher", Roles: []core.Role{core.RolePublisher}}
}

// newSurfaces builds the one action registry and service and every surface
// over them.
func (c *Control) newSurfaces(ctx context.Context, sc SurfaceConfig, repo github.RepoRef, installation identity.Installation) (control.Surfaces, error) {
	cfg := c.cfg
	specs, err := actionsets.Specs()
	if err != nil {
		return control.Surfaces{}, err
	}
	signIn, err := newSignIn(ctx, sc.Identity, oauthas.ScopesOf(specs), cfg.Clock)
	if err != nil {
		return control.Surfaces{}, err
	}
	authz, err := newAuthorizer(sc.Identity, signIn.service, repo, installation, sc.GitHubToken, cfg.Clock)
	if err != nil {
		return control.Surfaces{}, err
	}
	reg, err := c.registry(sc, repo)
	if err != nil {
		return control.Surfaces{}, err
	}
	roles := func(ctx context.Context, actor core.Actor) ([]core.Role, error) {
		return authz.Roles(ctx, actor, repo.Owner, repo.Name)
	}
	svc, err := actions.NewService(actions.Config{FactoryID: cfg.Manifest.FactoryID, Registry: reg, Authorizer: authz,
		Approvals: c.Approvals, Policy: actions.PolicyRevisionFunc(c.policyRevision), Audit: c.Audit, Clock: cfg.Clock,
		Roles: actions.RoleResolverFunc(roles), StepUpPage: web.PathApprovals, StepUp: signIn.service,
		Idempotency: actions.NewKVIdempotency(cfg.Store)})
	if err != nil {
		return control.Surfaces{}, err
	}
	c.Actions = svc
	oauth, err := oauthas.New(oauthas.Config{Issuer: sc.Identity.ExternalURL, Resource: sc.Identity.ExternalURL + mcp.Path,
		ResourceName: "SISF factory " + string(cfg.Manifest.FactoryID), LoginPath: web.PathSignIn, Sessions: signIn.service,
		Scopes: oauthas.RoleScopes{Roles: roles, Specs: svc.Specs}, ScopesSupported: oauthas.ScopesOf(svc.Specs()),
		DynamicRegistration: true, Clock: cfg.Clock})
	if err != nil {
		return control.Surfaces{}, err
	}
	c.Identity, c.Authorizer = signIn.service, authz
	return control.Surfaces{Identity: signIn.service, Actions: svc, OAuth: oauth, LoginProviders: signIn.providers,
		FormOrigins: signIn.formOrigins, ExternalURL: sc.Identity.ExternalURL, Version: sc.Version}, nil
}

// registry registers every action set over the control zone's state.
func (c *Control) registry(sc SurfaceConfig, repo github.RepoRef) (*actions.Registry, error) {
	cfg := c.cfg
	publisher, err := catalog.New(catalog.Config{Repo: repo, BaseBranch: catalogBaseBranch, Actor: publisherActor(),
		Files: cfg.GitHub, Branches: catalogBranches{cfg.GitHub}, Pulls: cfg.GitHub})
	if err != nil {
		return nil, err
	}
	docs := sc.Docs
	if docs == nil {
		docs = noDocs{}
	}
	reg := actions.NewRegistry()
	err = actionsets.Register(reg, actionsets.Deps{
		FactoryID: cfg.Manifest.FactoryID,
		Runs: runactions.Deps{FactoryID: cfg.Manifest.FactoryID, Records: runactions.StoreRecords(cfg.Store), QAHolds: storeQAHolds{store: cfg.Store}, CandidatePRs: storeCandidatePRs{store: cfg.Store},
			Runs: dispatcher.StoredRunRecords{State: cfg.Store}, Approvals: c.Approvals, Clock: cfg.Clock, GitHub: c.GitHubSync},
		Approvals: c.Approvals, Budget: budget.ActionsConfig{Store: budget.StateStore(cfg.Store), Clock: cfg.Clock},
		SecretProviders: sc.SecretProviders, Catalog: catalog.ActionsConfig{Publisher: publisher, Docs: docs, DocsRoot: docsRoot},
		Telemetry: sc.Telemetry, Repo: repo, Issues: cfg.GitHub, Supervisor: sc.Supervisor,
	})
	return reg, err
}

// policyRevision is the revision of the policy in force, which an approval
// request is bound to.
func (c *Control) policyRevision(ctx context.Context) (core.CommitSHA, error) {
	baseline, err := c.Baselines.Current(ctx)
	if err != nil {
		return "", err
	}
	return baseline.Revision, nil
}

// catalogBranches adapts the GitHub client to the catalog's branch writer.
type catalogBranches struct{ gh GitHub }

// Head returns where branch points.
func (b catalogBranches) Head(ctx context.Context, repo github.RepoRef, branch string) (core.CommitSHA, error) {
	return b.gh.BranchHead(ctx, repo, branch)
}

// Commit commits c on its branch, which starts at c.Base.
func (b catalogBranches) Commit(ctx context.Context, repo github.RepoRef, c catalog.Commit) (core.CommitSHA, error) {
	return b.gh.CommitToBranch(ctx, repo, github.BranchCommit{Branch: c.Branch, Base: c.Base, Message: c.Message, Author: c.Author, Files: c.Files})
}

// noDocs is an empty docs tree.
type noDocs struct{}

// Open finds nothing but the empty root.
func (noDocs) Open(name string) (fs.File, error) {
	if name == docsRoot {
		return emptyDir{}, nil
	}
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}

// emptyDir is a directory with no entries.
type emptyDir struct{}

func (emptyDir) Stat() (fs.FileInfo, error) { return emptyDirInfo{}, nil }
func (emptyDir) Read([]byte) (int, error) {
	return 0, &fs.PathError{Op: "read", Path: docsRoot, Err: fs.ErrInvalid}
}
func (emptyDir) Close() error                       { return nil }
func (emptyDir) ReadDir(int) ([]fs.DirEntry, error) { return nil, nil }

type emptyDirInfo struct{}

func (emptyDirInfo) Name() string       { return docsRoot }
func (emptyDirInfo) Size() int64        { return 0 }
func (emptyDirInfo) Mode() fs.FileMode  { return fs.ModeDir | 0o555 }
func (emptyDirInfo) ModTime() time.Time { return time.Time{} }
func (emptyDirInfo) IsDir() bool        { return true }
func (emptyDirInfo) Sys() any           { return nil }
