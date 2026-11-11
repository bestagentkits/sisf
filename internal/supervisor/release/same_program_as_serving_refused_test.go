package release

import (
	"context"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
)

// A candidate that builds exactly the program that already serves, as one
// that changes only tests or documentation, has nothing to deploy. It is
// not held, which the Owner's retry would only repeat: its issue is
// blocked as refused with ALREADY_EXISTS, and nothing is staged or merged.
func TestACandidateThatBuildsTheServingProgramIsRefusedNotHeld(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	lander := &countingLander{}
	w.cfg.Lander = lander
	issue, run := issueRef(90), core.RunID("run-same-program")
	same := core.Digest(lkgDigest)
	w.seedReleaseCandidate(issue, run, same)
	out, err := w.pipeline().Release(context.Background(), w.request(issue, run, same))
	requireRefusedUntouched(t, w, issue, out, err, core.CodeAlreadyExists)
	if lander.lands.Load() != 0 {
		t.Fatal("a candidate with nothing to deploy was merged")
	}
}
