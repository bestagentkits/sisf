//go:build unix

package cliagent_test

import (
	"context"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/agent/cliagent"
	"github.com/bestagentkits/sisf/pkg/agent/cliagent/clitest"
)

// A burst of output written just before the process exits is still in the
// pipe when the exit is seen. However long reading it takes, as on a
// loaded machine, the drain grace must not cut it off, or the final report
// at its end is lost and a finished run reads as one that never reported.
func TestOutputWrittenBeforeTheExitIsReadToTheEndHoweverLongItTakes(t *testing.T) {
	t.Parallel()
	steps := make([]clitest.Step, 0, 6001)
	for range 6000 {
		steps = append(steps, clitest.Step{Emit: `{"type":"msg","text":"working"}`})
	}
	steps = append(steps, clitest.Step{Emit: `{"type":"done"}`})
	r := newRig(t, clitest.Script{Steps: steps})
	// A grace far shorter than reading the burst takes stands in for a
	// machine too loaded to read it within the real one.
	cliagent.SetDrainGrace(r.b, time.Millisecond)
	ref := r.start(t, newTask("run-0001", "k1"))
	if status := r.wait(t, ref); status.Phase != agent.PhaseSucceeded {
		t.Fatalf("a run that reported at the end of its output ended %s: %s", status.Phase, status.Detail)
	}
	report, err := r.b.FinalReport(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if report.Complete {
		t.Fatal("a record with events past the cap reads as complete")
	}
}
