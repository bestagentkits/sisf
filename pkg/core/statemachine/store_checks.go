package statemachine

import (
	"fmt"
	"slices"
	"strconv"

	"github.com/bestagentkits/sisf/pkg/core"
)

// The checks below are pure, like CheckEdge: they read no clock and consult
// no guard. The operational store calls each of them in every write of a
// transition, so the rules hold even for a caller that never asked the
// machine.

// leasedStates are the states in which a run holds the issue: from the claim
// until the release leaves Monitoring.
var leasedStates = []core.State{
	core.StateClaimed, core.StateDeveloping, core.StateQAEvaluation,
	core.StateReleaseCandidate, core.StateCanary, core.StateMonitoring,
}

// RequiresLease reports whether a record in state is held by a run, so that
// every write to it must carry the current fencing token of the lease on the
// issue. It is true from Claimed through Monitoring and false for every other
// state and for anything that is not a lifecycle state.
func RequiresLease(state core.State) bool { return slices.Contains(leasedStates, state) }

// CheckLease reports whether t carries the lease its step needs. A step needs
// one when it leaves a state that RequiresLease, or when it is the claim,
// which enters the first such state under the lease it has just taken. Such
// a step without a lease owner or with a zero fencing token yields
// STALE_FENCING_TOKEN: a writer that holds no lease is no better than one
// whose lease was taken over.
//
// The check sees that a token is carried, not that it is the latest one. The
// store calls CheckLease and then compares the token a step carries with the
// latest one issued for the issue, so a step that needs a lease is always
// written by its current holder.
func CheckLease(t core.Transition) error {
	needed := RequiresLease(t.From) || RequiresLease(t.To)
	if !needed || (t.LeaseOwner != "" && t.FencingToken > 0) {
		return nil
	}
	refusal := core.NewError(core.CodeStaleFencingToken,
		"a run holds the issue, so the step must be written under the current lease and its fencing token")
	refusal.Details = stepDetails(t)
	return refusal
}

// CheckRecordBinding reports whether t continues what the stored record is
// about: the run that holds the issue, the attempt it is on, the candidate it
// submitted and the release it rolled out. stored is the record as it is
// before the write. A step that does not continue it yields CONFLICT:
//
//   - the claim must name a run, and not the run the stored record names: a
//     claim starts a new run, so receipts of the run before never count again;
//   - any other step must not name another run than the stored one, and must
//     name the stored one while a run holds the issue (RequiresLease);
//   - the claim must carry attempt 1, since a new run starts its count; the
//     failed evaluation (qa_failed) must carry exactly the stored attempt
//     plus one; every other step must carry the stored attempt unchanged, so
//     no step can lower, reset or skip the count the attempt limit is judged
//     against;
//   - submitting a candidate must name it; the steps into ReleaseCandidate and
//     Canary must name the stored candidate, and no other step may name
//     another one;
//   - starting the rollout must name the release; the steps into Monitoring
//     and Verified must name the stored release, and no other step may name
//     another one.
func CheckRecordBinding(stored core.StateRecord, t core.Transition) error {
	if want := attemptAfter(stored, t.Event); t.Attempt != want {
		refusal := unbound(t, "the step must carry attempt %d, not %d", want, t.Attempt)
		refusal.Details["attempt"] = strconv.Itoa(t.Attempt)
		refusal.Details["stored_attempt"] = strconv.Itoa(stored.Attempt)
		return refusal
	}
	if t.Event == core.EventClaimed {
		if t.RunID == "" || t.RunID == stored.RunID {
			return unbound(t, "a claim starts a new run and must name another run than the one the record names")
		}
		return nil
	}
	if t.RunID != stored.RunID && (t.RunID != "" || RequiresLease(stored.State)) {
		return unbound(t, "the step does not belong to the run the issue is in")
	}
	candidate := binding{
		sets:     t.Event == core.EventCandidateSubmitted,
		required: t.Event == core.EventQAPassed || t.Event == core.EventRolloutStarted,
	}
	if !candidate.holds(string(t.CandidateDigest), string(stored.CandidateDigest)) {
		return unbound(t, "the step is not about the candidate that was submitted")
	}
	release := binding{
		sets:     t.Event == core.EventRolloutStarted,
		required: t.Event == core.EventRolloutHealthy || t.Event == core.EventPostDeployVerified,
	}
	if !release.holds(string(t.ReleaseDigest), string(stored.ReleaseDigest)) {
		return unbound(t, "the step is not about the release that was rolled out")
	}
	return nil
}

// attemptAfter is the attempt number the record must carry after a step on
// event: the claim starts the count of a new run at 1, a failed evaluation
// sends the run into its next attempt, and every other step keeps the stored
// number. The attempts guard judges the limit against the stored number, so
// this is what makes the limit bind.
func attemptAfter(stored core.StateRecord, event core.Event) int {
	switch event {
	case core.EventClaimed:
		return 1
	case core.EventQAFailed:
		return stored.Attempt + 1
	}
	return stored.Attempt
}

// binding is how one step relates to a value the record stores: the step
// sets it, must repeat it, or may repeat it.
type binding struct{ sets, required bool }

// holds reports whether the value a step names fits the stored one. A step
// that sets the value must name one; a step that must repeat it must name the
// stored one; any other step names nothing or the stored one.
func (b binding) holds(named, stored string) bool {
	switch {
	case b.sets:
		return named != ""
	case b.required:
		return named != "" && named == stored
	}
	return named == "" || named == stored
}

// unbound builds the CONFLICT of a step that does not continue the stored
// record.
func unbound(t core.Transition, format string, args ...any) *core.Error {
	refusal := core.NewError(core.CodeConflict, fmt.Sprintf(format, args...))
	refusal.Details = stepDetails(t)
	return refusal
}

// stepDetails names the step a store check refused.
func stepDetails(t core.Transition) map[string]string {
	return map[string]string{"from": string(t.From), "event": string(t.Event), "to": string(t.To)}
}
