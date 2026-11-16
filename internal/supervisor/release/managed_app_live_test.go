//go:build smoke

package release

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/adapters/deploy/compose"
	"github.com/bestagentkits/sisf/internal/dockerfilter"
	"github.com/bestagentkits/sisf/internal/edge"
	"github.com/bestagentkits/sisf/internal/supplychain"
	"github.com/bestagentkits/sisf/pkg/appbundle"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/deploy"
	"github.com/bestagentkits/sisf/pkg/managedapp"
	"github.com/bestagentkits/sisf/pkg/managedapp/changegate"
	"github.com/bestagentkits/sisf/pkg/managedapp/checkrun"
)

// The managed shop smoke test drives the real release path against Docker:
// the deploy role of the Docker request filter, the Compose backend, the
// edge and this pipeline, over the trial WooCommerce shop that
// examples/woocommerce-app/bin/release-smoke.sh prepares as its Owner would:
// the database, the secrets, the uploads volume and three bundles.
//
//	SISF_SHOP_BUNDLES  directory holding v1.tar, v2.tar and bad.tar
//	SISF_SHOP_NETWORK  the network the database answers on as "db"
//	SISF_SHOP_UPLOADS  the uploads volume, owned by 33:33
//	SISF_SHOP_SECRETS  the volume holding db_password and wp_secret
//	SISF_SHOP_EDGE     host:port the edge serves the shop on
//	SISF_DOCKER_SOCK   the Docker socket the filter forwards to
//
// Containers are reached as <name>.orb.local, as OrbStack names them.

// trafficClock is the world's clock whose every wait sends real requests
// through the edge first, so a window the monitor reads holds traffic.
type trafficClock struct {
	*fakeClock
	drive func()
}

func (c trafficClock) After(d time.Duration) <-chan time.Time {
	fired := make(chan time.Time, 1)
	now := c.Advance(d)
	c.drive()
	fired <- now
	return fired
}

func env(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		t.Skipf("%s is not set; run examples/woocommerce-app/bin/release-smoke.sh", name)
	}
	return v
}

