//go:build e2e

// Package e2e checks a running factory from the outside: real processes or
// containers, a real state store, and the fake GitHub as the only stand-in.
package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// deployment is one way of running the factory: Compose or Standalone.
type deployment interface {
	github() simGitHub
	// control is the control plane's base URL and the Owner token.
	control(t *testing.T) (base, ownerToken string)
	// factoryYAML is the install's manifest, committed as the policy file.
	factoryYAML(t *testing.T) []byte
	// approvePolicy puts revision in force, as the Owner on the factory's host.
	approvePolicy(t *testing.T, revision string)
	// dispatcherHealth is the dispatcher's /healthz, or an error while it is down.
	dispatcherHealth(t *testing.T) (health, error)
	// killDispatcher stops the dispatcher at once; startDispatcher starts it again.
	killDispatcher(t *testing.T)
	startDispatcher(t *testing.T)
	// report makes a report on a run with the agent zone's command line and
	// returns what it printed.
	report(t *testing.T, issue, run, owner string, token int64) (string, error)
	// journey lists the states an issue went through, from the store's journal.
	journey(t *testing.T, issue string) []string
}

// health is the part of the dispatcher's /healthz the scenario reads.
type health struct {
	Owner string `json:"owner"`
	Runs  []struct {
		Issue        core.IssueRef `json:"issue"`
		RunID        string        `json:"run_id"`
		FencingToken int64         `json:"fencing_token"`
	} `json:"runs"`
}

const (
	vagueBody = "The health check is flaky."
	replyBody = "It fails when the database is slow.\n\n### Definition of Done\n- /healthz answers 200 within one second while the database is slow\n"
	// waitLimit bounds every wait; the passes run every second or two.
	waitLimit = 90 * time.Second
)

// eventually polls cond until it holds or waitLimit passes.
func eventually(t *testing.T, what string, cond func() (bool, string)) {
	t.Helper()
	deadline := time.Now().Add(waitLimit)
	last := ""
	for time.Now().Before(deadline) {
		ok, detail := cond()
		if ok {
			return
		}
		last = detail
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s; last seen: %s", what, last)
}

// moduleRoot is the repository checkout the test runs from.
func moduleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the test source")
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(file)))
}

// foundation is the policy commit: the repository's foundation files with
// the install's own manifest.
func foundation(t *testing.T, manifest []byte) map[string]string {
	t.Helper()
	files := map[string]string{".sisf/factory.yaml": string(manifest)}
	for _, path := range []string{"AGENTS.md", "REVIEW.md", "DECISIONS.md", "FEATURES.md"} {
		raw, err := os.ReadFile(filepath.Join(moduleRoot(t), path))
		if err != nil {
			t.Fatal(err)
		}
		files[path] = string(raw)
	}
	return files
}

