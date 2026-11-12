package stages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/testkit/fakegithub"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/execution"
	"github.com/bestagentkits/sisf/pkg/github"
)

const (
	testRepoName  = "acme/app"
	testRepoToken = "ghs_test_token"
	// editMain changes app/main.go, which the base holds.
	editMain = "--- a/app/main.go\n+++ b/app/main.go\n@@ -1 +1 @@\n-a\n+b\n"
)

// realGitHub is the GitHub client against a fake that serves the git data
// API, with a base commit holding app/main.go.
type realGitHub struct {
	fake   *fakegithub.Server
	data   *fakegithub.GitData
	client *github.RESTClient
	base   core.CommitSHA
}

// noWait is a clock whose waits end at once, so the client's own retries
// of a 5xx take no time.
type noWait struct{ core.SystemClock }

func (noWait) After(time.Duration) <-chan time.Time {
	c := make(chan time.Time, 1)
	c <- time.Now()
	return c
}

func newRealGitHub(t *testing.T) *realGitHub {
	t.Helper()
	fake := fakegithub.New(fakegithub.WithToken(testRepoToken))
	if err := fake.AddRepo(testRepoName); err != nil {
		t.Fatal(err)
	}
	data := fakegithub.NewGitData(fake)
	srv := httptest.NewServer(data)
	t.Cleanup(srv.Close)
	client, err := github.NewRESTClient(github.ClientConfig{BaseURL: srv.URL,
		Token: github.StaticToken(core.NewSecretValue([]byte(testRepoToken))), Clock: noWait{}})
	if err != nil {
		t.Fatal(err)
	}
	base, err := fake.Commit(testRepoName, map[string]string{"app/main.go": "a\n", "app/lib": "x\n"})
	if err != nil {
		t.Fatal(err)
	}
	return &realGitHub{fake: fake, data: data, client: client, base: core.CommitSHA(base)}
}

func (g *realGitHub) build(t *testing.T, diff string) (Built, error) {
	t.Helper()
	b := &CandidateBuilder{FactoryID: "f1", Repo: github.RepoRef{Owner: "acme", Name: "app"}, Git: g.client,
		Images: SimulatedImages{}, Units: DefaultUnitRouter(), Clock: core.SystemClock{}}
	return b.Build(context.Background(), BuildRequest{RunID: "run-1", Attempt: 1, Base: g.base, Diff: diff})
}

// outage marks a row whose refusal is not the patch's fault.
const outage RefusalReason = ""

