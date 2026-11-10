package stages

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/qa"
)

func TestProtectedPathNamesCIOwnershipPolicyAndMigrations(t *testing.T) {
	t.Parallel()
	protected := map[string]ProtectedKind{
		".github/workflows/ci.yml":          ProtectedCI,
		".github/actions/setup/action.yml":  ProtectedCI,
		".github/dependabot.yml":            ProtectedCI,
		".GitHub/Workflows/Release.YML":     ProtectedCI,
		"CODEOWNERS":                        ProtectedOwnership,
		".github/CODEOWNERS":                ProtectedOwnership,
		"docs/CODEOWNERS":                   ProtectedOwnership,
		".sisf/factory.yaml":                ProtectedPolicy,
		".sisf/anything/else":               ProtectedPolicy,
		"AGENTS.md":                         ProtectedPolicy,
		"agents.md":                         ProtectedPolicy,
		"REVIEW.md":                         ProtectedPolicy,
		"DECISIONS.md":                      ProtectedPolicy,
		"FEATURES.md":                       ProtectedPolicy,
		"features.yaml":                     ProtectedPolicy,
		"./AGENTS.md":                       ProtectedPolicy,
		"migrations/0001_init.sql":          ProtectedMigration,
		"pkg/state/migrations/0002.sql":     ProtectedMigration,
		"app/../.github/workflows/push.yml": ProtectedCI,
	}
	for p, want := range protected {
		if got, ok := ProtectedPath(p); !ok || got != want {
			t.Errorf("%s: got %q, %v; want %q", p, got, ok, want)
		}
	}
	for _, p := range []string{"app/main.go", "docs/AGENTS.md", "ui/github/page.html", "src/codeowners_test.go", "migrationsfoo/x", ""} {
		if kind, ok := ProtectedPath(p); ok {
			t.Errorf("%s is not protected, got %q", p, kind)
		}
	}
}

func TestTheHardGatesUseTheSamePredicateAsTheBuilder(t *testing.T) {
	t.Parallel()
	gates := map[qa.GateName]func(context.Context, qa.Candidate) (bool, string, error){}
	for _, g := range HardGates(GateSources{SupplyChain: gate{name: qa.GateSupplyChain}}) {
		if fn, ok := g.(gate); ok {
			gates[g.Name()] = fn.check
		}
	}
	for p, failing := range map[string]qa.GateName{
		".github/actions/build/action.yml": qa.GatePermissions,
		"docs/CODEOWNERS":                  qa.GatePermissions,
		".sisf/factory.yaml":               qa.GatePermissions,
		"AGENTS.md":                        qa.GatePermissions,
		"db/migrations/0003.sql":           qa.GateMigrations,
	} {
		for _, name := range []qa.GateName{qa.GatePermissions, qa.GateMigrations} {
			passed, _, err := gates[name](context.Background(), qa.Candidate{ChangedPaths: []string{"app/main.go", p}})
			if err != nil || passed == (name == failing) {
				t.Errorf("%s under the %s gate: passed=%v, %v", p, name, passed, err)
			}
		}
	}
}

// recordingRepo is a repository that records every call and holds one file.
type recordingRepo struct {
	reads, commits int
	files          map[string]string
	commitErr      error
}

func (r *recordingRepo) ReadFile(_ context.Context, _ github.RepoRef, _ core.CommitSHA, p string) ([]byte, error) {
	r.reads++
	content, ok := r.files[p]
	if !ok {
		return nil, core.NewError(core.CodeNotFound, "no such file")
	}
	return []byte(content), nil
}

func (r *recordingRepo) CommitToBranch(context.Context, github.RepoRef, github.BranchCommit) (core.CommitSHA, error) {
	r.commits++
	if r.commitErr != nil {
		return "", r.commitErr
	}
	return "0123456789abcdef0123456789abcdef01234567", nil
}

const baseSHA = core.CommitSHA("89abcdef0123456789abcdef0123456789abcdef")

func newTestBuilder(repo *recordingRepo) *CandidateBuilder {
	return &CandidateBuilder{FactoryID: "f1", Repo: github.RepoRef{Owner: "acme", Name: "app"}, Git: repo,
		Images: SimulatedImages{}, Units: DefaultUnitRouter(), Clock: core.SystemClock{}}
}

