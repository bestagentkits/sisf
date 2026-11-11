//go:build e2e

package e2e

import (
	"strings"
	"testing"
)

// A change to the factory's own control plane follows the governed path:
// built, judged with a signed receipt and handed to the release pipeline,
// which refuses it because the install's release.units names the
// application alone; nothing of the factory is replaced and the
// application keeps serving. A change to the trusted QA baseline is refused
// as a governance change, whatever QA said, and nothing is deployed.
func TestComposeGovernsChangesToTheFactoryItself(t *testing.T) {
	d := newComposeRelease(t)
	seedRelease(t, d, dashboardDiff)
	supervisorBefore := d.containerIdentity(t, "supervisor")
	controlBefore := d.containerIdentity(t, "control")
	before := servedRevision(t, d)
	load := startTraffic(t, d)

	_, governed := openBug(t, d, "Say who checked the status")
	rec := awaitRelease(t, d, governed)
	journey := d.journey(t, governed)
	if !strings.Contains(strings.Join(journey, ">"), "Developing>QAEvaluation>ReleaseCandidate") {
		t.Fatalf("the dashboard change went %v", journey)
	}
	if rec.Result != "refused" || !strings.HasPrefix(rec.Reason, "APPROVAL_REQUIRED") || !strings.Contains(rec.Reason, "release.units") {
		t.Fatalf("the release of the control unit ended %+v", rec)
	}
	if deployed := d.deployed(t, "control", true); len(deployed) != 0 {
		t.Fatalf("the control unit was deployed: %v", deployed)
	}

	setPatch(t, d, qaBaselineDiff)
	_, ungoverned := openBug(t, d, "Make QA faster")
	rec = awaitRelease(t, d, ungoverned)
	if rec.Result != "refused" || !strings.HasPrefix(rec.Reason, "APPROVAL_REQUIRED") {
		t.Fatalf("the change to the QA baseline was not refused as a governance change: %+v", rec)
	}
	load.halt()

	if got := d.containerIdentity(t, "supervisor"); got != supervisorBefore {
		t.Fatalf("the supervisor was replaced: %s, was %s", got, supervisorBefore)
	}
	if got := d.containerIdentity(t, "control"); got != controlBefore {
		t.Fatalf("the control plane was replaced: %s, was %s", got, controlBefore)
	}
	if got := servedRevision(t, d); got != before {
		t.Fatalf("the application moved to %s, was %s", got, before)
	}
	if sent, failed := load.sent.Load(), load.failed.Load(); sent == 0 || failed != 0 {
		t.Fatalf("the application failed %d of %d requests while the factory changed itself", failed, sent)
	}
}

// containerIdentity is a container's image and start time: it changes when
// the container is replaced or restarted.
func (d *composeDeployment) containerIdentity(t *testing.T, unit string) string {
	t.Helper()
	out, err := d.docker(t, "inspect", "--format", "{{.Image}} {{.State.StartedAt}}", d.container(unit))
	if err != nil {
		t.Fatalf("inspect %s: %v\n%s", unit, err, out)
	}
	return strings.TrimSpace(out)
}