// states reads the issue's state from the API and the dashboard.
func states(t *testing.T, d deployment, issue string) (api, dashboard string) {
	t.Helper()
	base, owner := d.control(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	api, dashboard, err := harness.SurfaceStates(ctx, base, owner, issue)
	if err != nil {
		return "error: " + err.Error(), ""
	}
	return api, dashboard
}

// waitState waits until both surfaces show want.
func waitState(t *testing.T, d deployment, issue, want string) {
	t.Helper()
	eventually(t, issue+" in "+want+" on the API and the dashboard", func() (bool, string) {
		api, dashboard := states(t, d, issue)
		return api == want && dashboard == want, "API " + api + ", dashboard " + dashboard
	})
}

// requireFreeRunSlot stops the test early when the factory has no room for
// another run: the runs of earlier tests on the same install keep their
// slots until the scripted agent finishes its work.
func requireFreeRunSlot(t *testing.T, d deployment, manifestYAML []byte) {
	t.Helper()
	m, err := manifest.Parse(manifestYAML)
	if err != nil {
		t.Fatal(err)
	}
	var h health
	eventually(t, "the dispatcher's health", func() (bool, string) {
		h, err = d.dispatcherHealth(t)
		return err == nil, fmt.Sprint(err)
	})
	if len(h.Runs) >= m.Limits.Concurrency {
		t.Fatalf("all %d run slots of this install are held by earlier runs (%d); wait until the scripted agent "+
			"finishes them or start from fresh state with `sisf down -v` and `sisf up`", m.Limits.Concurrency, len(h.Runs))
	}
}

func runOf(h health, issue string) (string, int64, bool) {
	for _, r := range h.Runs {
		if r.Issue.String() == issue {
			return r.RunID, r.FencingToken, true
		}
	}
	return "", 0, false
}

func count(states []string, want string) int {
	n := 0
	for _, s := range states {
		if s == want {
			n++
		}
	}
	return n
}

// runLifecycle takes one issue from a labeled request to a run in progress
// through a clarification, with its webhooks late, reordered and repeated,
// then kills the dispatcher mid-run and checks that the run resumes and that
// a report under the dead incarnation's lease is refused.
func runLifecycle(t *testing.T, d deployment) {
	gh := d.github()
	manifestYAML := d.factoryYAML(t)
	d.approvePolicy(t, gh.commit(t, foundation(t, manifestYAML)))
	requireFreeRunSlot(t, d, manifestYAML)

	// Every delivery of the new issue is held, then let through newest
	// first, and each one twice.
	before := len(gh.deliveries(t))
	gh.faults(t, map[string]any{"hold": true})
	number := gh.openIssue(t, maintainer, fmt.Sprintf("Health check is flaky (%d)", time.Now().UnixNano()), vagueBody, "sisf:ready", "kind:bug")
	issue := gh.repo + "#" + fmt.Sprint(number)
	held := len(gh.deliveries(t)) - before
	if held < 1 {
		t.Fatalf("opening the issue produced no delivery")
	}
	gh.faults(t, map[string]any{"duplicate": held, "release": "reversed"})

	waitState(t, d, issue, "Clarifying")
	eventually(t, "the clarifying question", func() (bool, string) {
		n := gh.commentsWith(t, number, "More information needed")
		return n == 1, fmt.Sprintf("%d questions", n)
	})

	gh.comment(t, number, maintainer, replyBody)
	waitState(t, d, issue, "Developing")
	if n := gh.commentsWith(t, number, "Scope of Work v1"); n != 1 {
		t.Fatalf("scope v1 was posted %d times", n)
	}
	journey := d.journey(t, issue)
	if !slices.Contains(journey, "Clarifying") || !strings.HasSuffix(strings.Join(journey, ">"), "Ready>Claimed>Developing") {
		t.Fatalf("the issue went %v", journey)
	}

	// Every delivery so far arrives once more: nothing changes.
	gh.redeliverAll(t, gh.deliveries(t))
	var h health
	eventually(t, "the dispatcher to report the run", func() (bool, string) {
		var err error
		h, err = d.dispatcherHealth(t)
		_, _, ok := runOf(h, issue)
		return err == nil && ok, fmt.Sprintf("%+v %v", h, err)
	})
	run, stale, _ := runOf(h, issue)
	time.Sleep(5 * time.Second) // the redeliveries are processed within a few passes
	if j := d.journey(t, issue); count(j, "Claimed") != 1 || count(j, "Developing") != 1 {
		t.Fatalf("redelivered webhooks moved the issue again: %v", j)
	}
	if gh.commentsWith(t, number, "More information needed") != 1 || gh.commentsWith(t, number, "Scope of Work v1") != 1 {
		t.Fatal("a redelivered webhook posted a comment twice")
	}

	// The dispatcher dies mid-run and comes back.
	d.killDispatcher(t)
	d.startDispatcher(t)
	eventually(t, "the restarted dispatcher to resume the run", func() (bool, string) {
		next, err := d.dispatcherHealth(t)
		r, token, ok := runOf(next, issue)
		return err == nil && ok && r == run && token > stale, fmt.Sprintf("%+v %v", next, err)
	})
	out, err := d.report(t, issue, run, h.Owner, stale)
	if err == nil || !strings.Contains(out, "STALE_FENCING_TOKEN") {
		t.Fatalf("a report under the dead dispatcher's lease was not fenced: %v\n%s", err, out)
	}
	waitState(t, d, issue, "Developing")
	if j := d.journey(t, issue); count(j, "Claimed") != 1 {
		t.Fatalf("the resumed run was claimed again: %v", j)
	}
}
