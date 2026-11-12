package acceptance

import (
	"context"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/cmd/factoryd/wiring"
	"github.com/bestagentkits/sisf/cmd/supervisor/stages"
	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/state"
)

// Two factories with their own IDs work on forks of one upstream. What
// factory A improves lives only in A's state and A's branches; factory B
// and the upstream are untouched, and nothing goes upstream unless the
// Owner sends it.
func TestTwoFactoriesOnOneUpstreamKeepTheirImprovementsApart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	up := harness.NewUpstream(t)
	a := harness.New(t, harness.Options{FactoryID: "factory-a", Upstream: up})
	b := harness.New(t, harness.Options{FactoryID: "factory-b", Upstream: up})
	upstreamBranches := up.GitData.Branches(up.Repo)
	bBranches := b.GitData.Branches(b.Repo)

	number := a.OpenIssue("maintainer", "MEMBER", "Greet the world", bugBody, github.LabelReady, github.LabelKindBug)
	a.ScriptPatch(number, 0, greetingDiff)
	for range 4 {
		a.Cycle(1)
		b.Cycle(1)
	}
	wantSurfaces(t, a, number, core.StateClosed)
	record := a.Record(number)
	branch := stages.Branch(a.Manifest.FactoryID, record.RunID, 1)
	if _, _, ok := a.GitData.Branch(a.Repo, branch); !ok {
		t.Fatalf("factory A's candidate branch %s is missing", branch)
	}

	// Factory B saw the delivery and kept nothing of it.
	if records := listRecords(t, b); len(records) != 0 {
		t.Fatalf("factory B tracks another factory's issue: %+v", records)
	}
	if got := b.GitData.Branches(b.Repo); strings.Join(got, ",") != strings.Join(bBranches, ",") {
		t.Fatalf("factory B's repository changed from %v to %v", bBranches, got)
	}
	if staged := b.Deploy.Staged(); len(staged) != 0 {
		t.Fatalf("factory B deployed %+v", staged)
	}
	if got := up.GitData.Branches(up.Repo); strings.Join(got, ",") != strings.Join(upstreamBranches, ",") {
		t.Fatalf("the upstream changed from %v to %v", upstreamBranches, got)
	}

	// Sending the improvement upstream needs the Owner.
	offer := wiring.Contribution{Branch: branch, Title: "Greet the world"}
	for name, actor := range map[string]core.Actor{
		"a maintainer": {ID: "user:maintainer", Kind: core.ActorHuman, Roles: []core.Role{core.RoleMaintainer}, Surface: core.SurfaceCLI},
		"an agent":     {ID: "agent:fake/coder", Kind: core.ActorAgent},
	} {
		if _, err := a.Control.ContributeUpstream(ctx, actor, offer); !core.HasCode(err, core.CodeApprovalRequired) {
			t.Fatalf("%s contributed upstream: %v", name, err)
		}
	}
	owner := core.Actor{ID: "owner:local", Kind: core.ActorHuman, Roles: []core.Role{core.RoleOwner}, Surface: core.SurfaceCLI, StepUpAt: a.Clock.Now()}
	pr, err := a.Control.ContributeUpstream(ctx, owner, offer)
	if err != nil || pr.Number == 0 {
		t.Fatalf("the Owner's contribution was not opened upstream: %+v, %v", pr, err)
	}
}

// listRecords returns every lifecycle record a factory holds.
func listRecords(t *testing.T, f *harness.Factory) []core.StateRecord {
	t.Helper()
	var records []core.StateRecord
	err := f.ControlStore.View(context.Background(), func(ctx context.Context, tx state.Tx) error {
		var err error
		records, err = tx.ListRecords(ctx, state.RecordFilter{})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return records
}
