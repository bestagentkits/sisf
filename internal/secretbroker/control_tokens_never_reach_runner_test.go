package secretbroker_test

import (
	"context"
	"testing"

	"github.com/bestagentkits/sisf/internal/secretbroker"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/secrets"
)

// The GitHub App key, the Owner token and the signing keys are never
// granted to a run, whatever the request calls them, and nothing about
// them is disclosed in the refusal.
func TestControlPlaneCredentialsAreNeverGrantedToARun(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	misstated := func(ref secrets.SecretRef) secrets.SecretRef {
		ref.Class, ref.Zone = secrets.ClassRun, core.ZoneAgent
		return ref
	}
	var errs []error
	for name, ref := range map[string]secrets.SecretRef{
		"github app key":                  githubApp,
		"owner token":                     owner,
		"signing key":                     signing,
		"github app key called a run key": misstated(githubApp),
		"owner token called a run key":    misstated(owner),
		"signing key called a run key":    misstated(signing),
		"run secret of the control zone":  controlRun,
		"control run secret relabeled":    misstated(controlRun),
	} {
		_, err := f.broker.Issue(ctx, envGrant("run-0001", ref, "GITHUB_TOKEN"))
		wantCode(t, err, core.CodePermissionDenied, name)
		errs = append(errs, err)
	}
	if denied := f.audit.Find("secrets.grant.issue", core.OutcomeDenied); len(denied) != 8 {
		t.Fatalf("%d refusals were audited, want 8", len(denied))
	}
	if issued := f.audit.Find("secrets.grant.issue", core.OutcomeExecuted); len(issued) != 0 {
		t.Fatalf("%d grants were issued", len(issued))
	}
	f.assertNoValueLeaked(t, errs...)
}

func TestTheBrokerNeedsAProvider(t *testing.T) {
	t.Parallel()
	if _, err := secretbroker.New(secretbroker.Config{}); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Fatalf("want INVALID_ARGUMENT, got %v", err)
	}
}
