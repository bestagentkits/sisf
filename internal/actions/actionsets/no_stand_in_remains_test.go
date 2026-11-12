package actionsets_test

import (
	"context"
	"testing"
	"testing/fstest"
	"time"

	"github.com/bestagentkits/sisf/internal/actions"
	"github.com/bestagentkits/sisf/internal/actions/actionsets"
	"github.com/bestagentkits/sisf/internal/actions/pendingactions"
	"github.com/bestagentkits/sisf/internal/actions/runactions"
	"github.com/bestagentkits/sisf/internal/budget"
	"github.com/bestagentkits/sisf/internal/catalog"
	"github.com/bestagentkits/sisf/internal/dispatcher"
	"github.com/bestagentkits/sisf/internal/intake/intaketest"
	"github.com/bestagentkits/sisf/internal/supervisor"
	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/telemetry"
)

// required are the actions the brief and the dashboard name. None of them
// may be served by a stand-in once the real sets are registered.
var required = []string{
	"factory.status", "issues.list", "runs.inspect", "runs.explain", "approvals.list", "approvals.get", "approvals.decide",
	"secrets.list_refs", "budget.get", "budget.set", "decisions.propose_update", "docs.list", "features.list",
	"changelog.list", "deployments.rollback", "factory.emergency_stop", "telemetry.search", "setup.sample_issue",
}

type noRecords struct{}

func (noRecords) Record(context.Context, core.IssueRef) (core.StateRecord, error) {
	return core.StateRecord{}, core.NewError(core.CodeNotFound, "none")
}
func (noRecords) Records(context.Context, state.RecordFilter) ([]core.StateRecord, error) {
	return nil, nil
}
func (noRecords) RunRecord(context.Context, core.RunID) (dispatcher.RunRecord, error) {
	return dispatcher.RunRecord{}, core.NewError(core.CodeNotFound, "none")
}

type noIssues struct{}

func (noIssues) CreateIssue(context.Context, github.RepoRef, github.NewIssue) (github.Issue, error) {
	return github.Issue{}, nil
}

type noSupervisor struct{}

func (noSupervisor) Status(context.Context) (supervisor.Status, error) {
	return supervisor.Status{}, nil
}
func (noSupervisor) EmergencyStop(context.Context, supervisor.EmergencyStopRequest) (supervisor.EmergencyStopReport, error) {
	return supervisor.EmergencyStopReport{}, nil
}
func (noSupervisor) RequestRollback(context.Context, supervisor.RollbackRequest) (supervisor.RollbackReport, error) {
	return supervisor.RollbackReport{}, nil
}

func realRegistry(t *testing.T) *actions.Registry {
	t.Helper()
	clock := intaketest.NewClock(time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC))
	st := intaketest.NewState(clock)
	keys := intaketest.NewKeys("k")
	inbox, err := approvals.NewStore(approvals.Config{State: st, FactoryID: "acme-app", Signer: keys, Verifier: keys,
		Audit: &intaketest.AuditLog{}, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	reg := actions.NewRegistry()
	err = actionsets.Register(reg, actionsets.Deps{
		FactoryID: "acme-app",
		Runs:      runactions.Deps{FactoryID: "acme-app", Records: noRecords{}, Runs: noRecords{}, Approvals: inbox},
		Approvals: inbox, Budget: budget.ActionsConfig{Store: budget.StateStore(st)},
		Catalog:   catalog.ActionsConfig{Publisher: &catalog.Publisher{}, Docs: fstest.MapFS{}},
		Telemetry: telemetry.NewMemoryStore(10), Repo: github.RepoRef{Owner: "acme", Name: "app"},
		Issues: noIssues{}, Supervisor: noSupervisor{},
	})
	if err != nil {
		t.Fatalf("register the real sets: %v", err)
	}
	return reg
}

func TestNoStandInRemainsReachableOnceTheRealSetsAreRegistered(t *testing.T) {
	t.Parallel()
	reg := realRegistry(t)
	for _, name := range append(required, namesOf(pendingactions.Specs())...) {
		if _, ok := reg.Lookup(name); !ok {
			t.Errorf("%s is not registered", name)
		} else if reg.IsFallback(name) {
			t.Errorf("%s is still served by a stand-in", name)
		}
	}
}

func TestTheDescribedRegistryMatchesTheRealOne(t *testing.T) {
	t.Parallel()
	described, err := actionsets.Describe()
	if err != nil {
		t.Fatal(err)
	}
	real := realRegistry(t)
	got, want := namesOf(described.Specs()), namesOf(real.Specs())
	if len(got) != len(want) {
		t.Fatalf("described %v, registered %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] || described.IsFallback(got[i]) {
			t.Fatalf("described %v, registered %v (fallback %v)", got, want, described.IsFallback(got[i]))
		}
	}
}

func namesOf(specs []core.ActionSpec) []string {
	out := make([]string, 0, len(specs))
	for _, s := range specs {
		out = append(out, s.Name)
	}
	return out
}