// The decision for every step and status GitHub can answer the builder
// with. The table is written out here, apart from the one the builder
// uses, so a change of the builder's table shows as a failing row.
var wantRepositoryOutcomes = []struct {
	step, route, method string
	status              int
	want                RefusalReason
}{
	// Reading a base file the patch changes.
	{github.StepReadFile, "/contents/", http.MethodGet, 400, outage},
	{github.StepReadFile, "/contents/", http.MethodGet, 401, outage},
	{github.StepReadFile, "/contents/", http.MethodGet, 403, outage},
	{github.StepReadFile, "/contents/", http.MethodGet, 404, RefusalDoesNotApply},
	{github.StepReadFile, "/contents/", http.MethodGet, 409, outage},
	{github.StepReadFile, "/contents/", http.MethodGet, 410, outage},
	{github.StepReadFile, "/contents/", http.MethodGet, 422, outage},
	{github.StepReadFile, "/contents/", http.MethodGet, 429, outage},
	{github.StepReadFile, "/contents/", http.MethodGet, 500, outage},
	{github.StepReadFile, "/contents/", http.MethodGet, 503, outage},
	// Reading the base commit, the factory's own policy commit.
	{github.StepBaseCommit, "/git/commits/", http.MethodGet, 400, outage},
	{github.StepBaseCommit, "/git/commits/", http.MethodGet, 401, outage},
	{github.StepBaseCommit, "/git/commits/", http.MethodGet, 403, outage},
	{github.StepBaseCommit, "/git/commits/", http.MethodGet, 404, outage},
	{github.StepBaseCommit, "/git/commits/", http.MethodGet, 409, outage},
	{github.StepBaseCommit, "/git/commits/", http.MethodGet, 410, outage},
	{github.StepBaseCommit, "/git/commits/", http.MethodGet, 422, outage},
	{github.StepBaseCommit, "/git/commits/", http.MethodGet, 429, outage},
	{github.StepBaseCommit, "/git/commits/", http.MethodGet, 500, outage},
	{github.StepBaseCommit, "/git/commits/", http.MethodGet, 503, outage},
	// Writing the tree of the patch's files.
	{github.StepTree, "/git/trees", http.MethodPost, 400, outage},
	{github.StepTree, "/git/trees", http.MethodPost, 401, outage},
	{github.StepTree, "/git/trees", http.MethodPost, 403, outage},
	{github.StepTree, "/git/trees", http.MethodPost, 404, outage},
	{github.StepTree, "/git/trees", http.MethodPost, 409, outage},
	{github.StepTree, "/git/trees", http.MethodPost, 410, outage},
	{github.StepTree, "/git/trees", http.MethodPost, 413, RefusalCommitTooLarge},
	{github.StepTree, "/git/trees", http.MethodPost, 422, RefusalPathRejected},
	{github.StepTree, "/git/trees", http.MethodPost, 429, outage},
	{github.StepTree, "/git/trees", http.MethodPost, 500, outage},
	{github.StepTree, "/git/trees", http.MethodPost, 503, outage},
	// Writing the commit of that tree.
	{github.StepCommit, "/git/commits", http.MethodPost, 400, outage},
	{github.StepCommit, "/git/commits", http.MethodPost, 401, outage},
	{github.StepCommit, "/git/commits", http.MethodPost, 403, outage},
	{github.StepCommit, "/git/commits", http.MethodPost, 404, outage},
	{github.StepCommit, "/git/commits", http.MethodPost, 409, outage},
	{github.StepCommit, "/git/commits", http.MethodPost, 410, outage},
	{github.StepCommit, "/git/commits", http.MethodPost, 422, outage},
	{github.StepCommit, "/git/commits", http.MethodPost, 429, outage},
	{github.StepCommit, "/git/commits", http.MethodPost, 500, outage},
	{github.StepCommit, "/git/commits", http.MethodPost, 503, outage},
	// Pointing the candidate branch at the commit.
	{github.StepBranch, "/git/refs", http.MethodPost, 400, outage},
	{github.StepBranch, "/git/refs", http.MethodPost, 401, outage},
	{github.StepBranch, "/git/refs", http.MethodPost, 403, outage},
	{github.StepBranch, "/git/refs", http.MethodPost, 404, outage},
	{github.StepBranch, "/git/refs", http.MethodPost, 409, outage},
	{github.StepBranch, "/git/refs", http.MethodPost, 410, outage},
	{github.StepBranch, "/git/refs", http.MethodPost, 422, outage},
	{github.StepBranch, "/git/refs", http.MethodPost, 429, outage},
	{github.StepBranch, "/git/refs", http.MethodPost, 500, outage},
	{github.StepBranch, "/git/refs", http.MethodPost, 503, outage},
}

// Each row is driven through the real GitHub client against the fake,
// which refuses exactly that step with exactly that status.
func TestEachGitHubStatusAtEachStepIsDecidedExplicitly(t *testing.T) {
	t.Parallel()
	for _, row := range wantRepositoryOutcomes {
		name := row.step + " " + strconv.Itoa(row.status)
		g := newRealGitHub(t)
		g.data.FailOn(row.method, row.route, row.status, "refused")
		if row.status >= 500 || row.status == 429 {
			// The client retries these itself; refuse every try.
			for range 8 {
				g.data.FailOn(row.method, row.route, row.status, "refused")
			}
		}
		_, err := g.build(t, editMain)
		if err == nil {
			t.Errorf("%s: the build succeeded", name)
			continue
		}
		r, refused := RefusalOf(err)
		switch {
		case row.want == outage && refused:
			t.Errorf("%s: refused as %s, want an outage", name, r.Reason)
		case row.want != outage && (!refused || r.Reason != row.want):
			t.Errorf("%s: %v, want the refusal %s", name, err, row.want)
		}
		if got := repositoryOutcome(row.step, row.status); got != row.want && (row.status != 404 || row.step != github.StepReadFile) {
			t.Errorf("%s: the table says %q, want %q", name, got, row.want)
		}
	}
	// A status the table does not name is an outage, never the patch's.
	if got := repositoryOutcome(github.StepTree, 418); got != outage {
		t.Errorf("an unknown status: %q", got)
	}
	if got := repositoryOutcome("", 422); got != outage {
		t.Errorf("an unknown step: %q", got)
	}
}