func TestManagedShopReleasesAndRollsBackOnRealDocker(t *testing.T) {
	bundlesDir, network := env(t, "SISF_SHOP_BUNDLES"), env(t, "SISF_SHOP_NETWORK")
	uploads, secrets := env(t, "SISF_SHOP_UPLOADS"), env(t, "SISF_SHOP_SECRETS")
	edgeAddr, sock := env(t, "SISF_SHOP_EDGE"), env(t, "SISF_DOCKER_SOCK")
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	spec, err := managedapp.Load(filepath.Join("..", "..", "..", "examples", "woocommerce-app", managedapp.FileName))
	if err != nil {
		t.Fatal(err)
	}
	store := appbundle.Store{Dir: t.TempDir()}
	limits := appbundle.Limits{MaxTotalBytes: 1 << 30, MaxFiles: 100000, MaxFileBytes: 128 << 20}
	bundle := map[string]string{}
	for _, name := range []string{"v1", "v2", "bad"} {
		f, err := os.Open(filepath.Join(bundlesDir, name+".tar"))
		if err != nil {
			t.Fatal(err)
		}
		bundle[name], err = store.Put(f, limits)
		_ = f.Close()
		if err != nil {
			t.Fatalf("store %s: %v", name, err)
		}
	}
	image := spec.Runtime.Image
	pair := func(name string) core.ReleaseDigest { return deploy.PairDigest(image, bundle[name]) }

	// The sensitive change of the trial, a Core upgrade, is the Owner's.
	gate := changegate.Judge(changegate.Input{
		Baseline: spec, BaselineLock: mustLock(t, "core wordpress 7.0.4 "+strings.Repeat("a", 64)),
		CandidateLock: mustLock(t, "core wordpress 7.0.8 "+strings.Repeat("b", 64)), Changed: []string{"content.lock"},
	})
	if !gate.Owner {
		t.Fatalf("a Core upgrade was judged the factory's: %+v", gate)
	}

	// The world: real store, keys and LKG, with the shop's first bundle as
	// the release the install recorded.
	clock := &fakeClock{now: startTime}
	w := &world{t: t, clock: clock, keys: newKeyring(t, clock), freeze: &fakeFreeze{}, publisher: &fakePublisher{}, commits: &fakeCommits{}}
	w.store = openStore(t, clock)
	w.trustDir = filepath.Join(t.TempDir(), "trust")
	if err := os.Mkdir(w.trustDir, 0o700); err != nil {
		t.Fatal(err)
	}
	w.book = w.recordInstallLKG(pair("v1"))

	// The deploy role of the filter, in front of the real daemon.
	upstream := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}
	revisionEnv := spec.Runtime.RevisionEnv()
	dataVolumes := []compose.VolumeMount{
		{Name: uploads, Target: spec.Data[0].Target},
		{Name: secrets, Target: managedapp.SecretsDir, ReadOnly: true},
	}
	filter, err := dockerfilter.New(dockerfilter.Config{
		Role: dockerfilter.RoleDeploy, Upstream: upstream, RunnerValue: testFactory, DeployNetwork: network,
		Images: []string{image}, BundleTarget: spec.Bundle.Mount, DeployEnv: revisionEnv, Logger: log,
		DataVolumes: []dockerfilter.DeployVolume{
			{Name: uploads, Target: spec.Data[0].Target},
			{Name: secrets, Target: managedapp.SecretsDir, ReadOnly: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	filterSock := filepath.Join(shortTempDir(t), "deploy.sock")
	serveOn(t, "unix", filterSock, filter)

	resolve := func(container string) string { return "http://" + container + ".orb.local" }
	containerOf := func(d core.ReleaseDigest) string { return string(testFactory) + "-" + testUnit + "-" + string(d)[7:19] }
	edgeURL := "http://" + edgeAddr
	traffic := &driver{url: edgeURL + "/robots.txt", perWindow: 400, workers: 16}
	tclock := trafficClock{fakeClock: clock, drive: traffic.drive}
	splitter, err := edge.NewSplitter(tclock, log, edge.Target{Revision: string(pair("v1")), URL: resolve(containerOf(pair("v1")))})
	if err != nil {
		t.Fatal(err)
	}
	serveOn(t, "tcp", edgeAddr, splitter)

	backend, err := compose.New(compose.Config{
		Endpoint: "unix://" + filterSock, Factory: testFactory, Network: network, AppPort: spec.Runtime.Port,
		Env: revisionEnv, Volumes: dataVolumes, User: spec.Runtime.User,
		BundleTarget: spec.Bundle.Mount, BundleSource: store, Edge: splitter, Resolve: resolve,
		ReadyTimeout: 3 * time.Minute, Logger: log,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { traffic.report(t) })

	// Bootstrap: the install starts the recorded release and sends it all
	// traffic, as the supervisor's install does.
	v1 := w.bundleArtifact(t, image, bundle["v1"])
	rev, err := backend.Stage(ctx, v1)
	if err != nil {
		t.Fatalf("stage the installed release: %v", err)
	}
	if err := backend.Promote(ctx, rev, 100); err != nil {
		t.Fatalf("serve the installed release: %v", err)
	}

	w.cfg = Config{
		FactoryID: testFactory, State: w.store, Backend: backend, Verifier: w.keys.verifier, VerifierAt: w.keys.verifierAt, LKG: w.book,
		Freeze: w.freeze, Publisher: w.publisher, Blocks: storeBlocks{}, Clock: tclock, Commits: w.commits,
		Monitor: MonitorConfig{Window: time.Minute, MinSamples: 15},
	}
	p := w.pipeline()

	// The good update: WooCommerce 11.2.1 -> 11.2.2, released by canary.
	good := w.bundleRequest(t, issueRef(101), "run-good", image, bundle["v2"])
	out, err := p.Release(ctx, good)
	if err != nil || out.Result != ResultClosed || out.Observed.Digest != pair("v2") {
		t.Fatalf("the good update: %+v, %v", out, err)
	}
	t.Logf("good update closed: %d windows, observed %s", len(out.Windows), out.Observed.Digest)

	// A purchase on the live shop, through the edge, as a customer would.
	purchase := spec.Checks[slices.IndexFunc(spec.Checks, func(c managedapp.Check) bool { return c.Name == "purchase" })]
	if r := (checkrun.Runner{BaseURL: edgeURL, Client: &http.Client{Timeout: 30 * time.Second}}).Run(ctx, purchase); !r.Passed {
		t.Fatalf("a purchase after the good update failed: %+v", r)
	}

	// The failing update: the shop plugin breaks every page but health.
	bad := w.bundleRequest(t, issueRef(102), "run-bad", image, bundle["bad"])
	out, err = p.Release(ctx, bad)
	if err != nil || out.Result != ResultBlocked || out.RestoredTo != pair("v2") || out.Observed.Digest != pair("v2") {
		t.Fatalf("the failing update: %+v, %v", out, err)
	}
	t.Logf("failing update rolled back to %s: %s", out.RestoredTo, out.Reason)
	if resp, err := http.Get(edgeURL + "/healthz"); err != nil || resp.StatusCode != http.StatusOK || resp.Header.Get(edge.RevisionHeader) != string(pair("v2")) {
		t.Fatalf("after the rollback the edge serves %v, %v", resp, err)
	}
}

func mustLock(t *testing.T, lines ...string) *appbundle.Lock {
	t.Helper()
	l, err := appbundle.ParseLock(strings.NewReader(strings.Join(lines, "\n")))
	if err != nil {
		t.Fatal(err)
	}
	return &l
}

// bundleArtifact is the signed artifact of the runtime image and bundle.
func (w *world) bundleArtifact(t *testing.T, image, bundle string) deploy.Artifact {
	t.Helper()
	d := deploy.PairDigest(image, bundle)
	a, err := supplychain.SignArtifact(context.Background(), w.keys.artifact, deploy.Artifact{
		FactoryID: testFactory, Unit: testUnit, Digest: d, Image: image, Bundle: bundle,
		SBOMRef: "sbom:" + string(d), ProvenanceRef: "provenance:" + string(d),
	})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// bundleRequest is a release request of the bundle, with a passing receipt.
func (w *world) bundleRequest(t *testing.T, issue core.IssueRef, run core.RunID, image, bundle string) Request {
	t.Helper()
	a := w.bundleArtifact(t, image, bundle)
	c := candidate(issue, run, core.Digest(a.Digest))
	return Request{
		IssueID: issue, RunID: run, Candidate: c, Receipt: w.keys.receiptFor(t, c, nil), Artifact: a,
		Feature: "Managed shop update", Summary: "Update the shop",
	}
}

// shortTempDir is a temporary directory with a path short enough for a
// unix socket.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "sisf-smoke-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func serveOn(t *testing.T, network, addr string, h http.Handler) {
	t.Helper()
	ln, err := net.Listen(network, addr)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
}

// driver sends a window's worth of real requests through the edge.
type driver struct {
	url                string
	perWindow, workers int
	mu                 sync.Mutex
	sent, failed       int
	byStatus           map[int]int
}

func (d *driver) drive() {
	client := &http.Client{Timeout: 30 * time.Second}
	jobs := make(chan struct{})
	var wg sync.WaitGroup
	for range d.workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range jobs {
				status := 0
				resp, err := client.Get(d.url)
				if err == nil {
					_, _ = io.Copy(io.Discard, resp.Body)
					_ = resp.Body.Close()
					status = resp.StatusCode
				}
				d.mu.Lock()
				d.sent++
				if d.byStatus == nil {
					d.byStatus = map[int]int{}
				}
				d.byStatus[status]++
				if err != nil && !errors.Is(err, context.Canceled) {
					d.failed++
				}
				d.mu.Unlock()
			}
		}()
	}
	for range d.perWindow {
		jobs <- struct{}{}
	}
	close(jobs)
	wg.Wait()
}

func (d *driver) report(t *testing.T) {
	d.mu.Lock()
	defer d.mu.Unlock()
	t.Log(fmt.Sprintf("traffic: %d requests, %d transport errors, by status %v", d.sent, d.failed, d.byStatus))
}