func TestTheBuilderRefusesAProtectedPathBeforeReadingOrPushingAnything(t *testing.T) {
	t.Parallel()
	for name, diff := range map[string]string{
		"a new push workflow":           "--- /dev/null\n+++ b/.github/workflows/push.yml\n@@ -0,0 +1 @@\n+on: push\n",
		"a composite action":            "--- a/.github/actions/x/action.yml\n+++ b/.github/actions/x/action.yml\n@@ -1 +1 @@\n-a\n+b\n",
		"code ownership":                "--- /dev/null\n+++ b/CODEOWNERS\n@@ -0,0 +1 @@\n+* @me\n",
		"the policy":                    "--- a/AGENTS.md\n+++ b/AGENTS.md\n@@ -1 +1 @@\n-a\n+b\n",
		"a migration":                   "--- /dev/null\n+++ b/migrations/0009.sql\n@@ -0,0 +1 @@\n+drop table x;\n",
		"a rename away from a workflow": "--- a/.github/workflows/ci.yml\n+++ b/app/ci.yml\n@@ -1 +1 @@\n-a\n+b\n",
		"one of several files": "--- a/app/main.go\n+++ b/app/main.go\n@@ -1 +1 @@\n-a\n+b\n" +
			"--- /dev/null\n+++ b/.github/workflows/push.yml\n@@ -0,0 +1 @@\n+on: push\n",
	} {
		repo := &recordingRepo{files: map[string]string{"app/main.go": "a\n"}}
		_, err := newTestBuilder(repo).Build(context.Background(), BuildRequest{RunID: "run-1", Attempt: 1, Base: baseSHA, Diff: diff})
		refusal, ok := RefusalOf(err)
		if !ok || refusal.Reason != RefusalProtectedPath || !core.HasCode(err, core.CodePermissionDenied) {
			t.Errorf("%s: %v", name, err)
		}
		if repo.reads != 0 || repo.commits != 0 {
			t.Errorf("%s: the builder read %d files and committed %d times", name, repo.reads, repo.commits)
		}
	}
}

func TestOnlyFaultsOfThePatchAreRefusals(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := &recordingRepo{files: map[string]string{"app/main.go": "a\n"}}
	for name, c := range map[string]struct {
		diff string
		want RefusalReason
	}{
		"unreadable":      {"not a diff", RefusalUnreadable},
		"does not apply":  {"--- a/app/main.go\n+++ b/app/main.go\n@@ -1 +1 @@\n-z\n+b\n", RefusalDoesNotApply},
		"missing in base": {"--- a/app/gone.go\n+++ b/app/gone.go\n@@ -1 +1 @@\n-z\n+b\n", RefusalDoesNotApply},
		"spans two units": {"--- a/app/main.go\n+++ b/app/main.go\n@@ -1 +1 @@\n-a\n+b\n--- /dev/null\n+++ b/ui/x.html\n@@ -0,0 +1 @@\n+x\n", RefusalUnitSpan},
	} {
		_, err := newTestBuilder(repo).Build(ctx, BuildRequest{RunID: "run-1", Attempt: 1, Base: baseSHA, Diff: c.diff})
		if r, ok := RefusalOf(err); !ok || r.Reason != c.want {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A GitHub 404 or 409 from the commit is an outage, not the patch's fault.
	for _, code := range []core.ErrorCode{core.CodeNotFound, core.CodeConflict, core.CodeUnavailable} {
		repo := &recordingRepo{files: map[string]string{"app/main.go": "a\n"}, commitErr: core.NewError(code, "github said no")}
		_, err := newTestBuilder(repo).Build(ctx, BuildRequest{RunID: "run-1", Attempt: 1, Base: baseSHA,
			Diff: "--- a/app/main.go\n+++ b/app/main.go\n@@ -1 +1 @@\n-a\n+b\n"})
		if _, refused := RefusalOf(err); refused || !core.HasCode(err, code) {
			t.Errorf("a commit failing with %s: %v", code, err)
		}
	}
	if _, refused := RefusalOf(errors.New("docker is down")); refused {
		t.Error("a plain error is not a refusal")
	}
}

func TestAnOutageIsRetriedWithABoundedBackoffAndHeldAfterRepeatedFailures(t *testing.T) {
	t.Parallel()
	r := BuildRetry{First: time.Second, Max: 5 * time.Second, HoldAfter: 3}.settled()
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 5 * time.Second, 5 * time.Second}
	for i, w := range want {
		if got := r.backoff(i + 1); got != w {
			t.Errorf("after %d failures: %s, want %s", i+1, got, w)
		}
	}
	if r.held(retryEntry{BuildOutage: BuildOutage{Failures: 2}}) || !r.held(retryEntry{BuildOutage: BuildOutage{Failures: 3}}) {
		t.Error("the hold threshold is not three failures")
	}
	if d := (BuildRetry{}).settled(); d != DefaultBuildRetry {
		t.Errorf("the zero value settles to %+v", d)
	}
	now := time.Now()
	if (retryEntry{}).waiting(now) {
		t.Error("a patch with no outage is not waiting")
	}
	if !(retryEntry{BuildOutage: BuildOutage{NextTryAt: now.Add(time.Second)}, version: 1}).waiting(now) {
		t.Error("a patch within its backoff is waiting")
	}
}
