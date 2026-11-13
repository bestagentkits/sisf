package statemachine

import (
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
)

// storedIn is the record of the test issue in the given state, held by the
// test run with a submitted candidate and a started release.
func storedIn(state core.State) core.StateRecord {
	return core.StateRecord{
		FactoryID:       testFactory,
		IssueID:         testIssue,
		State:           state,
		Version:         1,
		IdempotencyKey:  "earlier-step",
		LeaseOwner:      "dispatcher-1",
		FencingToken:    5,
		Attempt:         1,
		RunID:           testRun,
		CandidateDigest: testDigest,
		ReleaseDigest:   released,
		UpdatedAt:       testNow,
	}
}

func TestAStepThatContinuesTheStoredRunCandidateAndReleaseIsBound(t *testing.T) {
	t.Parallel()
	for _, e := range Edges() {
		if e.From == "" || e.Event == core.EventClaimed {
			continue
		}
		if err := CheckRecordBinding(storedIn(e.From), transitionOn(e.From, e.Event, e.To)); err != nil {
			t.Errorf("%s --%s--> %s for the stored run, candidate and release was refused: %v", e.From, e.Event, e.To, err)
		}
	}
}

func TestAStepOfAnotherRunIsRefusedExceptTheClaim(t *testing.T) {
	t.Parallel()
	for _, e := range Edges() {
		if e.From == "" || e.Event == core.EventClaimed {
			continue
		}
		foreign := transitionOn(e.From, e.Event, e.To)
		foreign.RunID = otherRun
		if err := CheckRecordBinding(storedIn(e.From), foreign); !core.HasCode(err, core.CodeConflict) {
			t.Errorf("%s --%s--> %s by another run: want %s, got %v", e.From, e.Event, e.To, core.CodeConflict, err)
		}
		unnamed := transitionOn(e.From, e.Event, e.To)
		unnamed.RunID = ""
		err := CheckRecordBinding(storedIn(e.From), unnamed)
		if held := RequiresLease(e.From); held != core.HasCode(err, core.CodeConflict) {
			t.Errorf("%s --%s--> %s naming no run: refused=%v, want %v", e.From, e.Event, e.To, err != nil, held)
		}
	}
	// A step before any claim cannot bring a run of its own.
	early := transitionOn(core.StateIntake, core.EventInfoSufficient, core.StateScopeDraft)
	unclaimed := storedIn(core.StateIntake)
	unclaimed.RunID, unclaimed.CandidateDigest, unclaimed.ReleaseDigest = "", "", ""
	early.CandidateDigest, early.ReleaseDigest = "", ""
	if err := CheckRecordBinding(unclaimed, early); !core.HasCode(err, core.CodeConflict) {
		t.Fatalf("a step before the claim that names a run: want %s, got %v", core.CodeConflict, err)
	}
}

func TestAClaimStartsARunTheIssueHasNotHad(t *testing.T) {
	t.Parallel()
	ready := storedIn(core.StateReady)
	fresh := claimRequest()
	fresh.RunID = otherRun
	if err := CheckRecordBinding(ready, fresh); err != nil {
		t.Fatalf("a claim that starts a new run was refused: %v", err)
	}
	first := ready
	first.RunID = ""
	if err := CheckRecordBinding(first, claimRequest()); err != nil {
		t.Fatalf("the first claim of an issue was refused: %v", err)
	}
	// Reusing the run of the last claim would let its receipts count again.
	if err := CheckRecordBinding(ready, claimRequest()); !core.HasCode(err, core.CodeConflict) {
		t.Fatalf("a claim that reuses the stored run: want %s, got %v", core.CodeConflict, err)
	}
	unnamed := claimRequest()
	unnamed.RunID = ""
	if err := CheckRecordBinding(first, unnamed); !core.HasCode(err, core.CodeConflict) {
		t.Fatalf("a claim that names no run: want %s, got %v", core.CodeConflict, err)
	}
	// A new run has built nothing and released nothing, so a claim that
	// names a candidate or a release names what another run made.
	for name, change := range map[string]func(*core.Transition){
		"a claim that names a candidate": func(c *core.Transition) { c.CandidateDigest = testDigest },
		"a claim that names a release":   func(c *core.Transition) { c.ReleaseDigest = released },
	} {
		named := fresh
		change(&named)
		if err := CheckRecordBinding(ready, named); !core.HasCode(err, core.CodeConflict) {
			t.Errorf("%s: want %s, got %v", name, core.CodeConflict, err)
		}
	}
}

func TestAStepAboutAnotherCandidateOrReleaseThanTheStoredOneIsRefused(t *testing.T) {
	t.Parallel()
	otherCandidate := func(r *core.Transition) { r.CandidateDigest = core.Digest(otherBuild) }
	noCandidate := func(r *core.Transition) { r.CandidateDigest = "" }
	otherRelease := func(r *core.Transition) { r.ReleaseDigest = otherBuild }
	noRelease := func(r *core.Transition) { r.ReleaseDigest = "" }
	cases := map[string]struct {
		request core.Transition
		mutate  func(*core.Transition)
	}{
		"QA passes another candidate than the one submitted":    {qaPassRequest(), otherCandidate},
		"QA passes without naming the candidate":                {qaPassRequest(), noCandidate},
		"the rollout starts for another candidate":              {rolloutRequest(), otherCandidate},
		"the rollout starts without naming the candidate":       {rolloutRequest(), noCandidate},
		"the rollout starts without naming the release":         {rolloutRequest(), noRelease},
		"a candidate is submitted without its digest":           {candidateRequest(), noCandidate},
		"the rollout is healthy for another release":            {canaryRequest(), otherRelease},
		"the rollout is healthy without naming the release":     {canaryRequest(), noRelease},
		"another release than the one rolled out is verified":   {postDeployRequest(), otherRelease},
		"a release is verified without being named":             {postDeployRequest(), noRelease},
		"a failed evaluation is reported for another candidate": {qaRetryRequest(), otherCandidate},
		"the issue is closed for another release":               {transitionOn(core.StateVerified, core.EventIssueClosed, core.StateClosed), otherRelease},
	}
	for name, tc := range cases {
		request := tc.request
		tc.mutate(&request)
		if err := CheckRecordBinding(storedIn(request.From), request); !core.HasCode(err, core.CodeConflict) {
			t.Errorf("%s: want %s, got %v", name, core.CodeConflict, err)
		}
	}
}

func TestSubmittingACandidateAndStartingARolloutReplaceWhatIsStored(t *testing.T) {
	t.Parallel()
	// A new attempt submits a new build, and the rollout names the release
	// for the first time; neither is held to the value stored before.
	resubmitted := candidateRequest()
	resubmitted.CandidateDigest = core.Digest(otherBuild)
	if err := CheckRecordBinding(storedIn(core.StateDeveloping), resubmitted); err != nil {
		t.Fatalf("submitting the build of a new attempt was refused: %v", err)
	}
	before := storedIn(core.StateReleaseCandidate)
	before.ReleaseDigest = ""
	if err := CheckRecordBinding(before, rolloutRequest()); err != nil {
		t.Fatalf("starting the first rollout of a run was refused: %v", err)
	}
	// A step that names neither digest leaves both as they are stored.
	quiet := transitionOn(core.StateClaimed, core.EventDevelopmentStarted, core.StateDeveloping)
	quiet.CandidateDigest, quiet.ReleaseDigest = "", ""
	if err := CheckRecordBinding(storedIn(core.StateClaimed), quiet); err != nil {
		t.Fatalf("a step that names no candidate or release was refused: %v", err)
	}
}
