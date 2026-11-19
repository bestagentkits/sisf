package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/bestagentkits/sisf/internal/supervisor/release"
	"github.com/bestagentkits/sisf/pkg/appbundle"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/deploy"
	"github.com/bestagentkits/sisf/pkg/managedapp"
)

func shopSpecPath(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "..", "examples", "woocommerce-app", managedapp.FileName))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func shopFlags(t *testing.T) *lifecycleFlags {
	t.Helper()
	l := &lifecycleFlags{deploy: "compose", deployEndpoint: "http://127.0.0.1:1", deployNetwork: "shop_default", appPort: 8080}
	l.app.spec = shopSpecPath(t)
	l.app.binding = managedapp.Binding{Volumes: map[string]string{"uploads": "shop-uploads"}, Secrets: "shop-secrets"}
	return l
}

func emptyTrustVolume(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "trust")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

// The Compose backend of a managed application starts the Spec's revision
// over bundles from the trust volume, and reaches it where the host says.
func TestAManagedApplicationsBackendRunsTheSpecsRevision(t *testing.T) {
	t.Parallel()
	l := shopFlags(t)
	l.app.hostSuffix = ".orb.local"
	cfg, err := l.composeConfig("acme-shop", nil, emptyTrustVolume(t), core.SystemClock{}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AppPort != 80 || cfg.User != "33:33" || cfg.BundleTarget != "/var/www/html" || len(cfg.Volumes) != 2 || !cfg.Volumes[1].ReadOnly {
		t.Fatalf("%+v", cfg)
	}
	if _, ok := cfg.BundleSource.(appbundle.Store); !ok {
		t.Fatalf("bundle source %T", cfg.BundleSource)
	}
	if got := cfg.Resolve("acme-shop-app-0123456789ab"); got != "http://acme-shop-app-0123456789ab.orb.local:80" {
		t.Fatalf("resolve = %s", got)
	}
	observer, err := l.artifactObserver(false, emptyTrustVolume(t))
	if _, ok := observer.(appbundle.PairObserver); err != nil || !ok {
		t.Fatalf("observer %T, %v", observer, err)
	}
}

// A managed application is refused where it cannot be released as its
// Spec says: by the local backend, with a relative Spec, with a binding but
// no Spec, or with a second synthetic suite beside its checks.
func TestAManagedApplicationIsRefusedWhereItCannotBeReleased(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*lifecycleFlags){
		"the local backend":      func(l *lifecycleFlags) { l.deploy = "local" },
		"a relative Spec":        func(l *lifecycleFlags) { l.app.spec = "examples/woocommerce-app/" + managedapp.FileName },
		"a binding but no Spec":  func(l *lifecycleFlags) { l.app.spec = "" },
		"an unbound data volume": func(l *lifecycleFlags) { l.app.binding.Volumes = nil },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			l := shopFlags(t)
			change(l)
			if _, err := l.app.load(l.deploy); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	l := shopFlags(t)
	l.syntheticProgram = "/usr/bin/true"
	if _, err := l.syntheticSuite(nil, slog.New(slog.DiscardHandler)); err == nil {
		t.Fatal("a synthetic program was taken beside the Spec's checks")
	}
}

// shop answers the shop's read-only checks, fails the paths in broken, and
// records every request it gets.
type shop struct {
	mu     sync.Mutex
	broken map[string]bool
	seen   []string
}

func (s *shop) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.seen = append(s.seen, r.Method+" "+r.URL.Path)
	broken := s.broken[r.URL.Path]
	s.mu.Unlock()
	if broken {
		http.Error(w, "broken", http.StatusInternalServerError)
		return
	}
	switch r.URL.Path {
	case "/healthz":
		_, _ = w.Write([]byte(`{"status":"ok","database":"ok"}`))
	case "/":
		_, _ = w.Write([]byte("<html></html>"))
	case "/wp-json/wc/store/v1/products":
		_, _ = w.Write([]byte(`[{"id":1,"prices":{}}]`))
	default:
		http.NotFound(w, r)
	}
}

func shopChecks(t *testing.T, s *shop) release.SyntheticSuite {
	t.Helper()
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	l := shopFlags(t)
	suite, err := l.syntheticSuite(previewBackend{url: srv.URL}, slog.New(slog.DiscardHandler))
	if err != nil || suite == nil {
		t.Fatalf("%v, %v", suite, err)
	}
	return suite
}

// A low-traffic release of the shop is judged by its read-only checks:
// health, home and catalogue pass it, and the purchase, which writes, is
// never sent to the revision.
func TestTheShopsReadOnlyChecksJudgeALowTrafficRelease(t *testing.T) {
	t.Parallel()
	s := &shop{}
	res, err := shopChecks(t, s).Run(context.Background(), deploy.Revision{Unit: "app"})
	if err != nil || !res.Passed {
		t.Fatalf("%+v, %v", res, err)
	}
	for _, req := range s.seen {
		if !strings.HasPrefix(req, "GET ") || strings.Contains(req, "/cart") || strings.Contains(req, "/checkout") {
			t.Fatalf("a check sent %s to the live revision", req)
		}
	}
}

func TestAFailingReadOnlyCheckFailsTheRelease(t *testing.T) {
	t.Parallel()
	s := &shop{broken: map[string]bool{"/wp-json/wc/store/v1/products": true}}
	res, err := shopChecks(t, s).Run(context.Background(), deploy.Revision{Unit: "app"})
	if err != nil || res.Passed || !strings.Contains(res.Detail, "catalogue") {
		t.Fatalf("%+v, %v", res, err)
	}
}
