package conformance_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/conformance/conformancetest"
	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/conformance"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/execution"
)

var errBroken = errors.New("backend is broken")

func executionProbe() conformance.ExecutionProbe {
	return conformance.ExecutionProbe{Spec: conformancetest.SandboxSpec(), Command: execution.Command{Argv: []string{"true"}}, EndToEndProven: true}
}

func deployProbe() conformance.DeployProbe {
	baseline, candidate := conformancetest.Artifacts()
	return conformance.DeployProbe{Baseline: baseline, Candidate: candidate, Window: time.Minute, EndToEndProven: true}
}

func agentProbe() conformance.AgentProbe {
	return conformance.AgentProbe{Task: conformancetest.Task(), Workspace: agent.WorkspaceRef{ID: "probe"}, EndToEndProven: true}
}

// wantMissing fails unless the row is unsupported for exactly the given area.
func wantMissing(t *testing.T, row conformance.Row, err error, area core.Capability) {
	t.Helper()
	if err != nil {
		t.Fatalf("a backend that fails a check is graded, not an error: %v", err)
	}
	if row.Verdict != conformance.VerdictUnsupported || !slices.Contains(row.Missing, area) {
		t.Fatalf("verdict %q, missing %v; want unsupported with %s missing", row.Verdict, row.Missing, area)
	}
	if len(row.Notes) == 0 {
		t.Fatal("a lowered grade must say why")
	}
}

func TestKitGradesABackendThatDoesWhatItDeclares(t *testing.T) {
	ctx := context.Background()
	rows := map[conformance.Kind]conformance.Row{}
	var err error
	if rows[conformance.KindExecution], err = conformance.GradeExecution(ctx, conformancetest.NewExecution("new-sandbox"), executionProbe()); err != nil {
		t.Fatal(err)
	}
	if rows[conformance.KindDeploy], err = conformance.GradeDeploy(ctx, conformancetest.NewDeploy("new-platform"), deployProbe()); err != nil {
		t.Fatal(err)
	}
	if rows[conformance.KindAgent], err = conformance.GradeAgent(ctx, conformancetest.NewAgent("new-agent"), agentProbe()); err != nil {
		t.Fatal(err)
	}
	for kind, row := range rows {
		if row.Kind != kind || row.Verdict != conformance.VerdictSupported || len(row.Notes) != 0 {
			t.Errorf("%s: a backend that proves every area must be supported, got %+v", kind, row)
		}
	}
	// The same behavior without an end-to-end run is not yet "supported".
	probe := executionProbe()
	probe.EndToEndProven = false
	row, err := conformance.GradeExecution(ctx, conformancetest.NewExecution("new-sandbox"), probe)
	if err != nil || row.Verdict != conformance.VerdictExperimental {
		t.Errorf("without an end-to-end run: verdict %q, err %v; want experimental", row.Verdict, err)
	}
}

func TestKitLowersAnExecutionBackendThatFailsWhatItDeclares(t *testing.T) {
	cases := map[string]struct {
		bend func(b *conformancetest.Execution)
		area core.Capability
	}{
		"it is not healthy":                    {func(b *conformancetest.Execution) { b.Fail["Health"] = errBroken }, core.CapLifecycle},
		"it cannot create a sandbox":           {func(b *conformancetest.Execution) { b.Fail["CreateSandbox"] = errBroken }, core.CapLifecycle},
		"it cannot run a command":              {func(b *conformancetest.Execution) { b.Fail["Exec"] = errBroken }, core.CapLifecycle},
		"the command fails":                    {func(b *conformancetest.Execution) { b.ExitCode = 3 }, core.CapLifecycle},
		"a sandbox cannot be inspected":        {func(b *conformancetest.Execution) { b.Fail["Inspect"] = errBroken }, core.CapObservability},
		"a sandbox cannot be destroyed":        {func(b *conformancetest.Execution) { b.Fail["Destroy"] = errBroken }, core.CapRollback},
		"destroy leaves the sandbox running":   {func(b *conformancetest.Execution) { b.LeaveBehind = true }, core.CapRollback},
		"destroying twice is an error":         {func(b *conformancetest.Execution) { b.DestroyOnce = true }, core.CapRollback},
		"isolation is hosted but never proven": {func(b *conformancetest.Execution) { delete(b.Declared, core.CapExecIsolation) }, core.CapIsolation},
		"the egress policy cannot be enforced": {func(b *conformancetest.Execution) {
			b.Declared[core.CapExecEgressEnforcement] = core.SupportNotSupported
		}, core.CapIsolation},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			backend := conformancetest.NewExecution("bent")
			tc.bend(backend)
			row, err := conformance.GradeExecution(context.Background(), backend, executionProbe())
			if tc.area == core.CapIsolation {
				// Isolation is judged from the declaration, so there is no note.
				if err != nil || !slices.Equal(row.Missing, []core.Capability{core.CapIsolation}) {
					t.Fatalf("missing %v, err %v; want isolation missing", row.Missing, err)
				}
				return
			}
			wantMissing(t, row, err, tc.area)
		})
	}
}

