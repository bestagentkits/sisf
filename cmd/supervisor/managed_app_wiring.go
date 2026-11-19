package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/adapters/deploy/compose"
	"github.com/bestagentkits/sisf/cmd/supervisor/stages"
	"github.com/bestagentkits/sisf/internal/edge"
	"github.com/bestagentkits/sisf/internal/supervisor"
	"github.com/bestagentkits/sisf/internal/supervisor/release"
	"github.com/bestagentkits/sisf/pkg/appbundle"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/deploy"
	"github.com/bestagentkits/sisf/pkg/managedapp"
	"github.com/bestagentkits/sisf/pkg/managedapp/checkrun"
)

// bundleStoreRecord names, in the trust volume, the directory where the
// supervisor keeps the content bundles of a managed application, each under
// the sha256 of the bytes it received.
const bundleStoreRecord = "bundles"

// checkTimeout bounds one request of a check the supervisor runs against a
// revision.
const checkTimeout = 30 * time.Second

// managedAppFlags configure a managed application: an application the
// factory releases as a runtime image and a content bundle (ADR 0027). The
// Docker request filter of the deploy role is given the same --app-spec,
// --app-volume and --app-secrets-volume, so the revisions the Compose
// backend starts are the ones the filter admits.
type managedAppFlags struct {
	spec    string
	binding managedapp.Binding
	// hostSuffix, when set, reaches a revision at <container><suffix>
	// instead of its name on the Compose network: ".orb.local" for a
	// supervisor that runs on an OrbStack host, outside the network.
	hostSuffix   string
	readyTimeout time.Duration
	// install is the loaded Spec and its revision; set by load.
	install *managedapp.Install
}

func (a *managedAppFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&a.spec, "app-spec", "", "deploy compose: absolute path of the managed application's installed "+managedapp.FileName+"; releases then run its runtime image over a content bundle")
	fs.Func("app-volume", "with --app-spec: name=volume binding of one of the Spec's data volumes (repeatable)", a.binding.BindVolume)
	fs.StringVar(&a.binding.Secrets, "app-secrets-volume", "", "with --app-spec: the volume holding the runtime's secrets as files, mounted read-only")
	fs.StringVar(&a.hostSuffix, "deploy-host-suffix", "", "deploy compose: reach a revision at <container><suffix> (for example .orb.local) instead of its name on the Compose network")
	fs.DurationVar(&a.readyTimeout, "deploy-ready-timeout", 0, "deploy compose: how long a staged revision has to report itself healthy; the backend's default when zero")
}

// load reads the managed application's Spec once. Without --app-spec there
// is none, and a binding given without it is refused.
func (a *managedAppFlags) load(deployBackend string) (*managedapp.Install, error) {
	if a.install != nil {
		return a.install, nil
	}
	if a.spec == "" {
		if len(a.binding.Volumes) > 0 || a.binding.Secrets != "" {
			return nil, stages.Misconfigured(core.NewError(core.CodeInvalidArgument, "--app-volume and --app-secrets-volume need --app-spec"))
		}
		return nil, nil
	}
	if deployBackend != "compose" {
		return nil, stages.Misconfigured(core.NewError(core.CodeInvalidArgument, "a managed application is released by the compose deploy backend only"))
	}
	in, err := managedapp.LoadInstall(a.spec, a.binding)
	if err != nil {
		return nil, stages.MisconfiguredWhenInvalid(err)
	}
	a.install = in
	return in, nil
}

// bundleStore is the store of the managed application's content bundles on
// the trust volume.
func bundleStore(trustDir string) (appbundle.Store, error) {
	dir, err := supervisor.TrustDirectory(trustDir, bundleStoreRecord)
	if err != nil {
		return appbundle.Store{}, err
	}
	return appbundle.Store{Dir: dir}, nil
}

// composeConfig is the Compose backend's configuration: the flags, and for
// a managed application its revision, its bundle store and how a revision
// is reached.
func (l *lifecycleFlags) composeConfig(factory core.FactoryID, ctl edge.Controller, trustDir string, clock core.Clock, log *slog.Logger) (compose.Config, error) {
	cfg := compose.Config{
		Endpoint: l.deployEndpoint, Factory: factory, Network: l.deployNetwork, AppPort: l.appPort,
		Edge: ctl, Clock: clock, Logger: log, ReadyTimeout: l.app.readyTimeout,
	}
	app, err := l.app.load(l.deploy)
	if err != nil || app == nil {
		return cfg, err
	}
	cfg.UseRevision(app.Revision)
	store, err := bundleStore(trustDir)
	if err != nil {
		return cfg, err
	}
	cfg.BundleSource = store
	if suffix := l.app.hostSuffix; suffix != "" {
		port := strconv.Itoa(cfg.AppPort)
		cfg.Resolve = func(container string) string { return "http://" + container + suffix + ":" + port }
	}
	return cfg, nil
}

// managedObserver measures a managed application's releases as the pair of
// the runtime image and the bundle bytes the store holds now; nil without
// a managed application.
func (l *lifecycleFlags) managedObserver(trustDir string) (release.ArtifactObserver, error) {
	app, err := l.app.load(l.deploy)
	if err != nil || app == nil {
		return nil, err
	}
	store, err := bundleStore(trustDir)
	if err != nil {
		return nil, err
	}
	return appbundle.PairObserver{Store: store}, nil
}

// managedChecks is the synthetic suite of a managed application: the
// Spec's checks that change no data, run against the revision itself. A
// check that writes, such as a purchase, never runs against a live
// revision: it runs on a copy restored from the latest backup.
type managedChecks struct {
	checks  []managedapp.Check
	backend deploy.Backend
	client  *http.Client
	log     *slog.Logger
}

// newManagedChecks keeps the Spec's read-only checks. A Spec with none
// gives no suite, so a low-traffic release holds, as without one.
func newManagedChecks(spec *managedapp.Spec, backend deploy.Backend, log *slog.Logger) *managedChecks {
	s := &managedChecks{backend: backend, client: &http.Client{Timeout: checkTimeout}, log: log}
	for _, c := range spec.Checks {
		if !c.Writes {
			s.checks = append(s.checks, c)
		}
	}
	if len(s.checks) == 0 {
		return nil
	}
	return s
}

// Run runs every read-only check against rev; the run passes only when all
// of them pass. A revision with no address judged nothing.
func (s *managedChecks) Run(ctx context.Context, rev deploy.Revision) (release.SyntheticResult, error) {
	preview, err := s.backend.Preview(ctx, rev)
	if err != nil {
		return release.SyntheticResult{}, err
	}
	if preview.URL == "" {
		return release.SyntheticResult{}, core.NewError(core.CodeReleaseHold, "the revision has no address the checks can reach")
	}
	runner := checkrun.Runner{BaseURL: preview.URL, Client: s.client}
	var failed []string
	for _, c := range s.checks {
		r := runner.Run(ctx, c)
		if ctx.Err() != nil {
			return release.SyntheticResult{}, ctx.Err()
		}
		if !r.Passed {
			failed = append(failed, c.Name)
			s.log.Warn("managed application check failed", "unit", rev.Unit, "release", rev.Digest, "check", c.Name, "step", r.Step, "reason", r.Reason)
		}
	}
	if len(failed) > 0 {
		return release.SyntheticResult{Detail: "the checks " + strings.Join(failed, ", ") + " failed"}, nil
	}
	return release.SyntheticResult{Passed: true, Detail: "the application's read-only checks passed"}, nil
}
