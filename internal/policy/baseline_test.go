package policy_test

import (
	"context"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/intake/intaketest"
	"github.com/bestagentkits/sisf/internal/policy"
	"github.com/bestagentkits/sisf/pkg/core"
)

func TestBaselinesAndLoaderAreNotBuiltWithoutTheirParts(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	full := policy.BaselinesConfig{State: w.state, Source: w.repo, Approvals: w.approvals, Audit: w.audit, FactoryID: "acme-hello"}
	cases := map[string]func(*policy.BaselinesConfig){
		"no state":         func(c *policy.BaselinesConfig) { c.State = nil },
		"no source":        func(c *policy.BaselinesConfig) { c.Source = nil },
		"no approvals":     func(c *policy.BaselinesConfig) { c.Approvals = nil },
		"no audit log":     func(c *policy.BaselinesConfig) { c.Audit = nil },
		"no factory id":    func(c *policy.BaselinesConfig) { c.FactoryID = "" },
		"bad factory name": func(c *policy.BaselinesConfig) { c.FactoryID = "Not A Factory" },
	}
	for name, mutate := range cases {
		cfg := full
		mutate(&cfg)
		if _, err := policy.NewBaselines(cfg); !core.HasCode(err, core.CodeInvalidArgument) {
			t.Errorf("%s: want %s, got %v", name, core.CodeInvalidArgument, err)
		}
	}
	if _, err := policy.NewLoader(nil, w.baselines); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Errorf("a loader without a source: %v", err)
	}
	if _, err := policy.NewLoader(w.repo, nil); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Errorf("a loader without a baseline: %v", err)
	}
}

func TestTheFirstBaselineIsSetByThePersonWhoOwnsTheFactory(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t)
	_, err := w.baselines.Current(ctx)
	wantCode(t, err, core.CodeNotFound, "baseline before install")

	agent := w.owner()
	agent.Kind = core.ActorAgent
	_, err = w.baselines.Approve(ctx, agent, revA, "")
	wantCode(t, err, core.CodePermissionDenied, "agent setting the first baseline")
	maintainer := w.owner()
	maintainer.Roles = []core.Role{core.RoleMaintainer}
	_, err = w.baselines.Approve(ctx, maintainer, revA, "")
	wantCode(t, err, core.CodePermissionDenied, "maintainer setting the first baseline")
	_, err = w.baselines.Current(ctx)
	wantCode(t, err, core.CodeNotFound, "baseline after refused attempts")

	baseline, err := w.baselines.Approve(ctx, w.owner(), revA, "")
	if err != nil {
		t.Fatal(err)
	}
	if baseline.Revision != revA || baseline.Previous != "" || baseline.ApprovedBy != "user:owner-1" ||
		!baseline.ApprovedAt.Equal(testNow) || baseline.Digest.Validate() != nil {
		t.Fatalf("baseline = %+v", baseline)
	}
	digest, err := w.baselines.Digest(ctx, revA)
	if err != nil || digest != baseline.Digest {
		t.Fatalf("the baseline digest %s is not the digest of its content %s (%v)", baseline.Digest, digest, err)
	}
}

func TestApprovingTheRevisionInForceAgainChangesNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := approved(t)
	first, _ := w.baselines.Current(ctx)
	w.clock.Advance(time.Hour)
	again, err := w.baselines.Approve(ctx, w.owner(), revA, "")
	if err != nil || !again.ApprovedAt.Equal(first.ApprovedAt) || again.Previous != "" {
		t.Fatalf("re-approving the baseline = %+v, %v", again, err)
	}
}

func TestOnlyAReadableFullCommitCanBecomeTheBaseline(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t)
	_, err := w.baselines.Approve(ctx, w.owner(), "main", "")
	wantCode(t, err, core.CodeInvalidArgument, "a branch name")
	_, err = w.baselines.Approve(ctx, w.owner(), revC, "")
	wantCode(t, err, core.CodePolicyHold, "a commit the repository does not have")
	_, err = w.baselines.Digest(ctx, revC)
	wantCode(t, err, core.CodePolicyHold, "digest of an unknown commit")
	_, err = w.baselines.Current(ctx)
	wantCode(t, err, core.CodeNotFound, "baseline after refused attempts")
}

func TestTheBaselineSurvivesARestart(t *testing.T) {
	t.Parallel()
	w := approved(t)
	restarted, err := policy.NewBaselines(policy.BaselinesConfig{
		State: w.state, Source: w.repo, Approvals: w.approvals, Audit: &intaketest.AuditLog{}, FactoryID: "acme-hello",
	})
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := restarted.Current(context.Background())
	if err != nil || baseline.Revision != revA {
		t.Fatalf("baseline after restart = %+v, %v", baseline, err)
	}
}

func TestAChangeThatCannotBeAuditedIsNotMade(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t)
	w.audit.FailWith(core.NewError(core.CodeUnavailable, "audit log is down"))
	_, err := w.baselines.Approve(ctx, w.owner(), revA, "")
	wantCode(t, err, core.CodeUnavailable, "approval without an audit record")
	_, err = w.baselines.Current(ctx)
	wantCode(t, err, core.CodeNotFound, "baseline after the unaudited attempt")
}

func TestALostWriteRaceLeavesTheBaselineAsItWas(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t)
	w.state.ConflictNextWrites(1)
	_, err := w.baselines.Approve(ctx, w.owner(), revA, "")
	wantCode(t, err, core.CodeConflict, "approval that lost a write race")
	_, err = w.baselines.Current(ctx)
	wantCode(t, err, core.CodeNotFound, "baseline after the lost race")
	if _, err := w.baselines.Approve(ctx, w.owner(), revA, ""); err != nil {
		t.Fatalf("the retry was refused: %v", err)
	}
}
