package wiring_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/policy"
	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
)

// commitPolicy commits the repository's foundation files, which the harness
// committed as the policy in force, once more with note appended to the
// decision rules, and returns the new revision.
func commitPolicy(t *testing.T, f *harness.Factory, note string) core.CommitSHA {
	t.Helper()
	files := map[string]string{}
	for _, path := range []string{policy.PathFactory, policy.PathAgents, policy.PathReview, policy.PathDecisions, policy.PathFeatures} {
		content, err := os.ReadFile(filepath.Join("..", "..", "..", filepath.FromSlash(path)))
		if err != nil {
			t.Fatal(err)
		}
		files[path] = string(content)
	}
	files[policy.PathDecisions] = strings.TrimRight(files[policy.PathDecisions], "\n") + "\n\n" + note + "\n"
	sha, err := f.GitHub.Commit(f.Repo, files)
	if err != nil {
		t.Fatal(err)
	}
	return core.CommitSHA(sha)
}

// The Owner files the request for a new policy revision, approves it, and
// puts the revision in force with it. An approval of another revision does
// not do it and stays unspent.
func TestTheOwnerRequestsApprovesAndPutsANewPolicyInForce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := harness.New(t, harness.Options{})
	next := commitPolicy(t, f, "A note that changes the content.")
	other := commitPolicy(t, f, "Another note.")

	request, err := f.Control.RequestPolicyChange(ctx, next)
	if err != nil {
		t.Fatal(err)
	}
	if request.Action != policy.ActionApproveRevision || request.PolicyRevision != f.PolicyRevision {
		t.Fatalf("the request = %+v", request)
	}
	wrong, err := f.Control.RequestPolicyChange(ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []approvals.Request{request, wrong} {
		if _, err := f.Control.Approvals.Submit(ctx, owner(f), submission(r, approvals.DecisionApprove)); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := f.Control.ApproveBaseline(ctx, next, wrong.ID); !core.HasCode(err, core.CodeApprovalRequired) {
		t.Fatalf("an approval of another revision was accepted: %v", err)
	}
	if r, _ := f.Control.Approvals.Get(ctx, wrong.ID); r.Status != approvals.StatusApproved {
		t.Fatalf("the refused approval was spent: %s", r.Status)
	}
	if _, err := f.Control.ApproveBaseline(ctx, next, request.ID); err != nil {
		t.Fatalf("the approved change was refused: %v", err)
	}
	if current, err := f.Control.Baselines.Current(ctx); err != nil || current.Revision != next {
		t.Fatalf("the policy in force is %+v, %v", current, err)
	}
}
