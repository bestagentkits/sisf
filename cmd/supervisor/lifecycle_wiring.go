package main

import (
	"context"
	"crypto/ed25519"
	"log/slog"
	"path/filepath"

	"github.com/bestagentkits/sisf/cmd/factoryd/wiring"
	"github.com/bestagentkits/sisf/cmd/supervisor/stages"
	"github.com/bestagentkits/sisf/internal/audit"
	"github.com/bestagentkits/sisf/internal/intake/prgate"
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
	sim := wiring.SimulationProfile(m)
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
	keys, err := loadSupervisorKeys(ctx, d.cfg.TrustDir, m.FactoryID, rootPub, root, d.clock)
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
	admin, err := l.edgeAdmin(d.lookupEnv)
	if err != nil {
		return nil, err
	}
	backend, err := l.deployBackend(m.FactoryID, admin, d.clock, d.log)
	if err != nil {
		return nil, err
	}
	monitor, err := l.tuning.monitor(sim)
	if err != nil {
		return nil, err
	}
	if l.deploy == "local" {
		l.adopter = &lkgAdopter{factory: m.FactoryID, backend: backend, lkg: book, signer: keys.Artifact, image: "local/" + string(m.FactoryID) + "/" + adoptedUnit}
	}
	gate, err := prgate.New(prgate.Config{Visibility: prgate.Visibility(l.visibility), AgentLogins: []string{settings.Self.Login}, Audit: log, Clock: d.clock})
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
		manifest: m, sim: sim, docker: docker, local: local, trustDir: d.cfg.TrustDir, dsnEnv: d.dsnEnv, rootKeyEnv: l.rootPrivateKeyEnv,
		lookupEnv: d.lookupEnv, clock: d.clock, store: d.stop.store,
		materials: stages.CandidateMaterials{State: d.stop.store, Tree: stages.Tree{GitHub: client, Repo: repo}},
	})
	if err != nil {
		return nil, err
	}
	return stages.New(stages.Config{
		FactoryID: m.FactoryID, Repo: repo, State: d.stop.store, SameRouteAcknowledged: m.ModelRoutes.SameModelRoute,
		GitHub: client, Judge: judge, QAIdentity: "agent:qa/" + string(m.FactoryID), QAWorkspace: "qa/" + string(m.FactoryID),
		QASigner: keys.QA, ArtifactSigner: keys.Artifact, Verifier: keys.Verifier, ApprovalVerifier: approvalVerifier,
		Redactor: d.redactor, MaxAttempts: m.Limits.MaxAttempts, Images: images, Clock: d.clock,
		Release: release.Config{Backend: backend, LKG: book, Freeze: d.stop.sw, Approvals: spender, Audit: log, Monitor: monitor},
	})
}

// gitHubClient connects to the factory's repository with the token in the
// environment.
func gitHubClient(m *manifest.Manifest, lookupEnv func(string) (string, bool), clock core.Clock) (*github.RESTClient, wiring.GitHubSettings, error) {
	settings, err := wiring.GitHubSettingsOf(m)
	if err != nil {
		return nil, settings, err
	}
	if url, ok := lookupEnv(envGitHubAPIURL); ok && url != "" {
		settings.APIURL = url
	}
	token, _ := lookupEnv(envGitHubToken)
	client, err := wiring.NewGitHubClient(settings, core.NewSecretValue([]byte(token)), clock)
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
