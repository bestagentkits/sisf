package synthetic_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bestagentkits/sisf/examples/hello-app/synthetic"
)

const revision = "sha256:" + "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"

// good is a minimal app that behaves as the suite expects; a test breaks
// one thing of it at a time.
type good struct {
	revision   string
	pageStatus int
	frozen     bool // the visit count never moves
	noMetrics  bool
	acceptPost bool
	visits     atomic.Int64
}

func newGood() *good { return &good{revision: revision, pageStatus: http.StatusOK} }

func (g *good) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/healthz":
		_, _ = fmt.Fprintf(w, `{"status":"ok","revision":%q}`, g.revision)
	case r.URL.Path == "/metrics" && !g.noMetrics:
		_, _ = fmt.Fprint(w, "hello_up 1\n")
	case r.URL.Path == "/" && r.Method == http.MethodGet:
		if g.pageStatus != http.StatusOK {
			w.WriteHeader(g.pageStatus)
			return
		}
		n := g.visits.Add(1)
		if g.frozen {
			n = 7
		}
		_, _ = fmt.Fprintf(w, "visit %d\n", n)
	case r.URL.Path == "/" && g.acceptPost:
		w.WriteHeader(http.StatusOK)
	case r.URL.Path == "/":
		w.WriteHeader(http.StatusMethodNotAllowed)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func run(t *testing.T, g *good) synthetic.Report {
	t.Helper()
	srv := httptest.NewServer(g)
	t.Cleanup(srv.Close)
	return synthetic.Run(context.Background(), synthetic.Target{BaseURL: srv.URL, ExpectedRevision: revision})
}

func TestAHealthyInstanceOfTheExpectedRevisionPasses(t *testing.T) {
	t.Parallel()
	if report := run(t, newGood()); !report.Passed() {
		t.Fatalf("failures: %+v", report.Failures())
	}
}

func TestEachBrokenFlowFailsItsOwnCheck(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		break_ func(*good)
		want   string
	}{
		"wrong revision":  {func(g *good) { g.revision = "sha256:other" }, "health"},
		"page fails":      {func(g *good) { g.pageStatus = 500 }, "page"},
		"count is stuck":  {func(g *good) { g.frozen = true }, "page"},
		"metrics missing": {func(g *good) { g.noMetrics = true }, "metrics"},
		"writes accepted": {func(g *good) { g.acceptPost = true }, "writes"},
	} {
		g := newGood()
		tc.break_(g)
		report := run(t, g)
		failures := report.Failures()
		if report.Passed() || len(failures) != 1 || !strings.Contains(failures[0].Name, tc.want) {
			t.Errorf("%s: failures = %+v, want only the %s check", name, failures, tc.want)
		}
	}
}

func TestAnUnreachableOrMalformedTargetFailsEveryCheck(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(newGood())
	srv.Close()
	for _, base := range []string{srv.URL, "", "ftp://app", "http://"} {
		report := synthetic.Run(context.Background(), synthetic.Target{BaseURL: base, ExpectedRevision: revision})
		if report.Passed() || len(report.Failures()) != len(report.Results) {
			t.Errorf("target %q: failures = %d of %d, want all", base, len(report.Failures()), len(report.Results))
		}
	}
}

func TestWithoutAnExpectedRevisionTheInstanceCannotBeIdentified(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(newGood())
	t.Cleanup(srv.Close)
	if synthetic.Run(context.Background(), synthetic.Target{BaseURL: srv.URL}).Passed() {
		t.Fatal("the suite passed without knowing which revision it looked at")
	}
}

func TestAnEmptyReportDidNotPass(t *testing.T) {
	t.Parallel()
	if (synthetic.Report{}).Passed() {
		t.Fatal("an empty report passed")
	}
}
