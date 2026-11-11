package main

import (
	"context"
	"crypto/ed25519"
	"log/slog"
	"path/filepath"

	"github.com/bestagentkits/sisf/cmd/supervisor/stages"
	"github.com/bestagentkits/sisf/internal/audit"
	"github.com/bestagentkits/sisf/internal/budget"
	"github.com/bestagentkits/sisf/internal/intake/prgate"
	"github.com/bestagentkits/sisf/internal/manifestsettings"
	"github.com/bestagentkits/sisf/internal/redact"
	"github.com/bestagentkits/sisf/internal/signing"
	"github.com/bestagentkits/sisf/internal/supervisor"
	"github.com/bestagentkits/sisf/internal/supervisor/release"
	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// lifecycleDeps are what the stages are built from besides the flags.
type lifecycleDeps struct {
	cfg       supervisor.Config
	stop      *emergencyStop
	lookupEnv func(string) (string, bool)
	redactor  *redact.Redactor
	log       *slog.Logger
	clock     core.Clock
	// dsnEnv names the variable holding the state store connection.
	dsnEnv string
}

// buildLifecycle wires the build, QA and release stages of the factory.
// Keys are loaded here and handed on only as signers.
func (l *lifecycleFlags) buildLifecycle(ctx context.Context, d lifecycleDeps) (*stages.Stages, error) {
	if l.buildOutageLimit < 1 {
		return nil, core.NewError(core.CodeInvalidArgument, "--build-outage-limit must be at least 1")
	}
	if d.stop == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "the lifecycle needs the state store; set the variable --state-dsn-env names")
	}
	m, err := manifest.Load(l.factoryManifest)
	if err != nil {
		return nil, err
	}
	if m.FactoryID != d.cfg.FactoryID {
		return nil, core.NewError(core.CodeInvalidArgument, "the factory manifest is not the one of --factory-id")
	}
	inputs, err := release.NewUnitInputs(declaredUnitInputs(m))
	if err != nil {
		return nil, err
	}
	sim := manifestsettings.SimulationProfile(m)
	client, settings, err := gitHubClient(m, d.lookupEnv, d.clock)
	if err != nil {
		return nil, err
	}
	rootPub, err := signing.ResolvePublicKey(d.cfg.RootKeyRef, d.lookupEnv)
	if err != nil {
		return nil, err
	}
	root, err := rootSignerFrom(l.rootPrivateKeyEnv, d.lookupEnv)
	if err != nil {
		return nil, err
	}
	keys, err := loadSupervisorKeys(ctx, d.cfg.TrustDir, m.FactoryID, rootPub, root, d.clock, d.log)
	if err != nil {
		return nil, err
	}
	approvalPub, err := signing.ResolvePublicKey(l.approvalKeyRef, d.lookupEnv)
	if err != nil {
		return nil, err
	}
	approvalVerifier, err := signing.NewVerifier(approvalPub)
	if err != nil {
		return nil, err
	}
	log := audit.New(d.stop.store, d.clock)
	spender, err := approvals.NewStore(approvals.Config{State: d.stop.store, FactoryID: m.FactoryID, Signer: spendOnly{},
		Verifier: approvalVerifier, Audit: log, Clock: d.clock})
	if err != nil {
		return nil, err
	}
	book, err := lkgBook(d.cfg, m.FactoryID, rootPub, d.clock)
	if err != nil {
		return nil, err
	}
	if err := l.openLocalPrograms(ctx, d.cfg.TrustDir, sim, book); err != nil {
		return nil, err
	}
	if store := l.programs.buildStore(); store != nil {
		l.retention = newProgramRetention(store, book, d.stop.store)
	}
	admin, err := l.edgeAdmin(d.lookupEnv)
	if err != nil {
		return nil, err
	}
	backend, err := l.deployBackend(m.FactoryID, admin, d.cfg.TrustDir, d.clock, d.log)
	if err != nil {
		return nil, err
	}
	monitor, err := l.tuning.monitor(sim)
	if err != nil {
		return nil, err
	}
	// A release whose real traffic is too thin is judged by the
	// application's synthetic suite; without one it holds.
	synthetic, err := l.syntheticSuite(backend, d.log)
	if err != nil {
		return nil, err
	}
	if closer, ok := backend.(interface{ Close(context.Context) error }); ok {
		l.closeBackend = closer.Close
	}
	if l.deploy == "local" {
		l.adopter = &lkgAdopter{factory: m.FactoryID, backend: backend, lkg: book, signer: keys.Artifact, image: "local/" + string(m.FactoryID) + "/" + adoptedUnit}
	}
	// An install on the operator's gh login names no bot account of its own.
	var agentLogins []string
	if settings.Self.Login != "" {
		agentLogins = []string{settings.Self.Login}
	}
	gate, err := prgate.New(prgate.Config{Visibility: prgate.Visibility(l.visibility), AgentLogins: agentLogins, Audit: log, Clock: d.clock})
	if err != nil {
		return nil, err
	}
	docker, local, err := l.execBackends(m, d.clock)
	if err != nil {
		return nil, err
	}
	images, err := l.imageBuilder(ctx, m, sim, gate, docker, local)
	if err != nil {
		return nil, err
	}
	repo := github.RepoRef{Owner: m.Repository.Owner, Name: m.Repository.Repo}
	judge, err := l.judge(ctx, judgeDeps{
		manifest: m, sim: sim, docker: docker, local: local, protected: l.protectedDirs(d.cfg.TrustDir, d.dsnEnv, d.lookupEnv), dsnEnv: d.dsnEnv, rootKeyEnv: l.rootPrivateKeyEnv,
		lookupEnv: d.lookupEnv, clock: d.clock, store: d.stop.store,
		materials: stages.CandidateMaterials{State: d.stop.store, Tree: stages.Tree{GitHub: client, Repo: repo}},
	})
	if err != nil {
		return nil, err
	}
	// Only the supervisor builds a merger: it lands a candidate on the
	// default branch after its release is verified.
	merger, err := github.NewMerger(client)
	if err != nil {
		return nil, err
	}
	spend := budget.NewGate(budget.Config{Clock: d.clock})
	return stages.New(stages.Config{
		FactoryID: m.FactoryID, Repo: repo, State: d.stop.store, SameRouteAcknowledged: m.ModelRoutes.SameModelRoute,
		GitHub: client, Merger: merger, Self: settings.Self, Judge: judge, QAIdentity: "agent:qa/" + string(m.FactoryID), QAWorkspace: "qa/" + string(m.FactoryID),
		QASigner: keys.QA, ArtifactSigner: keys.Artifact, Verifier: keys.Verifier, ApprovalVerifier: approvalVerifier,
		Redactor: d.redactor, MaxAttempts: m.Limits.MaxAttempts, Images: images, Clock: d.clock,
		BuildRetry: stages.BuildRetry{HoldAfter: l.buildOutageLimit}, Budget: spend,
		Release: release.Config{Backend: backend, LKG: book, Freeze: d.stop.sw, Approvals: spender, Audit: log, Monitor: monitor, Synthetic: synthetic,
			SelfPublishable: selfPublishableUnits(m), UnitInputs: inputs, VerifierAt: keys.VerifierAt,
			// A rollback reserves through the rollback reserve and records
			// its spend; an exhausted budget never blocks it.
			Budget: spend},
	})
}

