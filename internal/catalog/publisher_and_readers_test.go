package catalog

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
)

const baseSHA core.CommitSHA = "0123456789abcdef0123456789abcdef01234567"

var testRepo = github.RepoRef{Owner: "acme", Name: "hello"}

// fakeRepo is a repository with one base branch: it serves the files at
// its head and records the branches and pull requests made against it.
type fakeRepo struct {
	files   map[string]string
	commits []Commit
	pulls   []github.NewPullRequest
}

func (r *fakeRepo) ReadFile(_ context.Context, repo github.RepoRef, rev core.CommitSHA, path string) ([]byte, error) {
	if repo != testRepo || rev != baseSHA {
		return nil, core.NewError(core.CodeInvalidArgument, "read at an unexpected revision")
	}
	content, ok := r.files[path]
	if !ok {
		return nil, core.NewError(core.CodeNotFound, path+" not found")
	}
	return []byte(content), nil
}

func (r *fakeRepo) Head(_ context.Context, _ github.RepoRef, branch string) (core.CommitSHA, error) {
	if branch != "main" {
		return "", core.NewError(core.CodeNotFound, "no branch "+branch)
	}
	return baseSHA, nil
}

func (r *fakeRepo) Commit(_ context.Context, _ github.RepoRef, c Commit) (core.CommitSHA, error) {
	r.commits = append(r.commits, c)
	return "89abcdef0123456789abcdef0123456789abcdef", nil
}

func (r *fakeRepo) CreatePullRequest(_ context.Context, _ github.RepoRef, pr github.NewPullRequest) (github.PullRequest, error) {
	r.pulls = append(r.pulls, pr)
	return github.PullRequest{Repo: testRepo, Number: 41 + len(r.pulls)}, nil
}

var publisherActor = core.Actor{ID: "service:publisher", Kind: core.ActorService, Roles: []core.Role{core.RolePublisher}}

