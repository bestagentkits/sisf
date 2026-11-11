//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

// Helpers of the release scenarios: an issue taken from Ready through a
// candidate build, independent QA and the release pipeline of a running
// factory, with the scripted agent leaving the patch the scenario chose.

// releaseDeployment is a running factory whose releases a scenario drives.
type releaseDeployment interface {
	deployment
	// setAgentPatch makes the scripted agent leave diff on every run from
	// now on, and waits until the dispatcher runs with it.
	setAgentPatch(t *testing.T, diff string)
	// records lists every issue's record: its state, run and attempt.
	records(t *testing.T) []issueRecord
	// kv reads one entry of the store's key-value table, "" when absent.
	kv(t *testing.T, namespace, key string) string
	// candidateDigest is the digest of the candidate the issue was built into.
	candidateDigest(t *testing.T, issue string) string
	// edgeURL is the base URL of the edge, where the application is served.
	edgeURL() string
}

type issueRecord struct {
	Issue, State, RunID string
	Attempt             int
}

// releaseRecord is the release pipeline's outcome of one candidate, as the
// supervisor records it.
type releaseRecord struct {
	Result string `json:"result"`
	State  string `json:"state"`
	Reason string `json:"reason"`
}

// The sources the fake repository holds next to the foundation files: what
// the scenarios' patches change. examples/hello-app/ is the application,
// ui/dashboard/ the control plane's dashboard, internal/qa/ the trusted QA
// baseline.
var seedFiles = map[string]string{
	"examples/hello-app/main.go": "package main\n\nfunc main() { println(\"hello\") }\n",
	"ui/dashboard/status.html":   "<h1>Factory status</h1>\n<p>All systems nominal.</p>\n",
	"internal/qa/thresholds.txt": "pass_score=85\n",
}

// The patches the scripted agent leaves in the scenarios; setPatch fills
// in their {run} marker.
const (
	greetingDiff = `--- a/examples/hello-app/main.go
+++ b/examples/hello-app/main.go
@@ -1,3 +1,3 @@
 package main

-func main() { println("hello") }
+func main() { println("hello, world") } // {run}
`
	dashboardDiff = `--- a/ui/dashboard/status.html
+++ b/ui/dashboard/status.html
@@ -1,2 +1,2 @@
 <h1>Factory status</h1>
-<p>All systems nominal.</p>
+<p>All systems nominal. Last checked by the factory.</p><!-- {run} -->
`
	qaBaselineDiff = `--- a/internal/qa/thresholds.txt
+++ b/internal/qa/thresholds.txt
@@ -1 +1 @@
-pass_score=85
+pass_score=10 # {run}
`
	bugBody = "The greeting is too terse.\n\n### Definition of Done\n- the app greets the world\n"
)

// fullLifecycle is the path of an issue that is released and closed.
var fullLifecycle = []string{"Ready", "Claimed", "Developing", "QAEvaluation", "ReleaseCandidate", "Canary", "Monitoring", "Verified", "Closed"}

// releaseLimit bounds a wait that spans a scripted run, QA and a release:
// three canary steps of two ten-second windows each, with a loaded machine.
const releaseLimit = 5 * time.Minute

// seedRelease commits the foundation with the seed sources, puts that policy
// in force, waits until no earlier issue is still on its way, and makes the
// scripted agent leave diff.
func seedRelease(t *testing.T, d releaseDeployment, diff string) {
	t.Helper()
	gh := d.github()
	d.approvePolicy(t, gh.commit(t, foundation(t, d.factoryYAML(t))))
	awaitQuiet(t, d)
	setPatch(t, d, diff)
}

// setPatch makes the scripted agent leave diff, its {run} marker replaced
// by a value of this run alone, so no two scenarios build the same commit
// and none releases a candidate an earlier one already made the last known
// good release.
func setPatch(t *testing.T, d releaseDeployment, diff string) {
	t.Helper()
	d.setAgentPatch(t, strings.ReplaceAll(diff, "{run}", fmt.Sprintf("%s-%d", t.Name(), time.Now().UnixNano())))
}

// resting are the states an issue stays in until a person acts.
var resting = []string{"Closed", "Blocked", "Clarifying", "ApprovalPending"}

// awaitQuiet waits until every issue of the factory rests: closed, blocked,
// waiting for a person, or one whose candidate's release has been decided,
// which the release pipeline does once per candidate.
func awaitQuiet(t *testing.T, d releaseDeployment) {
	t.Helper()
	eventuallyWithin(t, releaseLimit, "the issues of earlier tests to come to rest", func() (bool, string) {
		for _, r := range d.records(t) {
			if slices.Contains(resting, r.State) {
				continue
			}
			if d.kv(t, "supervisor", releaseKey(r)) != "" {
				continue
			}
			return false, r.Issue + " is " + r.State
		}
		return true, ""
	})
}