func TestKitLowersADeploymentBackendThatFailsWhatItDeclares(t *testing.T) {
	cases := map[string]struct {
		bend func(b *conformancetest.Deploy)
		area core.Capability
	}{
		"it cannot stage":                           {func(b *conformancetest.Deploy) { b.Fail["Stage"] = errBroken }, core.CapLifecycle},
		"it cannot promote":                         {func(b *conformancetest.Deploy) { b.Fail["Promote"] = errBroken }, core.CapLifecycle},
		"the running revision cannot be observed":   {func(b *conformancetest.Deploy) { b.Fail["ObservedRevision"] = errBroken }, core.CapObservability},
		"a revision cannot be measured":             {func(b *conformancetest.Deploy) { b.Fail["Measure"] = errBroken }, core.CapObservability},
		"the rollback fails":                        {func(b *conformancetest.Deploy) { b.Fail["Rollback"] = errBroken }, core.CapRollback},
		"the rollback succeeds but changes nothing": {func(b *conformancetest.Deploy) { b.RollbackDoesNothing = true }, core.CapRollback},
		"staging takes live traffic":                {func(b *conformancetest.Deploy) { b.StageTakesTraffic = true }, core.CapIsolation},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			backend := conformancetest.NewDeploy("bent")
			tc.bend(backend)
			row, err := conformance.GradeDeploy(context.Background(), backend, deployProbe())
			wantMissing(t, row, err, tc.area)
		})
	}
}

// pretender reports a cancel as done although its backend cannot cancel.
type pretender struct{ *conformancetest.Agent }

func (pretender) Cancel(context.Context, agent.RunRef) error { return nil }

func TestKitLowersAnAgentBackendThatFailsWhatItDeclares(t *testing.T) {
	cases := map[string]struct {
		bend func(b *conformancetest.Agent)
		area core.Capability
	}{
		"it cannot start a run":                   {func(b *conformancetest.Agent) { b.Fail["Start"] = errBroken }, core.CapLifecycle},
		"artifacts cannot be collected":           {func(b *conformancetest.Agent) { b.Fail["CollectArtifacts"] = errBroken }, core.CapLifecycle},
		"the status cannot be read":               {func(b *conformancetest.Agent) { b.Fail["Status"] = errBroken }, core.CapObservability},
		"the events cannot be read":               {func(b *conformancetest.Agent) { b.Fail["Events"] = errBroken }, core.CapObservability},
		"the cancel fails":                        {func(b *conformancetest.Agent) { b.Fail["Cancel"] = errBroken }, core.CapRollback},
		"the cancel succeeds but the run goes on": {func(b *conformancetest.Agent) { b.CancelDoesNothing = true }, core.CapRollback},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			backend := conformancetest.NewAgent("bent")
			tc.bend(backend)
			row, err := conformance.GradeAgent(context.Background(), backend, agentProbe())
			wantMissing(t, row, err, tc.area)
		})
	}
}

func TestAnAgentThatCannotCancelMustSaySoInsteadOfPretending(t *testing.T) {
	honest := conformancetest.NewAgent("remote")
	honest.Declared[core.CapAgentCancel] = core.SupportNotSupported
	row, err := conformance.GradeAgent(context.Background(), honest, agentProbe())
	// Refusing honestly costs the rollback area and nothing else.
	if err != nil || !slices.Equal(row.Missing, []core.Capability{core.CapRollback}) || len(row.Notes) != 0 {
		t.Fatalf("honest refusal: missing %v, notes %v, err %v", row.Missing, row.Notes, err)
	}
	row, err = conformance.GradeAgent(context.Background(), pretender{honest}, agentProbe())
	wantMissing(t, row, err, core.CapLifecycle)
}

func TestKitRefusesToGradeWhatItCannotExercise(t *testing.T) {
	ctx := context.Background()
	silent := conformancetest.NewExecution("silent")
	silent.Fail["Capabilities"] = errBroken
	if _, err := conformance.GradeExecution(ctx, silent, executionProbe()); !core.HasCode(err, core.CodeUnavailable) || !errors.Is(err, errBroken) {
		t.Errorf("a backend that will not declare itself cannot be graded, got %v", err)
	}
	mute := conformancetest.NewDeploy("mute")
	mute.Fail["Capabilities"] = errBroken
	if _, err := conformance.GradeDeploy(ctx, mute, deployProbe()); !core.HasCode(err, core.CodeUnavailable) {
		t.Errorf("a deployment backend that will not declare itself cannot be graded, got %v", err)
	}
	quiet := conformancetest.NewAgent("quiet")
	quiet.Fail["Capabilities"] = errBroken
	if _, err := conformance.GradeAgent(ctx, quiet, agentProbe()); !core.HasCode(err, core.CodeUnavailable) {
		t.Errorf("an agent backend that will not declare itself cannot be graded, got %v", err)
	}

	if _, err := conformance.GradeExecution(ctx, nil, executionProbe()); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Errorf("no execution backend: %v", err)
	}
	if _, err := conformance.GradeDeploy(ctx, nil, deployProbe()); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Errorf("no deployment backend: %v", err)
	}
	if _, err := conformance.GradeAgent(ctx, nil, agentProbe()); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Errorf("no agent backend: %v", err)
	}

	noTimeout := executionProbe()
	noTimeout.Spec.Limits.Timeout = 0
	if _, err := conformance.GradeExecution(ctx, conformancetest.NewExecution("e"), noTimeout); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Errorf("a sandbox without a time limit is not a valid probe: %v", err)
	}
	same := deployProbe()
	same.Candidate = same.Baseline
	if _, err := conformance.GradeDeploy(ctx, conformancetest.NewDeploy("d"), same); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Errorf("a rollback cannot be told from a release when both builds are the same: %v", err)
	}
	unsigned := deployProbe()
	unsigned.Candidate.Signature = core.Signature{}
	if _, err := conformance.GradeDeploy(ctx, conformancetest.NewDeploy("d"), unsigned); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Errorf("an unsigned artifact is not a valid probe: %v", err)
	}
	noScope := agentProbe()
	noScope.Task.Handoff.SoW = ""
	if _, err := conformance.GradeAgent(ctx, conformancetest.NewAgent("a"), noScope); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Errorf("a task without a scope of work is not a valid probe: %v", err)
	}
}
