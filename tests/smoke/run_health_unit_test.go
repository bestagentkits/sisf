//go:build smoke

package smoke

import (
	"strings"
	"testing"
)

// The run stops waiting as soon as the factory's own records show the
// issue cannot close: the run used every attempt, the dispatcher logged the
// last allowed attempt failing, or a unit of `sisf up` exited. Anything else
// is a factory still at work.
func TestTheRunStopsWaitingWhenTheFactoryCannotFinish(t *testing.T) {
	t.Parallel()
	const healthyUp = "started control\nstarted dispatcher\nrestarting every unit\nstopped dispatcher\nstarted dispatcher\n"
	const working = `time=x level=INFO msg="issue claimed" issue=acme/app#1` + "\n"
	for name, c := range map[string]struct {
		runStatus, upLog, dispatcherLog string
		want                            string // part of the reason; "" means keep waiting
	}{
		"a factory at work": {"working", healthyUp, working, ""},
		"an earlier attempt failed": {"working", healthyUp, working +
			`time=x level=WARN msg="a coding attempt failed" issue=acme/app#1 run=run-1 attempt=2 code=UNAVAILABLE detail="x"` + "\n", ""},
		"the run is exhausted": {"exhausted", healthyUp, working, "used every coding attempt"},
		"the last attempt failed": {"working", healthyUp, working +
			`time=x level=WARN msg="a coding attempt failed" issue=acme/app#1 run=run-1 attempt=3 code=INVALID_ARGUMENT detail="the agent did not start"` + "\n",
			"attempt 3 of 3 failed"},
		"the dispatcher stopped the run": {"", healthyUp, working +
			`time=x level=ERROR msg="a run used every attempt and stopped; the issue stays where it is until the Owner acts" issue=acme/app#1` + "\n",
			"used every coding attempt"},
		"a unit exited":       {"working", healthyUp + "dispatcher exited (exit status 1); starting it again\n", working, "dispatcher exited"},
		"a unit cannot start": {"working", healthyUp + "supervisor cannot start: fork/exec: no such file\n", working, "supervisor cannot start"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := cannotFinish(c.runStatus, c.upLog, c.dispatcherLog, 3)
			if (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
				t.Fatalf("cannotFinish = %q, want %q", got, c.want)
			}
		})
	}
}
