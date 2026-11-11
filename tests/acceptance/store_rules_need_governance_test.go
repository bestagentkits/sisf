package acceptance

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/cmd/supervisor/stages"
	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
)

// storeRulesDiff adds the agent zone's own QA pass to the steps it may
// write. The fake repository holds no store sources, so the patch creates
// the file; the path is what the factory judges.
const storeRulesDiff = `--- /dev/null
+++ b/pkg/state/sqlstore/zone_ownership.go
@@ -0,0 +1,3 @@
+package sqlstore
+
+func init() { zoneEvents["agent"]["qa_passed"] = true }
`

// storeMigrationDiff appends a migration whose PostgreSQL script is built in
// Go and claims to be backward compatible.
const storeMigrationDiff = `--- /dev/null
+++ b/pkg/state/sqlstore/migrations.go
@@ -0,0 +1,3 @@
+package sqlstore
+
+func init() { releaseMigrations = append(releaseMigrations, migration{inline: "DROP TRIGGER sisf_record_step ON records"}) }
`

// storeContractDiff changes the store contract outside the package that
// builds the migrations: an ordinary build, but a governance change.
const storeContractDiff = `--- /dev/null
+++ b/pkg/state/zone_steps.go
@@ -0,0 +1 @@
+package state
`

// A coding agent's patch that changes the rules of who may take which
// lifecycle step, or the migrations of the state store, is refused before
// anything is committed: the next build of every zone binary, and the
// database it migrates, would carry it. Nothing is staged, no branch or
// main holds the file, and no pull request is merged.
func TestAPatchOfTheStoreRulesOrMigrationsIsNeitherStagedNorMerged(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct{ diff, path string }{
		"the zones' lifecycle steps": {storeRulesDiff, "pkg/state/sqlstore/zone_ownership.go"},
		"a migration built in Go":    {storeMigrationDiff, "pkg/state/sqlstore/migrations.go"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := harness.New(t, harness.Options{})
			number := f.OpenIssue("maintainer", "MEMBER", "Let runs pass their own QA", bugBody, github.LabelReady, github.LabelKindBug)
			f.ScriptPatch(number, 0, c.diff)

			record := exportedPatch(t, f, number)
			writes := f.GitData.Writes()
			if res := f.Stages.Build.Pass(context.Background()); len(res.Errors) > 0 || !slices.Equal(res.Unbuildable, []core.IssueRef{f.Issue(number)}) {
				t.Fatalf("the build pass: %+v", res)
			}
			if got := f.GitData.Writes(); got != writes {
				t.Fatalf("GitHub received %d git data writes for a refused patch", got-writes)
			}
			doc := candidateOf(t, f, record)
			if doc.BuildRefusal != string(stages.RefusalProtectedPath) || !strings.Contains(doc.BuildFailure, c.path) ||
				!strings.Contains(doc.BuildFailure, string(stages.ProtectedMigration)) {
				t.Fatalf("the refusal was recorded as %q: %q", doc.BuildRefusal, doc.BuildFailure)
			}

			f.Cycle(6)
			wantNothingReleased(t, f, c.path)
			for _, b := range f.GitData.Branches(f.Repo) {
				if _, files, _ := f.GitData.Branch(f.Repo, b); files[c.path] != "" {
					t.Fatalf("branch %s holds the agent's %s", b, c.path)
				}
			}
		})
	}
}

// A candidate that changes the store contract is built and evaluated like
// any other, and then refused by the release supervisor as a governance
// change: the issue is blocked for it, nothing is deployed and nothing
// reaches main.
func TestACandidateThatChangesTheStoreContractIsRefusedAsAGovernanceChange(t *testing.T) {
	t.Parallel()
	f := harness.New(t, harness.Options{})
	number := f.OpenIssue("maintainer", "MEMBER", "Name the zones' steps", bugBody, github.LabelReady, github.LabelKindBug)
	f.ScriptPatch(number, 0, storeContractDiff)
	f.Cycle(6)

	if rec := wantRefusedAndBlocked(t, f, number, core.CodeApprovalRequired); !strings.Contains(rec.Reason, "pkg/state/zone_steps.go") {
		t.Fatalf("the release was not refused for the store contract: %+v", rec)
	}
	wantNothingReleased(t, f, "pkg/state/zone_steps.go")
}

// An ordinary change of the application, under the same factory, is still
// staged, released and merged by the factory alone.
func TestAnOrdinaryApplicationChangeIsStillStagedAndMerged(t *testing.T) {
	t.Parallel()
	f := harness.New(t, harness.Options{})
	number := f.OpenIssue("maintainer", "MEMBER", "Greet the world", bugBody, github.LabelReady, github.LabelKindBug)
	f.ScriptPatch(number, 0, greetingDiff)
	f.Cycle(4)

	wantSurfaces(t, f, number, core.StateClosed)
	staged := f.Deploy.Staged()
	if len(staged) == 0 || staged[len(staged)-1].Unit != "app" {
		t.Fatalf("the application change was not staged: %+v", staged)
	}
	if _, files, ok := f.GitData.Branch(f.Repo, "main"); !ok || !strings.Contains(files["examples/hello-app/main.go"], "hello, world") || f.GitData.Merges() != 1 {
		t.Fatalf("main does not carry the change after %d merges", f.GitData.Merges())
	}
}

// wantNothingReleased fails if anything was deployed or merged, or if main
// holds path.
func wantNothingReleased(t *testing.T, f *harness.Factory, path string) {
	t.Helper()
	if staged := f.Deploy.Staged(); len(staged) != 0 {
		t.Fatalf("a change of the store was deployed: %+v", staged)
	}
	if merges := f.GitData.Merges(); merges != 0 {
		t.Fatalf("%d pull requests were merged", merges)
	}
	if _, files, _ := f.GitData.Branch(f.Repo, "main"); files[path] != "" {
		t.Fatalf("main holds %s", path)
	}
}
