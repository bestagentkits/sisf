package stages

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/internal/policy"
	"github.com/bestagentkits/sisf/internal/supervisor/release"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// stoppedKeeper is the keeper of the commit to build on of a factory that
// stopped building on main: it accepts no base and refuses every merge.
type stoppedKeeper struct{ checked []policy.MainCheck }

func (k *stoppedKeeper) Accept(context.Context, core.CommitSHA) (core.CommitSHA, error) {
	return "", core.NewError(core.CodePolicyHold, "the factory stopped building on main")
}

func (k *stoppedKeeper) CheckMain(_ context.Context, actor core.Actor, check policy.MainCheck) error {
	if actor.ID != policy.SupervisorActorID {
		return core.NewError(core.CodePermissionDenied, "not the supervisor")
	}
	k.checked = append(k.checked, check)
	return core.NewError(core.CodeApprovalRequired, "the factory stopped building on main at "+string(check.Head)+": main changes AGENTS.md, which only the Owner approves")
}

func (k *stoppedKeeper) Follow(context.Context, core.Actor, policy.FollowProof) (policy.BuildBase, error) {
	return policy.BuildBase{}, core.NewError(core.CodeApprovalRequired, "stopped")
}

// The supervisor merges into no main whose content the Owner has not
// approved: while the factory is stopped the pull request is left for the
// Owner with the reason, and nothing is merged.
func TestNoReleaseMergesIntoAMainTheOwnerHasNotApproved(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		ctx := context.Background()
		w := landingFactory(t, db, "main unapproved")
		keeper := &stoppedKeeper{}
		w.prs.Bases = keeper
		head := mainHeadOf(t, w)

		landing, err := w.prs.Land(ctx, w.req)
		if err != nil || landing.Merged || w.data.Merges() != 0 || w.mainHasTheChange() {
			t.Fatalf("Land = %+v, %v after %d merges", landing, err, w.data.Merges())
		}
		if !strings.Contains(landing.Summary, "AGENTS.md") {
			t.Fatalf("the pull request was left without the reason: %q", landing.Summary)
		}
		if len(keeper.checked) != 1 || keeper.checked[0].Head != head || keeper.checked[0].Run != w.req.RunID {
			t.Fatalf("main was checked as %+v, want its head %s for %s", keeper.checked, head, w.req.RunID)
		}
	})
}

// The QA proof names the base the supervisor accepted, never the receipt's
// own claim: a keeper that accepts another commit than the receipt names
// makes the guard refuse, and one that accepts none gives no proof.
func TestTheQAProofNamesTheBaseTheSupervisorAccepted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	revision := core.CommitSHA("1111111111111111111111111111111111111111")
	claimed := core.CommitSHA("2222222222222222222222222222222222222222")
	refs := func(context.Context) (core.PolicyRefs, error) { return core.PolicyRefs{PolicySHA: revision}, nil }
	receipt := core.QAReceipt{BaselineSHA: claimed}

	stage := &QAStage{Policy: refs, Bases: acceptingKeeper{accepts: revision}}
	proof, err := stage.proof(ctx, receipt)
	if err != nil || proof.BaselineSHA != revision {
		t.Fatalf("proof = %+v, %v; want the accepted %s", proof, err, revision)
	}
	if _, err := (&QAStage{Policy: refs, Bases: &stoppedKeeper{}}).proof(ctx, receipt); !core.HasCode(err, core.CodePolicyHold) {
		t.Fatalf("a refused base gave a proof: %v", err)
	}
	if _, err := (&QAStage{Policy: refs}).proof(ctx, receipt); !core.HasCode(err, core.CodePolicyHold) {
		t.Fatalf("without a keeper a base other than the policy revision gave a proof: %v", err)
	}
}

// acceptingKeeper accepts every base as the one commit it names.
type acceptingKeeper struct{ accepts core.CommitSHA }

func (k acceptingKeeper) Accept(context.Context, core.CommitSHA) (core.CommitSHA, error) {
	return k.accepts, nil
}

