package install

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// exitedWith is the error of a program that ended with an exit status, as
// os/exec reports it.
type exitedWith int

func (e exitedWith) Error() string { return "exit status " + itoa64(int64(e)) }
func (e exitedWith) ExitCode() int { return int(e) }

// A unit that refuses its configuration would refuse again at every start,
// so the group does not start it again: it stops every unit and reports the
// refusal, with the end of the unit's log, as one an `up` can never pass.
func TestAUnitThatRefusesItsConfigurationIsNotStartedAgain(t *testing.T) {
	t.Parallel()
	e, c := newFakeExec(), newStepClock()
	g := testGroup(e, c, "supervisor", "control")
	g.logTail = func(name string) string {
		return "  " + name + ": PERMISSION_DENIED: configure the docker execution backend"
	}
	done := make(chan error, 1)
	go func() { done <- g.Run(t.Context()) }()
	procs := map[string]*fakeProc{}
	for range 2 {
		p := nextProc(t, e)
		procs[p.cmd.Name] = p
	}
	procs["supervisor"].exit(exitedWith(exitConfigRefused))
	var err error
	select {
	case err = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the group kept running after a unit refused its configuration")
	}
	if !Refused(err) || !strings.Contains(err.Error(), "supervisor refused its configuration") || !strings.Contains(err.Error(), "PERMISSION_DENIED") {
		t.Fatalf("Run = %v, want a refusal naming the unit and its reason", err)
	}
	if !procs["control"].terminated {
		t.Error("the other units kept running")
	}
	if got := e.startedNames(); len(got) != 2 {
		t.Fatalf("started %v, want no second start", got)
	}
}

// Any other exit is a crash the backoff starts again after.
func TestAUnitThatFailsOtherwiseIsStillStartedAgain(t *testing.T) {
	t.Parallel()
	e, c := newFakeExec(), newStepClock()
	stop := runGroup(t, testGroup(e, c, "supervisor"))
	nextProc(t, e).exit(exitedWith(1))
	c.waitArmed(t)
	c.Advance(restartBase)
	nextProc(t, e)
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if exitStatus(errors.New("signal: killed")) != -1 {
		t.Fatal("an exit without a status counted as one")
	}
}

// The end of a unit's log is its last lines, whole.
func TestTheLogTailOfAUnitIsItsLastWholeLines(t *testing.T) {
	t.Parallel()
	in, _, _ := newTestInstaller(t)
	must(t, os.MkdirAll(in.Layout.Logs(), 0o700))
	if got := in.unitLogTail("supervisor"); got != "" {
		t.Fatalf("tail of a missing log = %q", got)
	}
	body := strings.Repeat("x", logTailBytes) + "\nfirst\n\nsecond\nthird\nfourth\n"
	must(t, os.WriteFile(filepath.Join(in.Layout.Logs(), "supervisor.log"), []byte(body), 0o600))
	if got, want := in.unitLogTail("supervisor"), "  second\n  third\n  fourth"; got != want {
		t.Fatalf("tail = %q, want %q", got, want)
	}
}