// gitHubClient connects to the factory's repository with the token in the
// environment.
func gitHubClient(m *manifest.Manifest, lookupEnv func(string) (string, bool), clock core.Clock) (*github.RESTClient, manifestsettings.GitHubSettings, error) {
	settings, err := manifestsettings.GitHubSettingsOf(m)
	if err != nil {
		return nil, settings, err
	}
	if url, ok := lookupEnv(envGitHubAPIURL); ok && url != "" {
		settings.APIURL = url
	}
	token, _ := lookupEnv(envGitHubToken)
	client, err := manifestsettings.NewGitHubClient(settings, core.NewSecretValue([]byte(token)), clock)
	return client, settings, err
}

// lkgBook opens the last known good book on the trust volume.
func lkgBook(cfg supervisor.Config, factory core.FactoryID, rootPub ed25519.PublicKey, clock core.Clock) (*release.LKGBook, error) {
	rootVerifier, err := signing.NewVerifier(rootPub)
	if err != nil {
		return nil, err
	}
	path := cfg.LKGPath
	if path == "" {
		path = filepath.Join(cfg.TrustDir, supervisor.DefaultLKGFile)
	}
	store, err := supervisor.OpenLKGStore(cfg.TrustDir, path, factory, rootVerifier)
	if err != nil {
		return nil, err
	}
	return release.OpenLKGBook(cfg.TrustDir, factory, store, clock)
}

// spendOnly is the approval store's signer in the supervisor, which only
// verifies and spends approvals: it never signs a decision.
type spendOnly struct{}

func (spendOnly) KeyID() string { return "supervisor-spend-only" }

func (spendOnly) Sign(context.Context, []byte) (core.Signature, error) {
	return core.Signature{}, core.NewError(core.CodePermissionDenied, "the supervisor does not decide approvals")
}
