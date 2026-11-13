//go:build e2e

package e2e

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// greetingPath is the file every release of the scenario changes.
const greetingPath = "examples/hello-app/main.go"

// releaseInARowWithoutTheOwner releases two more issues of the unit the
// factory just released, one after the other, with no Owner action between
// them: each run is built on the merge of the release before it, which the
// supervisor followed while the foundation files stayed as approved. The
// policy in force never moves.
func releaseInARowWithoutTheOwner(t *testing.T, d releaseDeployment) {
	t.Helper()
	approved := policyInForce(t, d)
	for n := 2; n <= 3; n++ {
		number, issue := releaseNext(t, d, n)
		if got := policyInForce(t, d); got != approved {
			t.Fatalf("release %d moved the policy in force from %s to %s", n, approved, got)
		}
		base := buildBaseOf(t, d)
		head := d.github().mainHead(t)
		if base.Commit != head || base.Revision != approved || base.Stopped != nil {
			t.Fatalf("after release %d (%s) the commit to build on is %+v, main is at %s", n, issue, base, head)
		}
		requireLanded(t, d.github(), number, greetingPath)
	}
}

// stopUntilTheOwnerApproves changes a foundation file on main outside the
// factory. The next release still serves, but the supervisor does not merge
// it into a main whose content the Owner has not approved: its pull request
// is left for the Owner and the factory stops. An issue opened then is not
// even claimed, so nothing is coded that could not be released. Once the
// Owner merged the release and approved a revision that contains the
// change, the waiting issue's run is superseded as after any policy change,
// and the Owner's retry releases it.
func stopUntilTheOwnerApproves(t *testing.T, d *standaloneDeployment) {
	t.Helper()
	gh := d.github()
	agents := gh.fileAt(t, gh.mainHead(t), "AGENTS.md")
	changed := gh.commitOnMainKeeping(t, map[string]string{"AGENTS.md": agents + "\n- Changed on main, outside the factory.\n"},
		"Change the execution guidance outside the factory")
	number, issue := releaseNext(t, d, 4)
	if merged := gh.mergedCandidates(t, number); len(merged) != 0 {
		t.Fatalf("%s was merged into a main whose foundation content the Owner has not approved: %v", issue, merged)
	}
	base := buildBaseOf(t, d)
	if base.Stopped == nil || base.Stopped.Merge != changed || !strings.Contains(base.Stopped.Reason, "AGENTS.md") {
		t.Fatalf("after a release with an unapproved main the commit to build on is %+v", base)
	}

	setPatch(t, d, nextGreetingDiff(t, d, 5))
	_, waiting := openBug(t, d, "Greet the world once the Owner approved")
	waitStateWithin(t, d, waiting, "Ready", releaseLimit)
	// The dispatcher claims a Ready issue within a few ticks; this one it
	// must not claim while the factory is stopped.
	quiet := time.Now().Add(20 * time.Second)
	for time.Now().Before(quiet) {
		if r := recordOf(t, d, waiting); r.State != "Ready" || r.RunID != "" {
			t.Fatalf("%s was taken up while the factory was stopped: %+v", waiting, r)
		}
		time.Sleep(time.Second)
	}

	// The Owner merges the release the factory left open, then approves
	// main as it stands.
	pr := gh.openCandidate(t, number)
	gh.must(t, http.StatusOK, "PUT", fmt.Sprintf("/repos/%s/pulls/%d/merge", gh.repo, pr), maintainer,
		map[string]any{"merge_method": "merge", "commit_title": "The Owner merges release 4"})
	ownerMovesBaseline(t, d, gh.mainHead(t))
	waitStateWithin(t, d, waiting, "Blocked", releaseLimit)
	// The Owner's merge changed the greeting, so the retry is coded on main
	// as it stands now.
	setPatch(t, d, nextGreetingDiff(t, d, 5))
	// The release is judged on users' traffic, as every release before it.
	load := startTraffic(t, d)
	if out, err := d.factoryd("control", "issue", "retry", "--issue", waiting); err != nil {
		t.Fatalf("the Owner's retry of %s: %v\n%s", waiting, err, out)
	}
	waitStateWithin(t, d, waiting, "Closed", releaseLimit)
	load.halt()
	if released := awaitRelease(t, d, waiting); released.Result != "closed" {
		t.Fatalf("the release after the Owner's approval ended %+v", released)
	}
	if got := d.candidateDigest(t, waiting); servedRevision(t, d) != got {
		t.Fatalf("the application does not serve %s, released after the Owner's approval", got)
	}
}

// openCandidate is the open candidate pull request of issue number.
func (s simGitHub) openCandidate(t *testing.T, number int) int {
	t.Helper()
	var pulls []struct {
		Number int
		Body   string
		State  string
		Head   struct{ Ref string }
	}
	out := s.must(t, http.StatusOK, "GET", "/repos/"+s.repo+"/pulls?state=open&per_page=100", "", nil)
	if err := json.Unmarshal(out, &pulls); err != nil {
		t.Fatal(err)
	}
	for _, pr := range pulls {
		if strings.HasPrefix(pr.Head.Ref, "sisf/") && strings.Contains(pr.Body, fmt.Sprintf("Refs #%d\n", number)) {
			return pr.Number
		}
	}
	t.Fatalf("no open candidate pull request of #%d: %+v", number, pulls)
	return 0
}

