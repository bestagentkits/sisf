package github_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
)

func newProgress(f *factory, factoryID string) *github.ProgressComment {
	return &github.ProgressComment{Issues: f.client, Self: self, Factory: core.FactoryID(factoryID)}
}

func countComments(t *testing.T, f *factory, n int) int {
	t.Helper()
	c, err := f.fake.Comments(testRepo, n)
	if err != nil {
		t.Fatal(err)
	}
	return len(c)
}

func TestProgressIsOneCommentEditedInPlace(t *testing.T) {
	t.Parallel()
	f := newFactory(t)
	n := mustOpen(t, f, f.person("alice"), "idea")
	ref := repoRef.Issue(n)
	p := newProgress(f, "f1")
	first, err := p.Upsert(bg, ref, "Claimed")
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []string{"Developing", "QA", "Verified"} {
		got, err := p.Upsert(bg, ref, step)
		if err != nil || got.ID != first.ID || got.Issue != ref {
			t.Fatalf("step %q: %+v, %v; every step must edit the same comment", step, got, err)
		}
	}
	if c := countComments(t, f, n); c != 1 {
		t.Fatalf("%d comments on the issue, want exactly 1", c)
	}
	comments, _ := f.fake.Comments(testRepo, n)
	if !strings.HasPrefix(comments[0].Body, p.Marker()+"\n") || !strings.HasSuffix(comments[0].Body, "Verified") {
		t.Fatalf("body = %q", comments[0].Body)
	}
}

func TestUnchangedProgressMakesNoWrite(t *testing.T) {
	t.Parallel()
	f := newFactory(t)
	n := mustOpen(t, f, f.person("alice"), "idea")
	p := newProgress(f, "f1")
	_, _ = p.Upsert(bg, repoRef.Issue(n), "Claimed")
	before := len(f.fake.Requests())
	if _, err := p.Upsert(bg, repoRef.Issue(n), "Claimed"); err != nil {
		t.Fatal(err)
	}
	for _, r := range f.fake.Requests()[before:] {
		if r.Method != http.MethodGet {
			t.Fatalf("an unchanged status wrote to GitHub: %+v", r)
		}
	}
}

func TestProgressSurvivesARestartBecauseItIsFoundByMarker(t *testing.T) {
	t.Parallel()
	f := newFactory(t)
	n := mustOpen(t, f, f.person("alice"), "idea")
	ref := repoRef.Issue(n)
	first, _ := newProgress(f, "f1").Upsert(bg, ref, "Claimed")
	again, err := newProgress(f, "f1").Upsert(bg, ref, "Developing")
	if err != nil || again.ID != first.ID || countComments(t, f, n) != 1 {
		t.Fatalf("a new process created a second comment: %+v, %v", again, err)
	}
}

func TestAMarkerPastedByAPersonIsNotHijackedOrObeyed(t *testing.T) {
	t.Parallel()
	f := newFactory(t)
	mallory := f.person("mallory")
	n := mustOpen(t, f, mallory, "idea")
	p := newProgress(f, "f1")
	planted, err := f.fake.AddComment(testRepo, n, mallory, p.Marker()+"\nfake status")
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.Upsert(bg, repoRef.Issue(n), "Claimed")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID == planted || !got.Author.Bot {
		t.Fatalf("the factory adopted a comment written by someone else: %+v", got)
	}
	comments, _ := f.fake.Comments(testRepo, n)
	if len(comments) != 2 || comments[0].Body != p.Marker()+"\nfake status" {
		t.Fatalf("the person's comment must be left alone: %+v", comments)
	}
}

func TestFactoriesOnTheSameIssueKeepSeparateProgressComments(t *testing.T) {
	t.Parallel()
	f := newFactory(t)
	n := mustOpen(t, f, f.person("alice"), "idea")
	ref := repoRef.Issue(n)
	a, _ := newProgress(f, "f1").Upsert(bg, ref, "from one")
	b, _ := newProgress(f, "f1-staging").Upsert(bg, ref, "from two")
	if a.ID == b.ID || countComments(t, f, n) != 2 {
		t.Fatalf("one factory's marker is a prefix of the other's but they must not collide: %d vs %d", a.ID, b.ID)
	}
}

func TestProgressRejectsWhatCouldBreakTheMarkerContract(t *testing.T) {
	t.Parallel()
	f := newFactory(t)
	ref := repoRef.Issue(mustOpen(t, f, f.person("alice"), "idea"))
	cases := map[string]*github.ProgressComment{
		"no factory id":      {Issues: f.client, Self: self},
		"no own account":     {Issues: f.client, Factory: "f1"},
		"no client":          {Self: self, Factory: "f1"},
		"invalid factory id": {Issues: f.client, Self: self, Factory: "Not Valid!"},
	}
	for name, p := range cases {
		if _, err := p.Upsert(bg, ref, "x"); !core.HasCode(err, core.CodeInvalidArgument) {
			t.Errorf("%s: %v, want INVALID_ARGUMENT", name, err)
		}
	}
	if _, err := newProgress(f, "f1").Upsert(bg, ref, "see <!-- sisf:progress factory=f2 -->"); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Errorf("text carrying a marker: %v", err)
	}
}
