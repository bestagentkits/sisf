package compose_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/bestagentkits/sisf/adapters/deploy/compose"
	"github.com/bestagentkits/sisf/internal/dockerfilter"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/deploy"
)

const (
	docRoot      = "/var/www/html"
	runtimeImage = "wordpress:7.0-php8.3-apache@sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
)

var (
	bundleA       = "sha256:" + strings.Repeat("1", 64)
	bundleB       = "sha256:" + strings.Repeat("2", 64)
	bundleVolumeA = string(factory) + "-bundle-" + strings.Repeat("1", 12)
	loaderA       = string(factory) + "-loader-" + strings.Repeat("1", 12)
	tarA          = []byte("the verified tar of bundle A")
)

// bundles is a BundleSource over tars held in memory. read runs on the
// first read of each stream, so a test can act in the middle of an upload;
// an error it returns ends the stream.
type bundles struct {
	mu    sync.Mutex
	tars  map[string][]byte
	opens []string
	fail  error
	read  func() error
}

func (s *bundles) Open(_ context.Context, bundle string) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.opens = append(s.opens, bundle)
	if s.fail != nil {
		return nil, s.fail
	}
	tar, ok := s.tars[bundle]
	if !ok {
		return nil, core.NewError(core.CodeNotFound, "no such bundle")
	}
	return io.NopCloser(&onRead{r: bytes.NewReader(tar), first: s.read}), nil
}

type onRead struct {
	r     io.Reader
	first func() error
}

func (o *onRead) Read(p []byte) (int, error) {
	if o.first != nil {
		first := o.first
		o.first = nil
		if err := first(); err != nil {
			return 0, err
		}
	}
	return o.r.Read(p)
}

func bundleArtifact(bundle string) deploy.Artifact {
	a := artifact(digestA)
	a.Image, a.Bundle = runtimeImage, bundle
	a.Digest = deploy.PairDigest(a.Image, a.Bundle)
	return a
}

// bundleWorld is a world whose backend runs bundle artifacts as www-data
// with an uploads volume, optionally through a deploy filter.
func bundleWorld(t *testing.T, src *bundles, filtered bool) *world {
	t.Helper()
	o := worldOptions{cfg: func(c *compose.Config) {
		c.User, c.BundleTarget, c.BundleSource = "33:33", docRoot, src
		c.Env = []string{"APACHE_RUN_DIR=/tmp/apache2"}
		c.Volumes = []compose.VolumeMount{{Name: "acme-app_uploads", Target: docRoot + "/wp-content/uploads"}}
	}}
	if filtered {
		o.filter = &dockerfilter.Config{
			DeployNetwork: "acme-app_default", Images: []string{runtimeImage, artifact(digestA).Image},
			BundleTarget: docRoot, DeployEnv: []string{"APACHE_RUN_DIR=/tmp/apache2"},
			DataVolumes: []dockerfilter.DeployVolume{{Name: "acme-app_uploads", Target: docRoot + "/wp-content/uploads"}},
		}
	}
	return newWorld(t, o)
}

func newBundles() *bundles {
	return &bundles{tars: map[string][]byte{bundleA: tarA, bundleB: []byte("bundle B")}}
}

// changes lists the requests that reached the daemon and were not reads.
// A container is shown as <c>, since a filter addresses it by the ID it
// inspected and the backend by its name; a create shows the name asked for.
func (w *world) changes() []string {
	var out []string
	for _, r := range w.daemon.Requests() {
		if r.Method == http.MethodGet {
			continue
		}
		path := r.Path[strings.Index(r.Path[1:], "/")+1:] // drop the API version
		if rest, ok := strings.CutPrefix(path, "/containers/"); ok && rest != "create" {
			_, action, _ := strings.Cut(rest, "/")
			path = strings.TrimSuffix("/containers/<c>/"+action, "/")
		}
		q, _ := url.ParseQuery(r.Query)
		for _, key := range []string{"name", "path"} {
			if v := q.Get(key); v != "" {
				path += "?" + key + "=" + v
			}
		}
		out = append(out, r.Method+" "+path)
	}
	return out
}

func (w *world) uploads() [][]byte {
	var out [][]byte
	for _, r := range w.daemon.Requests() {
		if r.Method == http.MethodPut {
			out = append(out, r.Body)
		}
	}
	return out
}

