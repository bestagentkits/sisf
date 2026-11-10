package acceptance

import (
	"slices"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
)

// greetingDiff changes the application's greeting.
const greetingDiff = `--- a/app/main.go
+++ b/app/main.go
@@ -1,3 +1,3 @@
 package main

-func main() { println("hello") }
+func main() { println("hello, world") }
`

// dashboardDiff changes the read-only status page of the dashboard.
const dashboardDiff = `--- a/ui/dashboard/status.html
+++ b/ui/dashboard/status.html
@@ -1,2 +1,2 @@
 <h1>Factory status</h1>
-<p>All systems nominal.</p>
+<p>All systems nominal. Last checked by the factory.</p>
`

// dashboardRewordDiff words the status page differently.
const dashboardRewordDiff = `--- a/ui/dashboard/status.html
+++ b/ui/dashboard/status.html
@@ -1,2 +1,2 @@
 <h1>Factory status</h1>
-<p>All systems nominal.</p>
+<p>Every system is working.</p>
`

// qaBaselineDiff lowers the QA bar: a change of the trusted control surface.
const qaBaselineDiff = `--- a/internal/qa/thresholds.txt
+++ b/internal/qa/thresholds.txt
@@ -1 +1 @@
-pass_score=85
+pass_score=10
`

// bugBody is an issue intake scopes at once: it carries its definition of done.
const bugBody = "The greeting is too terse.\n\n### Definition of Done\n- the app greets the world\n"

// fullLifecycle is the path of an issue that is released and closed.
var fullLifecycle = []core.State{
	core.StateReady, core.StateClaimed, core.StateDeveloping, core.StateQAEvaluation, core.StateReleaseCandidate,
	core.StateCanary, core.StateMonitoring, core.StateVerified, core.StateClosed,
}

// wantJourneyEnds fails unless issue number went through want, in order, at
// the end of its journey.
func wantJourneyEnds(t *testing.T, f *harness.Factory, number int, want []core.State) {
	t.Helper()
	journey := f.Journey(number)
	if len(journey) < len(want) || !slices.Equal(journey[len(journey)-len(want):], want) {
		t.Fatalf("issue %d went %v, want it to end %v", number, journey, want)
	}
}

// wantSurfaces fails unless the API and the dashboard both show want.
func wantSurfaces(t *testing.T, f *harness.Factory, number int, want core.State) {
	t.Helper()
	if got := f.Record(number).State; got != want {
		t.Fatalf("issue %d is %s, want %s", number, got, want)
	}
	if api, dashboard := f.SurfaceStates(number); api != string(want) || dashboard != string(want) {
		t.Fatalf("the API shows %s and the dashboard %s, want %s", api, dashboard, want)
	}
}

// commentContaining returns the first comment on issue number holding text.
func commentContaining(t *testing.T, f *harness.Factory, number int, text string) string {
	t.Helper()
	comments, err := f.GitHub.Comments(f.Repo, number)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range comments {
		if strings.Contains(c.Body, text) {
			return c.Body
		}
	}
	return ""
}

// wantLabels fails unless the issue's one lifecycle label on GitHub is want.
func wantLabels(t *testing.T, f *harness.Factory, number int, want string) {
	t.Helper()
	issue, err := f.GitHub.Issue(f.Repo, number)
	if err != nil {
		t.Fatal(err)
	}
	var lifecycle []string
	for _, l := range issue.Labels {
		if slices.Contains([]string{github.LabelRunning, github.LabelQA, github.LabelCanary, github.LabelVerified, github.LabelBlocked}, l) {
			lifecycle = append(lifecycle, l)
		}
	}
	if !slices.Equal(lifecycle, []string{want}) {
		t.Fatalf("GitHub shows the lifecycle labels %v, want %s", lifecycle, want)
	}
}
