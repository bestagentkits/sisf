package wiring_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/cmd/factoryd/wiring"
	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
)

// The operator token's write access is checked again while the factory
// runs. Once it is revoked the passes do nothing on GitHub and say why, the
// factory's status shows it, and work resumes only when GitHub confirms
// the access again. A check that cannot reach GitHub is not a revocation.
func TestARevokedOperatorTokenStopsTheFactoryUntilItsAccessIsBack(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := harness.New(t, harness.Options{OperatorToken: true})
	access := func() string {
		t.Helper()
		sync, err := f.Control.GitHubSync(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return sync.TokenAccess
	}
	f.Pass()
	if got := access(); got != "confirmed" {
		t.Fatalf("token access = %q at start, want confirmed", got)
	}

	// An outage of GitHub during a check changes nothing.
	f.Clock.Advance(wiring.OperatorAccessInterval)
	f.GitHub.FailNext(1, http.StatusBadGateway)
	if err := f.Control.Pass(ctx); core.HasCode(err, core.CodePermissionDenied) {
		t.Fatalf("an outage was taken for a revocation: %v", err)
	}
	if got := access(); got != "confirmed" {
		t.Fatalf("token access = %q after an outage, want confirmed", got)
	}
	if sync, _ := f.Control.GitHubSync(ctx); !strings.Contains(sync.TokenAccessDetail, "could not reach GitHub") {
		t.Fatalf("the status does not say the last check could not reach GitHub: %q", sync.TokenAccessDetail)
	}

	if err := f.GitHub.SetWriteAccess(f.Repo, false); err != nil {
		t.Fatal(err)
	}
	f.Clock.Advance(wiring.OperatorAccessInterval)
	if err := f.Control.Pass(ctx); !core.HasCode(err, core.CodePermissionDenied) {
		t.Fatalf("a pass with the write access revoked = %v, want PERMISSION_DENIED", err)
	}
	if got := access(); got != "revoked" {
		t.Fatalf("token access = %q, want revoked", got)
	}
	number := f.OpenIssue("maintainer", "MEMBER", "Health check fails", bugBody, github.LabelReady, github.LabelKindBug)
	before := len(f.GitHub.Requests())
	for range 2 {
		f.Clock.Advance(wiring.DefaultReconcileInterval)
		if err := f.Control.Pass(ctx); !core.HasCode(err, core.CodePermissionDenied) {
			t.Fatalf("a pass inside the check interval acted again: %v", err)
		}
	}
	if after := len(f.GitHub.Requests()); after != before {
		t.Fatalf("the factory made %d GitHub calls with its access revoked", after-before)
	}

	if err := f.GitHub.SetWriteAccess(f.Repo, true); err != nil {
		t.Fatal(err)
	}
	f.Clock.Advance(wiring.OperatorAccessInterval)
	for range 2 {
		f.Pass()
	}
	if got := access(); got != "confirmed" {
		t.Fatalf("token access = %q once restored, want confirmed", got)
	}
	if got := f.Record(number).State; got != core.StateReady {
		t.Fatalf("the issue is %s once access is back, want %s", got, core.StateReady)
	}
}