func TestABundleArtifactRunsItsBundleReadOnlyOverTheRuntimeImage(t *testing.T) {
	t.Parallel()
	for _, filtered := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct", true: "through the deploy filter"}[filtered], func(t *testing.T) {
			t.Parallel()
			w := bundleWorld(t, newBundles(), filtered)
			a := bundleArtifact(bundleA)
			rev, err := w.backend.Stage(context.Background(), a)
			if err != nil {
				t.Fatalf("Stage: %v", err)
			}
			if rev.Digest != a.Digest || !w.running(name(a.Digest)) {
				t.Fatalf("revision %+v is not running", rev)
			}
			want := []string{
				"POST /volumes/create",
				"POST /containers/create?name=" + loaderA,
				"PUT /containers/<c>/archive?path=" + compose.LoaderTarget,
				"DELETE /containers/<c>",
				"POST /containers/create?name=" + name(a.Digest),
				"POST /containers/<c>/start",
			}
			if got := w.changes(); !slices.Equal(got, want) {
				t.Fatalf("the daemon saw\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
			if up := w.uploads(); len(up) != 1 || !bytes.Equal(up[0], tarA) {
				t.Fatalf("uploads = %q, want the bundle once", up)
			}
			if v, ok := w.daemon.Volumes()[bundleVolumeA]; !ok || v.Labels[compose.LabelBundle] != bundleA || v.Labels[compose.LabelFactory] != string(factory) {
				t.Fatalf("bundle volume = %+v", w.daemon.Volumes())
			}
			for _, c := range w.daemon.Containers() {
				if c.Name == loaderA {
					t.Fatal("the loader was left behind")
				}
			}
			created := w.daemon.Created()
			var loader, revision struct {
				Image      string
				Cmd        []string
				Env        []string
				User       string
				HostConfig struct {
					NetworkMode string
					Mounts      []struct {
						Type, Source, Target string
						ReadOnly             bool
					}
				}
			}
			if err := json.Unmarshal(created[0].Body, &loader); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(created[1].Body, &revision); err != nil {
				t.Fatal(err)
			}
			lm := loader.HostConfig.Mounts
			if loader.Image != runtimeImage || loader.Cmd != nil || len(loader.Env) != 0 || loader.HostConfig.NetworkMode != "none" ||
				len(lm) != 1 || lm[0].Source != bundleVolumeA || lm[0].Target != compose.LoaderTarget || lm[0].ReadOnly {
				t.Errorf("loader = %+v", loader)
			}
			rm := revision.HostConfig.Mounts
			if revision.User != "33:33" || len(rm) != 2 || rm[0].Source != bundleVolumeA || rm[0].Target != docRoot || !rm[0].ReadOnly ||
				rm[1].Source != "acme-app_uploads" || rm[1].ReadOnly {
				t.Errorf("revision = %+v", revision)
			}
			if !slices.Equal(revision.Env, []string{compose.RevisionEnv + "=" + string(a.Digest), "APACHE_RUN_DIR=/tmp/apache2"}) {
				t.Errorf("revision env = %v", revision.Env)
			}
			if raw := string(created[1].Body); strings.Contains(raw, `"Binds"`) || strings.Contains(raw, `"bind"`) {
				t.Errorf("the revision asks for a host path: %s", raw)
			}
		})
	}
}

func TestABundleIsLoadedOnceAndSharedByItsRevisions(t *testing.T) {
	t.Parallel()
	src := newBundles()
	w := bundleWorld(t, src, false)
	a := bundleArtifact(bundleA)
	for range 2 {
		if _, err := w.backend.Stage(context.Background(), a); err != nil {
			t.Fatalf("Stage: %v", err)
		}
	}
	// Another pair over the same bundle reuses its volume.
	other := a
	other.Image = "wordpress:7.0-php8.4-apache@sha256:" + strings.Repeat("d", 64)
	other.Digest = deploy.PairDigest(other.Image, other.Bundle)
	if _, err := w.backend.Stage(context.Background(), other); err != nil {
		t.Fatalf("Stage of another pair: %v", err)
	}
	if up := w.uploads(); len(up) != 1 {
		t.Fatalf("%d uploads, want the bundle loaded once", len(up))
	}
	if len(src.opens) != 1 {
		t.Fatalf("the bundle was read %d times", len(src.opens))
	}
	if _, err := w.backend.Stage(context.Background(), bundleArtifact(bundleB)); err != nil {
		t.Fatalf("Stage of another bundle: %v", err)
	}
	if up := w.uploads(); len(up) != 2 || len(w.daemon.Volumes()) != 2 {
		t.Fatalf("another bundle must get its own volume: %d uploads, volumes %v", len(up), w.daemon.Volumes())
	}
}