// releaseNext opens the n-th issue of the scenario and waits until it is
// released and closed, under load, without anyone acting.
func releaseNext(t *testing.T, d releaseDeployment, n int) (int, string) {
	t.Helper()
	serving := servedRevision(t, d)
	setPatch(t, d, nextGreetingDiff(t, d, n))
	load := startTraffic(t, d)
	number, issue := openBug(t, d, fmt.Sprintf("Greet the world, take %d", n))
	waitStateWithin(t, d, issue, "Closed", releaseLimit)
	load.halt()
	journeyEnds(t, d, issue, fullLifecycle)
	if released := awaitRelease(t, d, issue); released.Result != "closed" {
		t.Fatalf("release %d ended %+v", n, released)
	}
	candidate := d.candidateDigest(t, issue)
	if got := servedRevision(t, d); got != candidate || got == serving {
		t.Fatalf("after release %d the application serves %s (candidate %s, before %s)", n, got, candidate, serving)
	}
	if sent, failed := load.sent.Load(), load.failed.Load(); sent == 0 || failed != 0 {
		t.Fatalf("the application failed %d of %d requests during release %d", failed, sent, n)
	}
	gh := d.github()
	eventually(t, "GitHub to show release "+fmt.Sprint(n)+" closed", func() (bool, string) {
		state := gh.issueState(t, number)
		return state == "closed", "it is " + state
	})
	return number, issue
}

// nextGreetingDiff is a patch of the greeting as main holds it now, so it
// applies to the merge of the release before it.
func nextGreetingDiff(t *testing.T, d releaseDeployment, n int) string {
	t.Helper()
	gh := d.github()
	lines := strings.Split(strings.TrimSuffix(gh.fileAt(t, gh.mainHead(t), greetingPath), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("%s on main has %d lines, want 3", greetingPath, len(lines))
	}
	return fmt.Sprintf("--- a/%[1]s\n+++ b/%[1]s\n@@ -1,3 +1,3 @@\n %[2]s\n %[3]s\n-%[4]s\n+func main() { println(\"hello, world %[5]d\") } // {run}\n",
		greetingPath, lines[0], lines[1], lines[2], n)
}

// buildBase is the supervisor's record of the commit to build on.
type buildBase struct {
	Revision string `json:"revision"`
	Commit   string `json:"commit"`
	Stopped  *struct {
		Merge  string `json:"merge"`
		Reason string `json:"reason"`
	} `json:"stopped"`
}

func buildBaseOf(t *testing.T, d deployment) buildBase {
	t.Helper()
	var base buildBase
	raw := d.kv(t, "control.policy", "build-base")
	if raw == "" {
		return base
	}
	if err := json.Unmarshal([]byte(raw), &base); err != nil {
		t.Fatalf("the commit to build on cannot be read: %v: %s", err, raw)
	}
	return base
}

// fileAt reads path at commit through the contents API.
func (s simGitHub) fileAt(t *testing.T, commit, path string) string {
	t.Helper()
	var file struct{ Content string }
	if err := json.Unmarshal(s.must(t, http.StatusOK, "GET", "/repos/"+s.repo+"/contents/"+path+"?ref="+commit, "", nil), &file); err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(file.Content, "\n", ""))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// commitOnMainKeeping commits files on main's head, keeping every other
// path, as a push to main does.
func (s simGitHub) commitOnMainKeeping(t *testing.T, files map[string]string, message string) string {
	t.Helper()
	head := s.mainHead(t)
	var commit struct {
		Tree struct{ SHA string } `json:"tree"`
	}
	if err := json.Unmarshal(s.must(t, http.StatusOK, "GET", "/repos/"+s.repo+"/git/commits/"+head, "", nil), &commit); err != nil {
		t.Fatal(err)
	}
	entries := make([]map[string]any, 0, len(files))
	for path, content := range files {
		entries = append(entries, map[string]any{"path": path, "mode": "100644", "type": "blob", "content": content})
	}
	var tree, made struct{ SHA string }
	if err := json.Unmarshal(s.must(t, http.StatusCreated, "POST", "/repos/"+s.repo+"/git/trees", "",
		map[string]any{"base_tree": commit.Tree.SHA, "tree": entries}), &tree); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(s.must(t, http.StatusCreated, "POST", "/repos/"+s.repo+"/git/commits", "",
		map[string]any{"message": message, "tree": tree.SHA, "parents": []string{head}}), &made); err != nil {
		t.Fatal(err)
	}
	s.must(t, http.StatusOK, "PATCH", "/repos/"+s.repo+"/git/refs/heads/main", "", map[string]any{"sha": made.SHA})
	return made.SHA
}