func releaseKey(r issueRecord) string { return fmt.Sprintf("release/%s/%d", r.RunID, r.Attempt) }

// recordOf is the record of one issue.
func recordOf(t *testing.T, d releaseDeployment, issue string) issueRecord {
	t.Helper()
	for _, r := range d.records(t) {
		if r.Issue == issue {
			return r
		}
	}
	return issueRecord{Issue: issue}
}

// awaitRelease waits until the release pipeline decided the issue's
// candidate and returns its outcome.
func awaitRelease(t *testing.T, d releaseDeployment, issue string) releaseRecord {
	t.Helper()
	var rec releaseRecord
	eventuallyWithin(t, releaseLimit, "the release of "+issue+" to be decided", func() (bool, string) {
		r := recordOf(t, d, issue)
		raw := d.kv(t, "supervisor", releaseKey(r))
		if raw == "" {
			return false, issue + " is " + r.State
		}
		return json.Unmarshal([]byte(raw), &rec) == nil, raw
	})
	return rec
}

// openBug opens an issue intake scopes at once and returns its number and
// reference.
func openBug(t *testing.T, d releaseDeployment, title string) (int, string) {
	t.Helper()
	gh := d.github()
	number := gh.openIssue(t, maintainer, fmt.Sprintf("%s (%d)", title, time.Now().UnixNano()), bugBody, "sisf:ready", "kind:bug")
	return number, gh.repo + "#" + fmt.Sprint(number)
}

// waitStateWithin waits until both surfaces show want.
func waitStateWithin(t *testing.T, d deployment, issue, want string, limit time.Duration) {
	t.Helper()
	eventuallyWithin(t, limit, issue+" in "+want+" on the API and the dashboard", func() (bool, string) {
		api, dashboard := states(t, d, issue)
		return api == want && dashboard == want, "API " + api + ", dashboard " + dashboard
	})
}

// journeyEnds fails unless the issue's journey ends with want.
func journeyEnds(t *testing.T, d deployment, issue string, want []string) {
	t.Helper()
	j := d.journey(t, issue)
	if len(j) < len(want) || !slices.Equal(j[len(j)-len(want):], want) {
		t.Fatalf("%s went %v, want it to end %v", issue, j, want)
	}
}

// servedRevision is the revision the application reports through the edge.
func servedRevision(t *testing.T, d releaseDeployment) string {
	t.Helper()
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Get(d.edgeURL() + "/healthz")
	if err != nil {
		t.Fatalf("the application does not answer through the edge: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var h struct{ Revision string }
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err := json.Unmarshal(raw, &h); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("the application's health answered %d: %s", resp.StatusCode, raw)
	}
	return h.Revision
}

// runFullLifecycle takes one issue the whole way: developed, built into a
// candidate, judged by the independent evaluator, released by canary under
// real load, observed, verified, published and closed.
func runFullLifecycle(t *testing.T, d releaseDeployment) {
	seedRelease(t, d, greetingDiff)
	before := servedRevision(t, d)
	load := startTraffic(t, d)
	number, issue := openBug(t, d, "Greet the world")

	waitStateWithin(t, d, issue, "Closed", releaseLimit)
	load.halt()
	journeyEnds(t, d, issue, fullLifecycle)
	rec := awaitRelease(t, d, issue)
	if rec.Result != "closed" {
		t.Fatalf("the release ended %+v", rec)
	}
	candidate := d.candidateDigest(t, issue)
	if got := servedRevision(t, d); got != candidate || got == before {
		t.Fatalf("the application serves %s after the release of %s (it served %s before)", got, candidate, before)
	}
	if sent, failed := load.sent.Load(), load.failed.Load(); sent == 0 || failed != 0 {
		t.Fatalf("the application failed %d of %d requests during the release", failed, sent)
	}
	gh := d.github()
	// The factory closes the issue on GitHub after it records it Closed.
	eventually(t, "GitHub to show the released issue closed", func() (bool, string) {
		state := gh.issueState(t, number)
		return state == "closed", "it is " + state
	})
	if gh.commentsWith(t, number, "Release rolled back") != 0 {
		t.Fatal("a rollback was reported on a release that succeeded")
	}
	// The supervisor merged the candidate's pull request before the close.
	requireLanded(t, gh, number, "examples/hello-app/main.go")
	if !strings.HasPrefix(candidate, "sha256:") {
		t.Fatalf("the candidate digest is %q", candidate)
	}
}