// Two Stages over one bundle at once never undo each other's load.
func TestConcurrentStagesOverOneBundleLoadItOnce(t *testing.T) {
	t.Parallel()
	w := bundleWorld(t, newBundles(), false)
	pairs := make([]deploy.Artifact, 4)
	for i := range pairs {
		pairs[i] = bundleArtifact(bundleA)
		pairs[i].Image = "wordpress:7.0-php8.3-apache@sha256:" + strings.Repeat(string(rune('a'+i)), 64)
		pairs[i].Digest = deploy.PairDigest(pairs[i].Image, pairs[i].Bundle)
	}
	var wg sync.WaitGroup
	errs := make([]error, len(pairs))
	for i, a := range pairs {
		wg.Go(func() { _, errs[i] = w.backend.Stage(context.Background(), a) })
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("Stage %d: %v", i, err)
		}
	}
	if up := w.uploads(); len(up) != 1 {
		t.Fatalf("%d uploads, want the bundle loaded once", len(up))
	}
	if len(w.daemon.Volumes()) != 1 {
		t.Fatalf("volumes = %v", w.daemon.Volumes())
	}
}

func TestAnImageOnlyArtifactGetsNoBundleVolume(t *testing.T) {
	t.Parallel()
	w := bundleWorld(t, newBundles(), true)
	w.stage(digestA)
	if len(w.daemon.Volumes()) != 0 || len(w.uploads()) != 0 || len(w.daemon.Created()) != 1 {
		t.Fatalf("an image-only artifact touched bundles: %v", w.changes())
	}
}

// noBundleLeft fails when a failed load left a loader, a volume or a
// revision behind.
func noBundleLeft(t *testing.T, w *world) {
	t.Helper()
	if names := w.containerNames(); len(names) != 0 {
		t.Fatalf("containers left behind: %v", names)
	}
	if v := w.daemon.Volumes(); len(v) != 0 {
		t.Fatalf("volumes left behind: %v", v)
	}
}

func TestAFailedLoadLeavesNoPartOfTheBundle(t *testing.T) {
	t.Parallel()
	for _, filtered := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct", true: "through the deploy filter"}[filtered], func(t *testing.T) {
			t.Parallel()
			w := bundleWorld(t, newBundles(), filtered)
			w.daemon.FailArchives(true)
			if _, err := w.backend.Stage(context.Background(), bundleArtifact(bundleA)); err == nil {
				t.Fatal("a failed upload staged a revision")
			}
			noBundleLeft(t, w)
			w.daemon.FailArchives(false)
			if _, err := w.backend.Stage(context.Background(), bundleArtifact(bundleA)); err != nil {
				t.Fatalf("the next Stage did not load the bundle again: %v", err)
			}
		})
	}
}

func TestACancelledLoadStillRemovesItsLoader(t *testing.T) {
	t.Parallel()
	src := newBundles()
	w := bundleWorld(t, src, false)
	ctx, cancel := context.WithCancel(context.Background())
	// The caller gives up in the middle of the upload, before the stream ends.
	src.read = func() error { cancel(); return context.Canceled }
	if _, err := w.backend.Stage(ctx, bundleArtifact(bundleA)); err == nil {
		t.Fatal("a cancelled load staged a revision")
	}
	noBundleLeft(t, w)
}

func TestAnUnreadableBundleCreatesNothing(t *testing.T) {
	t.Parallel()
	src := newBundles()
	src.fail = errors.New("the bundle store is down")
	w := bundleWorld(t, src, false)
	if _, err := w.backend.Stage(context.Background(), bundleArtifact(bundleA)); !core.HasCode(err, core.CodeReleaseHold) {
		t.Fatalf("Stage = %v, want RELEASE_HOLD", err)
	}
	if changes := w.changes(); len(changes) != 0 {
		t.Fatalf("the daemon was changed: %v", changes)
	}
}