func newPublisher(t *testing.T, repo *fakeRepo) *Publisher {
	t.Helper()
	p, err := New(Config{Repo: testRepo, BaseBranch: "main", Actor: publisherActor, Files: repo, Branches: repo, Pulls: repo})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

func TestAVerifiedReleaseIsRecordedThroughAPullRequestByThePublisher(t *testing.T) {
	t.Parallel()
	repo := &fakeRepo{files: map[string]string{PathFeatures: featuresMarkdown, PathFeaturesYAML: featuresYAML}}
	ref, err := newPublisher(t, repo).Publish(context.Background(), entry("run-7"))
	if err != nil || ref != "acme/hello#pull/42" {
		t.Fatalf("Publish = %q, %v", ref, err)
	}
	if len(repo.commits) != 1 || len(repo.pulls) != 1 {
		t.Fatalf("commits %d pulls %d, want one of each", len(repo.commits), len(repo.pulls))
	}
	c, pr := repo.commits[0], repo.pulls[0]
	if c.Branch != "sisf/release/run-7" || c.Base != baseSHA || c.Author != publisherActor.ID || pr.BaseRef != "main" || pr.HeadRef != c.Branch {
		t.Fatalf("commit %+v pull %+v", c, pr)
	}
	if len(c.Files) != 3 || !strings.Contains(string(c.Files[PathFeatures]), "| Greeting page | Brief §1 | built and released |") ||
		!strings.Contains(string(c.Files[PathFeaturesYAML]), "status: built and released") ||
		!strings.Contains(string(c.Files[PathChangelog]), "<!-- sisf:release run-7 -->") {
		t.Fatalf("files %v", c.Files)
	}
}

func TestAReleaseTheChangelogAlreadyRecordsOpensNoPullRequest(t *testing.T) {
	t.Parallel()
	recorded, _ := AddChangelogEntry(nil, entry("run-7"))
	repo := &fakeRepo{files: map[string]string{PathChangelog: string(recorded), PathFeatures: featuresMarkdown}}
	ref, err := newPublisher(t, repo).Publish(context.Background(), entry("run-7"))
	if err != nil || ref != "" || len(repo.commits)+len(repo.pulls) != 0 {
		t.Fatalf("Publish = %q, %v with %d commits", ref, err, len(repo.commits))
	}
}

func TestPublishingAnUnlistedFeatureOrWithoutACatalogIsRefused(t *testing.T) {
	t.Parallel()
	for name, files := range map[string]map[string]string{
		"unlisted feature": {PathFeatures: featuresMarkdown},
		"no catalog":       {},
	} {
		repo := &fakeRepo{files: files}
		e := entry("run-7")
		if name == "unlisted feature" {
			e.Feature = "Payments"
		}
		if _, err := newPublisher(t, repo).Publish(context.Background(), e); !core.HasCode(err, core.CodeApprovalRequired) {
			t.Errorf("%s: Publish = %v, want APPROVAL_REQUIRED", name, err)
		}
		if len(repo.commits)+len(repo.pulls) != 0 {
			t.Errorf("%s: a refused release still wrote", name)
		}
	}
}

func TestOnlyThePublisherServiceIdentityMayPublish(t *testing.T) {
	t.Parallel()
	repo := &fakeRepo{}
	for name, actor := range map[string]core.Actor{
		"an Owner's session":         {ID: "user:owner", Kind: core.ActorHuman, Roles: []core.Role{core.RoleOwner, core.RolePublisher}},
		"an agent":                   {ID: "agent:coder-1", Kind: core.ActorAgent, Roles: []core.Role{core.RolePublisher}},
		"a service without the role": {ID: "service:control", Kind: core.ActorService},
	} {
		_, err := New(Config{Repo: testRepo, BaseBranch: "main", Actor: actor, Files: repo, Branches: repo, Pulls: repo})
		if !core.HasCode(err, core.CodePermissionDenied) {
			t.Errorf("%s: New = %v, want PERMISSION_DENIED", name, err)
		}
	}
	if _, err := New(Config{Repo: testRepo, BaseBranch: "main; rm", Actor: publisherActor, Files: repo, Branches: repo, Pulls: repo}); err == nil {
		t.Error("a malformed base branch was accepted")
	}
}

func TestTheCatalogIsReadFromWhicheverFormTheRepositoryHas(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	md, err := ReadFeatures(ctx, &fakeRepo{files: map[string]string{PathFeatures: featuresMarkdown}}, testRepo, baseSHA)
	if err != nil || len(md) != 2 || md[0] != (Feature{Name: "Greeting page", Scope: "Brief §1", Status: "designed, not yet built"}) {
		t.Fatalf("markdown catalog = %+v, %v", md, err)
	}
	yml, err := ReadFeatures(ctx, &fakeRepo{files: map[string]string{PathFeatures: featuresMarkdown, PathFeaturesYAML: featuresYAML}}, testRepo, baseSHA)
	if err != nil || len(yml) != 2 || yml[0].Status != "designed" || yml[1].Status != "" {
		t.Fatalf("yaml catalog = %+v, %v", yml, err)
	}
	none, err := ReadFeatures(ctx, &fakeRepo{}, testRepo, baseSHA)
	if err != nil || len(none) != 0 {
		t.Fatalf("no catalog = %+v, %v", none, err)
	}
	if _, err := ListFeaturesMarkdown([]byte("no table")); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Fatalf("a catalog without a table = %v", err)
	}
}

func TestDocsAreListedByPathWithTheirTitles(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{
		"docs/threat-model.md":     {Data: []byte("Intro\n# Threat model\n")},
		"docs/adr/0002-release.md": {Data: []byte("# ADR 0002 <!-- x --> release\n")},
		"docs/untitled.MD":         {Data: []byte("no heading\n")},
		"docs/diagram.svg":         {Data: []byte("<svg/>")},
		"README.md":                {Data: []byte("# Not under docs\n")},
	}
	docs, err := ListDocs(fsys, "docs")
	if err != nil {
		t.Fatalf("ListDocs: %v", err)
	}
	want := []Doc{
		{Path: "docs/adr/0002-release.md", Title: "ADR 0002 x release"},
		{Path: "docs/threat-model.md", Title: "Threat model"},
		{Path: "docs/untitled.MD", Title: "untitled.MD"},
	}
	if len(docs) != len(want) {
		t.Fatalf("docs %+v", docs)
	}
	for i := range want {
		if docs[i] != want[i] {
			t.Errorf("doc %d = %+v, want %+v", i, docs[i], want[i])
		}
	}
	if _, err := ListDocs(fsys, "missing"); !core.HasCode(err, core.CodeNotFound) {
		t.Errorf("a missing tree = %v, want NOT_FOUND", err)
	}
}
