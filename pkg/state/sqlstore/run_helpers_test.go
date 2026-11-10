package sqlstore_test

import (
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
)

const (
	candidateDigest core.Digest        = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	otherCandidate  core.Digest        = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	releaseDigest   core.ReleaseDigest = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	otherRelease    core.ReleaseDigest = "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"

	// leaseTTL is how long the lease of a test worker lasts.
	leaseTTL = time.Hour
)

// next builds the write that moves a record along the edge of event, the way
// the run that holds the issue writes it: under the lease the record was last
// written under, naming the run of the record and its attempt, and naming the
// candidate and the release on the steps that record or must repeat them. A
// failed evaluation sends the run into its next attempt. The key is the event
// name, so a test takes each edge once per issue unless it sets another.
func next(t *testing.T, from core.StateRecord, event core.Event, change func(*core.Transition)) core.Transition {
	t.Helper()
	to, err := statemachine.Next(from.State, event)
	if err != nil {
		t.Fatalf("no edge from %s on %s: %v", from.State, event, err)
	}
	tr := step(from.IssueID, from.State, event, to, from.Version, string(event))
	tr.Attempt, tr.RunID = from.Attempt, from.RunID
	if statemachine.RequiresLease(from.State) {
		tr.LeaseOwner, tr.FencingToken = from.LeaseOwner, from.FencingToken
	}
	switch event {
	case core.EventCandidateSubmitted:
		tr.CandidateDigest = candidateDigest
	case core.EventQAFailed:
		tr.Attempt = from.Attempt + 1
	case core.EventQAPassed:
		tr.CandidateDigest = from.CandidateDigest
	case core.EventRolloutStarted:
		tr.CandidateDigest, tr.ReleaseDigest = from.CandidateDigest, releaseDigest
	case core.EventRolloutHealthy, core.EventPostDeployVerified:
		tr.ReleaseDigest = from.ReleaseDigest
	}
	if change != nil {
		change(&tr)
	}
	return tr
}

// advance takes one edge and returns the record after it.
func advance(t *testing.T, s *sqlstore.Store, from core.StateRecord, event core.Event, change func(*core.Transition)) core.StateRecord {
	t.Helper()
	return mustTransition(t, s, next(t, from, event, change))
}

// along takes the edges of events in order and returns the record after the
// last one.
func along(t *testing.T, s *sqlstore.Store, from core.StateRecord, events ...core.Event) core.StateRecord {
	t.Helper()
	for _, event := range events {
		from = advance(t, s, from, event, nil)
	}
	return from
}

// leaseOn takes the lease on an issue for owner.
func leaseOn(t *testing.T, s *sqlstore.Store, ref core.IssueRef, owner string) state.Lease {
	t.Helper()
	lease, err := acquire(s, state.IssueLeaseResource(ref), owner, leaseTTL)
	if err != nil {
		t.Fatalf("lease on %s for %s: %v", ref, owner, err)
	}
	return lease
}

// under makes a write one that is written under lease.
func under(lease state.Lease) func(*core.Transition) {
	return func(tr *core.Transition) { tr.LeaseOwner, tr.FencingToken = lease.Owner, lease.Token }
}

// claimWrite builds the claim of a Ready issue by owner, who takes the lease
// first, for a run that makes its first attempt.
func claimWrite(t *testing.T, s *sqlstore.Store, ready core.StateRecord, owner string, run core.RunID) core.Transition {
	t.Helper()
	lease := leaseOn(t, s, ready.IssueID, owner)
	return next(t, ready, core.EventClaimed, func(tr *core.Transition) {
		under(lease)(tr)
		tr.RunID, tr.Attempt = run, 1
		tr.IdempotencyKey = "claimed:" + string(run)
	})
}

// claim has owner take the lease on a Ready issue and claim it for run.
func claim(t *testing.T, s *sqlstore.Store, ready core.StateRecord, owner string, run core.RunID) core.StateRecord {
	t.Helper()
	return mustTransition(t, s, claimWrite(t, s, ready, owner, run))
}

// developing drives an issue to Developing: worker-1 holds its lease and
// run-1 works on it.
func developing(t *testing.T, s *sqlstore.Store, ref core.IssueRef) core.StateRecord {
	t.Helper()
	return advance(t, s, claim(t, s, readyRecord(t, s, ref), "worker-1", "run-1"), core.EventDevelopmentStarted, nil)
}

// refused fails the test unless the write is refused with code and leaves
// the stored record as it was.
func refused(t *testing.T, s *sqlstore.Store, name string, write core.Transition, code core.ErrorCode) {
	t.Helper()
	before, existed := lookup(t, s, write.IssueID)
	if _, err := transition(s, write); !core.HasCode(err, code) {
		t.Errorf("%s: want %s, got %v", name, code, err)
	}
	if after, exists := lookup(t, s, write.IssueID); after != before || exists != existed {
		t.Errorf("%s: the refused write changed the record: %+v", name, after)
	}
}

// lookup reads the stored record of an issue and reports whether it exists.
func lookup(t *testing.T, s *sqlstore.Store, ref core.IssueRef) (core.StateRecord, bool) {
	t.Helper()
	var got core.StateRecord
	err := view(s, func(tx state.Tx) (err error) {
		got, err = tx.GetRecord(ctx, ref)
		return err
	})
	if err != nil && !core.HasCode(err, core.CodeNotFound) {
		t.Fatalf("read record %s: %v", ref, err)
	}
	return got, err == nil
}
