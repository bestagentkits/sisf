package stages

import (
	"context"
	"slices"
	"time"

	"github.com/bestagentkits/sisf/internal/catalog"
	"github.com/bestagentkits/sisf/internal/dispatcher"
	"github.com/bestagentkits/sisf/internal/policy"
	iqa "github.com/bestagentkits/sisf/internal/qa"
	"github.com/bestagentkits/sisf/internal/redact"
	"github.com/bestagentkits/sisf/internal/signing/delegation"
	"github.com/bestagentkits/sisf/internal/supervisor/release"
	"github.com/bestagentkits/sisf/internal/supplychain"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/qa"
	"github.com/bestagentkits/sisf/pkg/state"
)

// GitHub is what the stages use of the factory's repository: reading it at
// exact commits, and the Publisher's branch and pull request.
type GitHub interface {
	GitHubRepository
	BranchHead(ctx context.Context, repo github.RepoRef, branch string) (core.CommitSHA, error)
	CommitToBranch(ctx context.Context, repo github.RepoRef, bc github.BranchCommit) (core.CommitSHA, error)
	CommitExists(ctx context.Context, repo github.RepoRef, commit core.CommitSHA) error
	CreatePullRequest(ctx context.Context, repo github.RepoRef, pr github.NewPullRequest) (github.PullRequest, error)
	// The candidates' pull requests: found by branch, titled from the issue
	// and noted when their release is rolled back.
	FindPullRequests(ctx context.Context, repo github.RepoRef, branch string) ([]github.PullRequest, error)
	GetIssue(ctx context.Context, ref core.IssueRef) (github.Issue, error)
	ListComments(ctx context.Context, ref core.IssueRef, since time.Time) ([]github.Comment, error)
	CreateComment(ctx context.Context, ref core.IssueRef, body string) (github.Comment, error)
	// Viewer names the factory's own account: only a pull request it
	// opened is the candidate's.
	Viewer(ctx context.Context) (github.User, error)
}

// Config wires the supervisor's lifecycle stages. Keys are given only as
// signers: the supervisor process issued or loaded them, and nothing here
// ever sees key material.
type Config struct {
	FactoryID core.FactoryID
	Repo      github.RepoRef
	// BaseBranch is where the Publisher's pull requests go; "main" when empty.
	BaseBranch string
	// State is the operational store, opened for the control zone.
	State  state.Store
	GitHub GitHub
	Judge  qa.Judge
	// QAIdentity and QAWorkspace are the evaluator's own, never the coder's.
	QAIdentity  string
	QAWorkspace string
	Routes      []core.ModelRoute
	// SameRouteAcknowledged is the Owner's acknowledgement of one route.
	SameRouteAcknowledged bool
	QASigner              core.Signer
	ArtifactSigner        core.Signer
	// Delegation finds the root-signed delegation of one of the
	// supervisor's keys; the QA stage stores it with each receipt so the
	// receipt can be checked with the root public key alone. Nil stores
	// none.
	Delegation func(keyID string) (delegation.SignedDelegation, bool)
	// Verifier trusts the supervisor's delegated keys; ApprovalVerifier
	// trusts the approval service's.
	Verifier         core.Verifier
	ApprovalVerifier core.Verifier
	Redactor         *redact.Redactor
	MaxAttempts      int
	// Images builds candidate images; see SelectImageBuilder.
	Images ImageBuilder
	// BuildRetry bounds the retries of a build that met outages;
	// DefaultBuildRetry where zero.
	BuildRetry BuildRetry
	// Merger merges a verified candidate's pull request. With it every
	// candidate QA passes gets a pull request, merged once its release is
	// verified; nil opens none and merges nothing.
	Merger Merger
	// Self is the factory's own GitHub account, as the install names it: a
	// candidate's pull request is the one it opened. When zero it is asked
	// of GitHub, which only a token of a person's login can answer.
	Self github.Identity
	// Budget closes the reservation of a run QA passed or held; nil leaves
	// it held until the dispatcher stops the run.
	Budget dispatcher.ReservationCloser
	// Release carries the release parameters and dependencies the pipeline
	// is built from (Backend, LKG, Freeze, Synthetic, Approvals, Audit and
	// the tuning); the rest is filled in here.
	Release release.Config
	Clock   core.Clock
}