func (acceptingKeeper) CheckMain(context.Context, core.Actor, policy.MainCheck) error { return nil }

func (acceptingKeeper) Follow(context.Context, core.Actor, policy.FollowProof) (policy.BuildBase, error) {
	return policy.BuildBase{}, nil
}

// While the factory is stopped the build stage accepts no base, the policy
// revision included: the run ends as a superseded one, blocked for the
// Owner, and is never built or judged.
func TestTheBuildStageBuildsNothingWhileTheFactoryIsStopped(t *testing.T) {
	t.Parallel()
	revision := core.CommitSHA("1111111111111111111111111111111111111111")
	stage := &BuildStage{Bases: &stoppedKeeper{}}
	for _, base := range []core.CommitSHA{"", revision} {
		_, err := stage.runBase(context.Background(), handoff.PatchDoc{Base: base}, core.PolicyRefs{PolicySHA: revision})
		if !policySuperseded(err) || !strings.Contains(err.Error(), "stopped building on main") {
			t.Fatalf("a run coded on %q while stopped: %v; want it ended for the Owner", base, err)
		}
	}
}

// nopAudit accepts every record.
type nopAudit struct{}

func (nopAudit) Append(context.Context, core.AuditEvent) (core.AuditEvent, error) {
	return core.AuditEvent{}, nil
}

func (nopAudit) Verify(context.Context) error { return nil }

// With an audit log the keeper of the commit to build on reaches every
// place that needs it: the follow after a merge, the release's base check
// and the merge's check of main. Without one there is none, and only the
// policy revision is built on.
func TestTheCommitToBuildOnIsWiredIntoReleaseAndMerge(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		cfg := Config{State: db.Open(t, core.ZoneControl), FactoryID: sqlstoretest.Factory}
		tree := Tree{}
		rc := release.Config{Audit: nopAudit{}, Commits: tree}
		prs := &CandidatePRs{}
		keeper, err := wireCommitToBuildOn(cfg, &rc, tree, prs)
		if err != nil || keeper == nil {
			t.Fatalf("no keeper with an audit log: %v", err)
		}
		if rc.Follower == nil || rc.Bases == nil || prs.Bases == nil {
			t.Fatalf("the keeper is not wired: follower %v, release bases %v, merge check %v", rc.Follower, rc.Bases, prs.Bases)
		}

		bare, prsBare := release.Config{Commits: tree}, &CandidatePRs{}
		if keeper, err := wireCommitToBuildOn(cfg, &bare, tree, prsBare); err != nil || keeper != nil || bare.Follower != nil || prsBare.Bases != nil {
			t.Fatalf("without an audit log a keeper was wired: %v, %v", keeper, err)
		}
	})
}

// verifiedListing is a store whose records list, besides what it holds,
// the Verified records of other runs, as the store lists releases that are
// verified and not closed yet.
type verifiedListing struct {
	state.Store
	verified []core.StateRecord
}

func (s verifiedListing) View(ctx context.Context, fn func(context.Context, state.Tx) error) error {
	return s.Store.View(ctx, func(ctx context.Context, tx state.Tx) error { return fn(ctx, listingTx{Tx: tx, verified: s.verified}) })
}

type listingTx struct {
	state.Tx
	verified []core.StateRecord
}

func (t listingTx) ListRecords(ctx context.Context, filter state.RecordFilter) ([]core.StateRecord, error) {
	records, err := t.Tx.ListRecords(ctx, filter)
	return append(records, t.verified...), err
}

