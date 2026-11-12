package release

import (
	"context"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
)

// The code that decides who is the Owner and who may approve is outside
// the supervisor's link closure, yet a factory that rewrote it could grant
// itself the approval a governance change needs: a candidate that changes
// only it is refused for approval and never merged.
func TestACandidateThatChangesOnlyIdentityOrAccessCodeIsRefusedForApproval(t *testing.T) {
	t.Parallel()
	for i, changed := range []string{"internal/auth/x.go", "internal/rbac/x.go", "internal/oauthas/x.go", "pkg/identity/x.go"} {
		t.Run(changed, func(t *testing.T) {
			t.Parallel()
			w := newWorld(t)
			lander := &countingLander{}
			w.cfg.Lander = lander
			issue, run := issueRef(70+i), core.RunID("run-identity")
			w.seedReleaseCandidate(issue, run, firstBuild)
			req := w.request(issue, run, firstBuild)
			req.Candidate.ChangedPaths = []string{changed}
			req.Receipt = w.keys.receiptFor(t, req.Candidate, nil)
			out, err := w.pipeline().Release(context.Background(), req)
			requireRefusedUntouched(t, w, issue, out, err, core.CodeApprovalRequired)
			if lander.lands.Load() != 0 {
				t.Fatal("a refused candidate was merged")
			}
		})
	}
}