// Stages are the build, evaluation and release stages of one factory.
type Stages struct {
	Build   *BuildStage
	QA      *QAStage
	Release *ReleaseStage
	// CandidatePRs is nil when the stages were built without a merger.
	CandidatePRs *CandidatePRs
	Pipeline     *release.Pipeline
}

// New builds the stages.
func New(cfg Config) (*Stages, error) {
	if cfg.State == nil || cfg.GitHub == nil || cfg.Judge == nil || cfg.QASigner == nil || cfg.ArtifactSigner == nil ||
		cfg.Verifier == nil || cfg.ApprovalVerifier == nil || cfg.Redactor == nil || cfg.Clock == nil || cfg.MaxAttempts < 1 || cfg.Images == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "the supervisor stages need a store, GitHub, a judge, both keys, the verifiers, a redactor, a clock, an attempt limit and an image builder")
	}
	if cfg.BaseBranch == "" {
		cfg.BaseBranch = "main"
	}
	tree := Tree{GitHub: cfg.GitHub, Repo: cfg.Repo}
	loader, err := policy.NewLoader(tree, StoredBaseline{State: cfg.State})
	if err != nil {
		return nil, err
	}
	refs := QAPolicy(loader)
	publisher, err := catalog.New(catalog.Config{
		Repo: cfg.Repo, BaseBranch: cfg.BaseBranch, Actor: PublisherActor(), Files: cfg.GitHub,
		Branches: Branches{GitHub: cfg.GitHub}, Pulls: PublicationPulls{GitHub: cfg.GitHub},
	})
	if err != nil {
		return nil, err
	}
	var prs *CandidatePRs
	if cfg.Merger != nil {
		prs = &CandidatePRs{Repo: cfg.Repo, Base: cfg.BaseBranch, State: cfg.State, GitHub: cfg.GitHub, Merger: cfg.Merger, Policy: refs, Clock: cfg.Clock,
			UnitInputs: cfg.Release.UnitInputs,
			self:       github.User{ID: cfg.Self.ID, Login: cfg.Self.Login}}
	}
	rc := cfg.Release
	rc.FactoryID, rc.State, rc.Verifier, rc.Publisher, rc.Clock = cfg.FactoryID, cfg.State, cfg.Verifier, publisher, cfg.Clock
	rc.Blocks = releaseBlocks{}
	if rc.Commits == nil {
		rc.Commits = tree
	}
	if prs != nil {
		rc.Lander = prs
	}
	if rc.Policy == nil {
		rc.Policy = func(ctx context.Context) (core.CommitSHA, error) {
			r, err := refs(ctx)
			return r.PolicySHA, err
		}
	}
	pipeline, err := release.New(rc)
	if err != nil {
		return nil, err
	}
	supply, err := supplychain.NewGate(supplychain.GateConfig{
		FactoryID: cfg.FactoryID, Tree: tree, Scans: NoScans{}, Approvals: NoDependencyApprovals{},
		ApprovalVerifier: cfg.ApprovalVerifier, Clock: cfg.Clock,
	})
	if err != nil {
		return nil, err
	}
	evaluator, err := iqa.New(iqa.Config{
		FactoryID: cfg.FactoryID, Identity: cfg.QAIdentity, Workspace: cfg.QAWorkspace,
		Gates: HardGates(GateSources{State: cfg.State, Files: tree, Redactor: cfg.Redactor, LKG: rc.LKG, Backend: rc.Backend, SupplyChain: supply}),
		Judge: cfg.Judge, Baselines: Baselines{Workspace: cfg.QAWorkspace, Policy: refs}, Builds: Ledger{State: cfg.State},
		Review: tree, Routes: cfg.Routes, SameRouteAcknowledged: cfg.SameRouteAcknowledged,
		Signer: cfg.QASigner, Verifier: cfg.Verifier, Clock: cfg.Clock,
	})
	if err != nil {
		return nil, err
	}
	releaseStage := &ReleaseStage{FactoryID: cfg.FactoryID, State: cfg.State, Pipeline: pipeline, ArtifactSigner: cfg.ArtifactSigner, Tree: tree, Clock: cfg.Clock}
	if prs != nil {
		releaseStage.Waits = prs
	}
	return &Stages{
		Pipeline:     pipeline,
		CandidatePRs: prs,
		Build: &BuildStage{
			Owner: "supervisor-builder", State: cfg.State, QAPolicy: refs, CodingPolicy: stepPolicy(loader, core.StepCoding), Clock: cfg.Clock,
			Retry:   cfg.BuildRetry,
			Builder: &CandidateBuilder{FactoryID: cfg.FactoryID, Repo: cfg.Repo, Git: cfg.GitHub, Images: cfg.Images, Units: UnitRouter{Inputs: cfg.Release.UnitInputs}, Clock: cfg.Clock},
		},
		QA: &QAStage{
			FactoryID: cfg.FactoryID, Owner: "supervisor-qa", State: cfg.State, Evaluator: evaluator, Tree: tree, Policy: refs,
			MaxAttempts: cfg.MaxAttempts, PassScore: passScore(rc.PassScore), ExtraGates: rc.ExtraGates, Budget: cfg.Budget, Clock: cfg.Clock,
			Delegation: cfg.Delegation,
		},
		Release: releaseStage,
	}, nil
}