// Before a merge, main is checked with every other release that was merged
// or is being merged and not closed yet, so a merge whose follow lags is
// followed before another is made on top of it. A pull request left for
// the Owner is no landing, nor is the unreadable record of another run:
// that run reports it, and the others are still merged.
func TestMainIsCheckedWithTheLandingsWhoseFollowMayLag(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		ctx := context.Background()
		w := landingFactory(t, db, "landings in flight")
		keeper := &stoppedKeeper{}
		w.prs.Bases = keeper
		merged := handoff.CandidatePR{RunID: "run-0007", Number: 7, State: handoff.LandingMerged,
			Commit: "7777777777777777777777777777777777777777", MergeCommit: "8888888888888888888888888888888888888888"}
		merging := handoff.CandidatePR{RunID: "run-0008", Number: 8, State: handoff.LandingPending, Commit: "9999999999999999999999999999999999999999"}
		left := handoff.CandidatePR{RunID: "run-0009", Number: 9, State: handoff.LandingLeftOpen, Commit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
		// A pull request record without its number, and one whose build
		// record names another commit: neither can be read.
		numberless := handoff.CandidatePR{RunID: "run-0010", State: handoff.LandingPending, Commit: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
		forgedBuild := handoff.CandidatePR{RunID: "run-0011", Number: 11, State: handoff.LandingMerged,
			Commit: "cccccccccccccccccccccccccccccccccccccccc", MergeCommit: "dddddddddddddddddddddddddddddddddddddddd"}
		all := []handoff.CandidatePR{merged, merging, left, numberless, forgedBuild}
		if err := w.prs.State.Update(ctx, func(ctx context.Context, tx state.Tx) error {
			for _, pr := range all {
				if err := handoff.PutCandidatePR(ctx, tx, pr); err != nil {
					return err
				}
			}
			if err := handoff.PutDoc(ctx, tx, handoff.BuildNamespace, handoff.BuildKey(forgedBuild.Commit), handoff.BuildRecord{Commit: merged.Commit, Unit: "app"}); err != nil {
				return err
			}
			return handoff.PutDoc(ctx, tx, handoff.BuildNamespace, handoff.BuildKey(merged.Commit), handoff.BuildRecord{Commit: merged.Commit, Unit: "app"})
		}); err != nil {
			t.Fatal(err)
		}
		var verified []core.StateRecord
		for _, pr := range all {
			verified = append(verified, core.StateRecord{State: core.StateVerified, RunID: pr.RunID})
		}
		w.prs.State = verifiedListing{Store: w.prs.State, verified: verified}

		if _, err := w.prs.Land(ctx, w.req); err != nil {
			t.Fatal(err)
		}
		want := []policy.FollowProof{
			{Merge: merged.MergeCommit, Candidate: merged.Commit, Run: merged.RunID, Unit: "app"},
			{Candidate: merging.Commit, Run: merging.RunID},
		}
		if len(keeper.checked) != 1 || !slices.Equal(keeper.checked[0].Landings, want) {
			t.Fatalf("main was checked with the landings %+v, want %+v", keeper.checked, want)
		}
		// The run whose record cannot be read meets the error itself.
		err := w.prs.State.View(ctx, func(ctx context.Context, tx state.Tx) error {
			_, err := handoff.ReadCandidatePR(ctx, tx, numberless.RunID)
			return err
		})
		if !core.HasCode(err, core.CodeTampered) {
			t.Fatalf("the unreadable record of %s reads as %v", numberless.RunID, err)
		}
	})
}

// The paths only the Owner changes, by which a merge is followed and a base
// accepted, are the governance's trusted control surface as well as what an
// agent's patch may not change.
func TestTheOwnerOnlyPathsAreTheTrustedControlSurface(t *testing.T) {
	t.Parallel()
	for _, p := range []string{"go.mod", "internal/supervisor/release/release.go", "cmd/edge/main.go", "internal/supplychain/verify.go",
		"AGENTS.md", "docs/adr/0001-x.md", ".github/workflows/ci.yml", "CODEOWNERS"} {
		if !foundationOrProtected(p) {
			t.Errorf("%s is not counted as a path only the Owner changes", p)
		}
	}
	if foundationOrProtected("examples/hello-app/main.go") {
		t.Error("the application's code is counted as a path only the Owner changes")
	}
}
