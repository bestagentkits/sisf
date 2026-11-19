package stages

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/bestagentkits/sisf/internal/testkit/fakegithub"
	"github.com/bestagentkits/sisf/pkg/appbundle"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/deploy"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/managedapp"
)

// testerFake is the sandbox's test phase: it passes or fails, or cannot run.
type testerFake struct {
	passed bool
	err    error
	ran    int
}

func (f *testerFake) Name() string { return "docker" }

func (f *testerFake) TestCandidate(context.Context, ImageRequest) (bool, string, error) {
	f.ran++
	return f.passed, "sandbox test phase exited with status 0", f.err
}

// archives serves the archives the lockfile pins, or fails every download.
type archives struct {
	files map[string][]byte
	err   error
}

func (a archives) Fetch(_ context.Context, url string) (io.ReadCloser, error) {
	if a.err != nil {
		return nil, a.err
	}
	data, ok := a.files[url]
	if !ok {
		return nil, errors.New("404 " + url)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func coreZip(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{"wordpress/index.php": "<?php // core", "wordpress/wp-includes/version.php": "<?php $v='7.0.4';"} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// shopRepo is a candidate commit of the shop on the fake GitHub, the files
// it holds, and the builder of its bundles.
type shopRepo struct {
	fake   *fakegithub.Server
	commit core.CommitSHA
	files  map[string]string
	b      BundleImages
	tests  *testerFake
}

const coreURL = "https://downloads.wordpress.org/release/wordpress-7.0.4.zip"

func newShopRepo(t *testing.T, fetch archives, extra map[string]string) *shopRepo {
	t.Helper()
	zipped := coreZip(t)
	sum := sha256.Sum256(zipped)
	if fetch.files == nil && fetch.err == nil {
		fetch.files = map[string][]byte{coreURL: zipped}
	}
	files := map[string]string{
		"content.lock":                     "core wordpress 7.0.4 " + hex.EncodeToString(sum[:]) + "\n",
		"plugins/sisf-sandbox-pay/pay.php": "<?php // sandbox pay",
		"wp-config.php":                    "<?php // config",
		"README.md":                        "not in the bundle",
	}
	for k, v := range extra {
		files[k] = v
	}
	fake := fakegithub.New(fakegithub.WithToken(testRepoToken))
	if err := fake.AddRepo(testRepoName); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(fakegithub.NewGitData(fake))
	t.Cleanup(srv.Close)
	client, err := github.NewRESTClient(github.ClientConfig{BaseURL: srv.URL,
		Token: github.StaticToken(core.NewSecretValue([]byte(testRepoToken))), Clock: noWait{}})
	if err != nil {
		t.Fatal(err)
	}
	sha, err := fake.Commit(testRepoName, files)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := managedapp.Load(filepath.Join("..", "..", "..", "examples", "woocommerce-app", managedapp.FileName))
	if err != nil {
		t.Fatal(err)
	}
	spec.Bundle.Sources = []managedapp.Source{{From: "plugins", To: "wp-content/plugins"}, {From: "wp-config.php", To: "wp-config.php"}}
	spec.Bundle.Prune = nil
	tests := &testerFake{passed: true}
	owner, name, _ := bytes.Cut([]byte(testRepoName), []byte("/"))
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "store"), 0o700); err != nil {
		t.Fatal(err)
	}
	return &shopRepo{fake: fake, commit: core.CommitSHA(sha), files: files, tests: tests, b: BundleImages{
		Tests: tests, GitHub: client, Repo: github.RepoRef{Owner: string(owner), Name: string(name)}, Spec: spec,
		Store: appbundle.Store{Dir: filepath.Join(dir, "store")}, Fetcher: fetch, CacheDir: filepath.Join(dir, "cache"), WorkDir: dir,
	}}
}

func (s *shopRepo) build(t *testing.T) (ImageResult, error) {
	t.Helper()
	return s.b.BuildImage(context.Background(), ImageRequest{FactoryID: "acme", RunID: "run-1", Unit: "app", Commit: s.commit, Untrusted: true})
}

// The supervisor's bundle of a candidate is the one `sisf app bundle`
// makes of the same files: same digest, kept in the store, released as
// the pair with the Spec's image, and judged by the sandbox's tests.
func TestABundleBuildIsTheBundleOfTheCandidatesCommit(t *testing.T) {
	t.Parallel()
	s := newShopRepo(t, archives{}, nil)
	got, err := s.build(t)
	if err != nil {
		t.Fatal(err)
	}
	local := t.TempDir()
	for p, body := range s.files {
		if err := os.MkdirAll(filepath.Join(local, filepath.Dir(p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(local, p), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := appbundle.Assemble(context.Background(), appbundle.SpecInput(s.b.Spec, local, s.b.Fetcher, t.TempDir(), filepath.Join(t.TempDir(), "b.tar")))
	if err != nil {
		t.Fatal(err)
	}
	if got.Bundle != want.Digest || got.Image != s.b.Spec.Runtime.Image || got.Digest != core.Digest(deploy.PairDigest(got.Image, got.Bundle)) {
		t.Fatalf("result %+v, want the bundle %s with the Spec's image", got, want.Digest)
	}
	if !got.TestsPassed || s.tests.ran != 1 {
		t.Fatalf("tests passed %v after %d runs", got.TestsPassed, s.tests.ran)
	}
	if _, err := s.b.Store.Measure(got.Bundle); err != nil {
		t.Fatalf("the store does not hold the bundle: %v", err)
	}
	s.tests.passed = false
	reads := &countedFiles{CommitFiles: s.b.GitHub}
	s.b.GitHub = reads
	_, err = s.build(t)
	if r, ok := RefusalOf(err); !ok || r.Reason != RefusalBuildFailed || reads.n != 0 {
		t.Fatalf("a failing suite = %v after %d reads of the commit; want refused as a failed build before any read", err, reads.n)
	}
}

// What the candidate's content makes impossible is refused as its fault;
// an outage of the sandbox or of an archive's source spends no attempt.
func TestABundleBuildTellsTheCandidatesFaultFromAnOutage(t *testing.T) {
	t.Parallel()
	refused := func(t *testing.T, err error) {
		t.Helper()
		var r *PatchRefusal
		if !errors.As(err, &r) || r.Reason != RefusalBundleRejected {
			t.Fatalf("err = %v, want the bundle refused as the candidate's fault", err)
		}
	}
	outage := func(t *testing.T, err error) {
		t.Helper()
		var r *PatchRefusal
		if err == nil || errors.As(err, &r) {
			t.Fatalf("err = %v, want an outage", err)
		}
	}
	t.Run("a link among the sources", func(t *testing.T) {
		t.Parallel()
		s := newShopRepo(t, archives{}, map[string]string{"plugins/escape": "/etc/passwd"})
		if err := s.fake.SetEntryKind(testRepoName, string(s.commit), "plugins/escape", fakegithub.EntrySymlink); err != nil {
			t.Fatal(err)
		}
		_, err := s.build(t)
		refused(t, err)
	})
	t.Run("an archive that is not its pin", func(t *testing.T) {
		t.Parallel()
		_, err := newShopRepo(t, archives{files: map[string][]byte{coreURL: []byte("PK not the pinned core")}}, nil).build(t)
		refused(t, err)
	})
	t.Run("a lockfile that does not parse", func(t *testing.T) {
		t.Parallel()
		_, err := newShopRepo(t, archives{}, map[string]string{"content.lock": "core ../../etc 1 x\n"}).build(t)
		refused(t, err)
	})
	t.Run("a tree GitHub cannot list whole", func(t *testing.T) {
		t.Parallel()
		s := newShopRepo(t, archives{}, nil)
		s.fake.TruncateTrees()
		_, err := s.build(t)
		refused(t, err)
	})
	t.Run("the archive source is down", func(t *testing.T) {
		t.Parallel()
		_, err := newShopRepo(t, archives{err: errors.New("connection refused")}, nil).build(t)
		outage(t, err)
	})
	t.Run("the sandbox is down", func(t *testing.T) {
		t.Parallel()
		s := newShopRepo(t, archives{}, nil)
		s.tests.err = core.NewError(core.CodeUnavailable, "the Docker endpoint does not answer")
		_, err := s.build(t)
		outage(t, err)
	})
	t.Run("no sandbox", func(t *testing.T) {
		t.Parallel()
		s := newShopRepo(t, archives{}, nil)
		s.b.Tests = nil
		_, err := s.build(t)
		if !errors.Is(err, ErrConfiguration) {
			t.Fatalf("err = %v, want a configuration refusal", err)
		}
	})
}

// countedFiles counts the reads of the candidate's commit.
type countedFiles struct {
	CommitFiles
	n int
}

func (c *countedFiles) ListTree(ctx context.Context, repo github.RepoRef, commit core.CommitSHA) ([]github.TreeEntry, error) {
	c.n++
	return c.CommitFiles.ListTree(ctx, repo, commit)
}