// PassResult is what one round of the stages did.
type PassResult struct {
	Build        BuildPassResult
	QA           QAPassResult
	CandidatePRs CandidatePRPassResult
	Release      ReleasePassResult
}

// Errors joins what each stage reported.
func (r PassResult) Errors() []error {
	return slices.Concat(r.Build.Errors, r.QA.Errors, r.CandidatePRs.Errors, r.Release.Errors)
}

// Pass runs one round of the stages in lifecycle order, so a patch can be
// built, judged, proposed as a pull request and released in the same round.
func (s *Stages) Pass(ctx context.Context) PassResult {
	var r PassResult
	r.Build = s.Build.Pass(ctx)
	r.QA = s.QA.Pass(ctx)
	if s.CandidatePRs != nil {
		r.CandidatePRs = s.CandidatePRs.Pass(ctx)
	}
	r.Release = s.Release.Pass(ctx)
	return r
}

func passScore(configured int) int {
	if configured == 0 {
		return release.DefaultPassScore
	}
	return configured
}

// PublisherActor is the service identity the changelog and the catalog are
// written as.
func PublisherActor() core.Actor {
	return core.Actor{Kind: core.ActorService, ID: "service:publisher", Roles: []core.Role{core.RolePublisher}}
}

// Branches adapts the GitHub client to the catalog's branch writer: it only
// ever creates or moves the Publisher's own branches.
type Branches struct{ GitHub GitHub }

// Head returns where branch points.
func (b Branches) Head(ctx context.Context, repo github.RepoRef, branch string) (core.CommitSHA, error) {
	return b.GitHub.BranchHead(ctx, repo, branch)
}

// Commit commits c on its branch, which starts at c.Base.
func (b Branches) Commit(ctx context.Context, repo github.RepoRef, c catalog.Commit) (core.CommitSHA, error) {
	return b.GitHub.CommitToBranch(ctx, repo, github.BranchCommit{Branch: c.Branch, Base: c.Base, Message: c.Message, Author: c.Author, Files: c.Files})
}
