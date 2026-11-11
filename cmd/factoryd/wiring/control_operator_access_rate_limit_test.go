package wiring_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/cmd/factoryd/wiring"
	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/core"
)

// A rate limit on the check of the operator token's access, primary or
// secondary, is a reason to ask again later, not a revocation; a 403 that
// is no rate limit still revokes.
func TestARateLimitOnTheTokenCheckIsNotARevocation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := harness.New(t, harness.Options{OperatorToken: true})
	f.Pass()
	limits := map[string]func(){
		"primary":   func() { f.GitHub.LimitPrimaryNext(1, f.Clock.Now().Add(time.Hour)) },
		"secondary": func() { f.GitHub.LimitNext(1, time.Minute) },
	}
	for _, name := range []string{"primary", "secondary"} {
		f.Clock.Advance(wiring.OperatorAccessInterval)
		limits[name]()
		if err := f.Control.Pass(ctx); core.HasCode(err, core.CodePermissionDenied) {
			t.Fatalf("a %s rate limit was taken for a revocation: %v", name, err)
		}
		sync, err := f.Control.GitHubSync(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if sync.TokenAccess != "confirmed" || !strings.Contains(sync.TokenAccessDetail, "could not reach GitHub") {
			t.Fatalf("after a %s rate limit the status shows %q (%q)", name, sync.TokenAccess, sync.TokenAccessDetail)
		}
	}
	f.Clock.Advance(wiring.OperatorAccessInterval)
	f.GitHub.FailNext(1, http.StatusForbidden)
	if err := f.Control.Pass(ctx); !core.HasCode(err, core.CodePermissionDenied) {
		t.Fatalf("a 403 that is no rate limit = %v, want PERMISSION_DENIED", err)
	}
}
