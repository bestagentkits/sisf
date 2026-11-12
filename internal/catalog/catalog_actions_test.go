package catalog

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"testing/fstest"

	"github.com/bestagentkits/sisf/internal/actions"
	"github.com/bestagentkits/sisf/internal/actions/pendingactions"
	"github.com/bestagentkits/sisf/pkg/core"
)

var (
	agentActor = core.Actor{ID: "agent:coder-1", Kind: core.ActorAgent, Roles: []core.Role{core.RoleAgent}}
	ownerActor = core.Actor{ID: "user:owner", Kind: core.ActorHuman, Roles: []core.Role{core.RoleOwner}}
)

func repoDecisions(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../DECISIONS.md")
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// catalogActions builds the actions over repo, keyed by name.
func catalogActions(t *testing.T, repo *fakeRepo) map[string]core.Action {
	t.Helper()
	list, err := Actions(ActionsConfig{Publisher: newPublisher(t, repo), Docs: fstest.MapFS{
		"docs/guide.md": {Data: []byte("# Guide\n")},
	}})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]core.Action{}
	for _, a := range list {
		if err := a.Spec.Validate(); err != nil {
			t.Fatal(err)
		}
		out[a.Spec.Name] = a
	}
	return out
}

func call(t *testing.T, a core.Action, actor core.Actor, input string, dryRun bool) (core.ActionResult, error) {
	t.Helper()
	return a.Handle(context.Background(), core.ActionRequest{Actor: actor, Input: json.RawMessage(input), DryRun: dryRun})
}

func TestCatalogActionsReplaceTheStandInsWithTheSameSensitivity(t *testing.T) {
	t.Parallel()
	list, err := Actions(ActionsConfig{Publisher: newPublisher(t, &fakeRepo{}), Docs: fstest.MapFS{}})
	if err != nil {
		t.Fatal(err)
	}
	registry := actions.NewRegistry()
	if err := registry.RegisterFallback(pendingactions.Fallbacks()...); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(list...); err != nil {
		t.Fatal(err)
	}
	standIns := map[string]core.ActionSpec{}
	for _, s := range pendingactions.Specs() {
		standIns[s.Name] = s
	}
	for _, a := range list {
		standIn, ok := standIns[a.Spec.Name]
		if !ok || registry.IsFallback(a.Spec.Name) || standIn.Sensitivity != a.Spec.Sensitivity {
			t.Errorf("%s: stand-in %+v, registered as fallback %v", a.Spec.Name, standIn, registry.IsFallback(a.Spec.Name))
		}
	}
	if len(list) != 4 {
		t.Fatalf("got %d actions, want 4", len(list))
	}
	if _, err := Actions(ActionsConfig{Docs: fstest.MapFS{}}); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Fatalf("no publisher: %v", err)
	}
}

func TestReadActionsListTheCatalogChangelogAndDocsAtTheBaseHead(t *testing.T) {
	t.Parallel()
	changelog, _ := AddChangelogEntry(nil, entry("run-7"))
	acts := catalogActions(t, &fakeRepo{files: map[string]string{PathFeatures: featuresMarkdown, PathChangelog: string(changelog)}})
	var features struct {
		Revision core.CommitSHA `json:"revision"`
		Features []Feature      `json:"features"`
	}
	decode(t, acts[ActionFeaturesList], `{}`, &features)
	if features.Revision != baseSHA || len(features.Features) != 2 {
		t.Fatalf("features = %+v", features)
	}
	var entries struct {
		Entries []ChangelogEntry `json:"entries"`
	}
	decode(t, acts[ActionChangelogList], `{"limit":5}`, &entries)
	if len(entries.Entries) != 1 || entries.Entries[0].RunID != "run-7" {
		t.Fatalf("changelog = %+v", entries)
	}
	var docs struct {
		Docs []Doc `json:"docs"`
	}
	decode(t, acts[ActionDocsList], ``, &docs)
	if len(docs.Docs) != 1 || docs.Docs[0].Title != "Guide" {
		t.Fatalf("docs = %+v", docs)
	}
	if _, err := call(t, acts[ActionChangelogList], agentActor, `{"limit":-1}`, false); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Fatalf("a negative limit: %v", err)
	}
	if _, err := call(t, acts[ActionFeaturesList], agentActor, `{"revision":"main"}`, false); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Fatalf("an unknown input field: %v", err)
	}
}

func decode(t *testing.T, a core.Action, input string, out any) {
	t.Helper()
	res, err := call(t, a, agentActor, input, false)
	if err != nil {
		t.Fatalf("%s: %v", a.Spec.Name, err)
	}
	if err := json.Unmarshal(res.Output, out); err != nil {
		t.Fatal(err)
	}
}
