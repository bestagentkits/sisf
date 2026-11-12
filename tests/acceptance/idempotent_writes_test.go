package acceptance

import (
	"bytes"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/core"
)

// issuesOpened counts the issues the factory opened on GitHub.
func issuesOpened(f *harness.Factory) int {
	n := 0
	for _, r := range f.GitHub.Requests() {
		if r.Method == http.MethodPost && strings.HasSuffix(r.Path, "/issues") && r.Status == http.StatusCreated {
			n++
		}
	}
	return n
}

// A write retried with its idempotency key, on any surface and however
// many times at once, applies once and answers every retry with the first
// answer; the same key for another input is refused.
func TestASampleIssueRetriedWithItsKeyOpensExactlyOneIssue(t *testing.T) {
	f := harness.New(t, harness.Options{})
	c := newClients(t, f, f.SignInOwner())
	before := issuesOpened(f)
	sample := call{Input: raw(`{}`), Key: "setup-sample-1"}

	const callers = 8
	got := make([]outcome, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Go(func() { got[i] = c.on(t, surfaceREST, "setup.sample_issue", sample) })
	}
	wg.Wait()
	for _, surface := range []string{surfaceCLI, surfaceMCP} {
		got = append(got, c.on(t, surface, "setup.sample_issue", sample))
	}
	for i, o := range got {
		if o.Code != "" || !bytes.Equal(o.Output, got[0].Output) {
			t.Fatalf("call %d answered %q %s %s, the first %s", i, o.Code, o.Message, o.Output, got[0].Output)
		}
	}
	if n := issuesOpened(f) - before; n != 1 {
		t.Fatalf("%d calls with one idempotency key opened %d issues, want 1", len(got), n)
	}

	// The same key for another input is a conflict and opens nothing.
	other := call{Input: raw(`{"reason":"other"}`), Key: sample.Key}
	if o := c.on(t, surfaceREST, "factory.emergency_stop", other); o.Code != "" {
		t.Fatalf("a key is per action, yet another action's call answered %q", o.Code)
	}
	if o := c.on(t, surfaceREST, "factory.emergency_stop", call{Input: raw(`{"reason":"changed"}`), Key: sample.Key}); o.Code != core.CodeConflict {
		t.Fatalf("the same key for another input answered %q %s", o.Code, o.Message)
	}
	if stops, _ := f.Supervisor.Recorded(); len(stops) != 1 {
		t.Fatalf("the supervisor was asked to stop %d times, want once", len(stops))
	}
}