func TestAnUnfinishedLoadIsRedone(t *testing.T) {
	t.Parallel()
	w := bundleWorld(t, newBundles(), true)
	// A load that was cut off leaves its loader and a labelled volume.
	w.daemon.AddVolume(bundleVolumeA, map[string]string{compose.LabelFactory: string(factory), compose.LabelBundle: bundleA})
	w.daemon.AddContainer(loaderA, map[string]string{compose.LabelFactory: string(factory), compose.LabelLoader: "1", compose.LabelBundle: bundleA})
	if _, err := w.backend.Stage(context.Background(), bundleArtifact(bundleA)); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if up := w.uploads(); len(up) != 1 || !bytes.Equal(up[0], tarA) {
		t.Fatalf("the bundle was not loaded again: %d uploads", len(up))
	}
	if !slices.Contains(w.changes(), "DELETE /volumes/"+bundleVolumeA) {
		t.Fatalf("the partly loaded volume was not removed: %v", w.changes())
	}
}

func TestANameClashWithAnotherBundleIsAConflict(t *testing.T) {
	t.Parallel()
	cases := map[string]func(w *world){
		"a volume of another bundle": func(w *world) {
			w.daemon.AddVolume(bundleVolumeA, map[string]string{compose.LabelFactory: string(factory), compose.LabelBundle: "sha256:" + strings.Repeat("1", 12) + strings.Repeat("0", 52)})
		},
		"a volume of another factory": func(w *world) {
			w.daemon.AddVolume(bundleVolumeA, map[string]string{compose.LabelFactory: "other", compose.LabelBundle: bundleA})
		},
		"a container of the loader's name that is no loader": func(w *world) {
			w.daemon.AddContainer(loaderA, map[string]string{compose.LabelFactory: string(factory), compose.LabelUnit: "loader"})
		},
	}
	for name, prepare := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			w := bundleWorld(t, newBundles(), false)
			prepare(w)
			if _, err := w.backend.Stage(context.Background(), bundleArtifact(bundleA)); !core.HasCode(err, core.CodeConflict) {
				t.Fatalf("Stage = %v, want CONFLICT", err)
			}
			if changes := w.changes(); len(changes) != 0 {
				t.Fatalf("the daemon was changed: %v", changes)
			}
		})
	}
}

func TestABundleArtifactNeedsABundleTargetAndSource(t *testing.T) {
	t.Parallel()
	w := newWorld(t, worldOptions{})
	if _, err := w.backend.Stage(context.Background(), bundleArtifact(bundleA)); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Fatalf("Stage = %v, want INVALID_ARGUMENT", err)
	}
	if len(w.daemon.Requests()) != 0 {
		t.Fatal("a refused bundle artifact reached the daemon")
	}
	// A bundle artifact whose digest does not name its pair is refused too.
	bw := bundleWorld(t, newBundles(), false)
	forged := bundleArtifact(bundleA)
	forged.Bundle = bundleB
	if _, err := bw.backend.Stage(context.Background(), forged); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Fatalf("Stage of a forged pair = %v, want INVALID_ARGUMENT", err)
	}
	if len(bw.daemon.Requests()) != 0 {
		t.Fatal("a forged pair reached the daemon")
	}
}

func TestBundleSettingsAreChecked(t *testing.T) {
	t.Parallel()
	for name, mutate := range map[string]func(*compose.Config){
		"a uid alone":               func(c *compose.Config) { c.User = "33" },
		"root":                      func(c *compose.Config) { c.User = "0:0" },
		"the root group":            func(c *compose.Config) { c.User = "33:0" },
		"a user name":               func(c *compose.Config) { c.User = "www-data" },
		"a relative bundle target":  func(c *compose.Config) { c.BundleTarget = "var/www/html" },
		"the root as bundle target": func(c *compose.Config) { c.BundleTarget = "/" },
		"an unclean bundle target":  func(c *compose.Config) { c.BundleTarget = "/var/www/../html" },
		"the loader's target":       func(c *compose.Config) { c.BundleTarget = compose.LoaderTarget },
		"a data volume named as a bundle": func(c *compose.Config) {
			c.Volumes = []compose.VolumeMount{{Name: string(factory) + "-bundle-data", Target: "/data"}}
		},
		"a data volume at the bundle target": func(c *compose.Config) {
			c.BundleTarget = docRoot
			c.Volumes = []compose.VolumeMount{{Name: "data", Target: docRoot}}
		},
	} {
		cfg := validConfig(brokenEdge{})
		mutate(&cfg)
		if _, err := compose.New(cfg); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