// A file the base lacks is the patch's fault only when the base itself can
// be read: a repository that cannot be seen is the configuration's.
func TestAMissingBaseFileIsThePatchsFaultOnlyWhenTheBaseCanBeRead(t *testing.T) {
	t.Parallel()
	g := newRealGitHub(t)
	_, err := g.build(t, "--- a/app/gone.go\n+++ b/app/gone.go\n@@ -1 +1 @@\n-a\n+b\n")
	if r, ok := RefusalOf(err); !ok || r.Reason != RefusalDoesNotApply {
		t.Fatalf("a file the base lacks: %v", err)
	}
	g = newRealGitHub(t)
	g.data.FailOn(http.MethodGet, "/contents/", 404, "Not Found")
	g.data.FailOn(http.MethodGet, "/git/commits/", 404, "Not Found")
	if _, err := g.build(t, editMain); err == nil || !core.HasCode(err, core.CodeNotFound) {
		t.Fatalf("a repository that cannot be seen: %v", err)
	} else if _, refused := RefusalOf(err); refused {
		t.Fatalf("a repository that cannot be seen was charged to the patch: %v", err)
	}
}

// What the patch's content makes GitHub refuse is the patch's fault: a
// base file it edits that is too large for the contents API, a directory
// or a submodule, and a path no commit can hold. The last is refused
// before anything is written.
func TestWhatThePatchsContentMakesGitHubRefuseIsThePatchsFault(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		kind, diff string
		want       RefusalReason
	}{
		"a base file over 1 MB": {fakegithub.EntryLargeFile, editMain, RefusalBaseFileTooLarge},
		"a directory":           {fakegithub.EntryDirectory, "--- a/app/lib\n+++ b/app/lib\n@@ -1 +1 @@\n-x\n+y\n", RefusalNotARegularFile},
		"a submodule":           {fakegithub.EntrySubmodule, "--- a/app/lib\n+++ b/app/lib\n@@ -1 +1 @@\n-x\n+y\n", RefusalNotARegularFile},
	} {
		g := newRealGitHub(t)
		path := "app/main.go"
		if c.kind != fakegithub.EntryLargeFile {
			path = "app/lib"
		}
		if err := g.fake.SetEntryKind(testRepoName, string(g.base), path, c.kind); err != nil {
			t.Fatal(err)
		}
		_, err := g.build(t, c.diff)
		if r, ok := RefusalOf(err); !ok || r.Reason != c.want {
			t.Errorf("%s: %v, want %s", name, err, c.want)
		}
		if g.data.Writes() != 0 {
			t.Errorf("%s: %d git data writes", name, g.data.Writes())
		}
	}
	for _, p := range []string{".git/config", "app/.git/hooks/post-checkout", "app/.GIT/x", "app/.Git"} {
		g := newRealGitHub(t)
		_, err := g.build(t, "--- /dev/null\n+++ b/"+p+"\n@@ -0,0 +1 @@\n+x\n")
		if r, ok := RefusalOf(err); !ok || r.Reason != RefusalPathRejected || r.Path != p {
			t.Errorf("adding %s: %v", p, err)
		}
		if g.data.Writes() != 0 || len(g.fake.Requests()) != 0 {
			t.Errorf("adding %s: %d writes and %d requests", p, g.data.Writes(), len(g.fake.Requests()))
		}
	}
}

// A build command that exits 0 without reporting its image and digest is
// the candidate's doing: the candidate controls what the command prints.
func TestABuildThatReportsNoImageIsThePatchsFault(t *testing.T) {
	t.Parallel()
	req := ImageRequest{RunID: "run-1", Unit: "app", Commit: baseSHA, Untrusted: true}
	for name, out := range map[string]string{
		"nothing":         "",
		"no digest":       "sisf-image: registry.local/app@sha256:01\n",
		"no image":        "sisf-image-digest: sha256:0000000000000000000000000000000000000000000000000000000000000001\n",
		"a broken digest": "sisf-image-digest: sha256:xyz\nsisf-image: registry.local/app\n",
	} {
		_, err := SandboxImages{Exec: scriptedSandbox{result: execution.ExecResult{Stdout: []byte(out)}}, Argv: []string{"build"},
			Timeout: time.Minute}.BuildImage(context.Background(), req)
		if r, ok := RefusalOf(err); !ok || r.Reason != RefusalNoImageReported {
			t.Errorf("%s: %v", name, err)
		}
	}
	// The same holds for any image builder the builder is given.
	repo := &recordingRepo{files: map[string]string{"app/main.go": "a\n"}}
	b := newTestBuilder(repo)
	b.Images = noDigestImages{}
	if _, err := b.Build(context.Background(), BuildRequest{RunID: "run-1", Attempt: 1, Base: baseSHA, Diff: editMain}); err == nil {
		t.Fatal("a build with no digest succeeded")
	} else if r, ok := RefusalOf(err); !ok || r.Reason != RefusalNoImageReported {
		t.Errorf("an image builder with no digest: %v", err)
	}
}

type noDigestImages struct{}

func (noDigestImages) Name() string { return "no-digest" }
func (noDigestImages) BuildImage(context.Context, ImageRequest) (ImageResult, error) {
	return ImageResult{Image: "x"}, nil
}
