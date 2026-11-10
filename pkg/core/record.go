package core

import "time"

// FencingToken orders the holders of a lease. Each acquisition of a lease
// yields a strictly larger token, and a write carrying a token older than the
// latest one issued is rejected, so a holder that lost its lease cannot
// overwrite the work of its successor.
type FencingToken int64

// StateRecord is the operational row of one issue. It is working state
// derived from GitHub, never a replacement for it.
//
// Version makes every write a compare-and-swap, IdempotencyKey makes a
// replayed write a no-op, and LeaseOwner with FencingToken name the single
// writer currently allowed to report on the issue. RunID, CandidateDigest and
// ReleaseDigest name what the current run is working on, so a later step
// cannot swap in another run, candidate or release.
type StateRecord struct {
	FactoryID      FactoryID    `json:"factory_id"`
	IssueID        IssueRef     `json:"issue_id"`
	State          State        `json:"state"`
	Version        int64        `json:"version"`
	IdempotencyKey string       `json:"idempotency_key"`
	LeaseOwner     string       `json:"lease_owner,omitempty"`
	FencingToken   FencingToken `json:"fencing_token,omitempty"`
	Attempt        int          `json:"attempt"`
	// RunID is the run the latest claim started.
	RunID RunID `json:"run_id,omitempty"`
	// CandidateDigest is the candidate the run last submitted for evaluation.
	CandidateDigest Digest `json:"candidate_digest,omitempty"`
	// ReleaseDigest is the release the run started rolling out.
	ReleaseDigest ReleaseDigest `json:"release_digest,omitempty"`
	UpdatedAt     time.Time     `json:"updated_at"`
}

// Validate checks the structural rules of a stored record. It does not judge
// whether the state was reachable; the transition table does that.
func (r StateRecord) Validate() error {
	if err := r.FactoryID.Validate(); err != nil {
		return err
	}
	if err := r.IssueID.Validate(); err != nil {
		return err
	}
	switch {
	case !r.State.Valid():
		return invalid("unknown issue state %q", string(r.State))
	case r.Version < 1:
		return invalid("record version %d must be at least 1", r.Version)
	case r.IdempotencyKey == "":
		return invalid("record needs the idempotency key of its last write")
	case r.Attempt < 0:
		return invalid("attempt %d must not be negative", r.Attempt)
	case r.UpdatedAt.IsZero():
		return invalid("record needs the instant of its last write")
	}
	if r.RunID != "" {
		if err := r.RunID.Validate(); err != nil {
			return err
		}
	}
	if r.CandidateDigest != "" {
		if err := r.CandidateDigest.Validate(); err != nil {
			return err
		}
	}
	if r.ReleaseDigest != "" {
		if err := r.ReleaseDigest.Validate(); err != nil {
			return err
		}
	}
	return validateLease(r.LeaseOwner, r.FencingToken)
}

// Transition is one requested change of an issue's state. It carries
// everything a store needs to apply the change atomically and everything an
// auditor needs to explain it afterwards.
//
// ExpectedVersion is the version the writer read; a mismatch means another
// writer got there first. A non-zero FencingToken says the writer acts under a
// lease and must be the latest holder. A zero token is an unleased write that
// relies on the version check alone; it is accepted only while no run holds
// the issue. From the claim until the release leaves Monitoring every step
// must carry the current token, so a worker that lost its lease cannot write.
type Transition struct {
	IssueID         IssueRef     `json:"issue_id"`
	From            State        `json:"from,omitempty"`
	To              State        `json:"to"`
	Event           Event        `json:"event"`
	ExpectedVersion int64        `json:"expected_version"`
	IdempotencyKey  string       `json:"idempotency_key"`
	LeaseOwner      string       `json:"lease_owner,omitempty"`
	FencingToken    FencingToken `json:"fencing_token,omitempty"`
	// RunID names the run the step belongs to. The claim starts a run and
	// makes it the record's run; every later step of that run must name it,
	// and no other step may name another run.
	RunID RunID `json:"run_id,omitempty"`
	// Attempt is the attempt number the record carries after the write. The
	// first attempt of a run is 1, the retry after a failed evaluation
	// carries the next number, and no other step lowers the stored number.
	Attempt int `json:"attempt"`
	// ActorID names who asked for the change, for the audit trail.
	ActorID string `json:"actor_id,omitempty"`
	Reason  string `json:"reason,omitempty"`
	// EvidenceRefs point to the receipts, reports or comments that justify the change.
	EvidenceRefs []string `json:"evidence_refs,omitempty"`
	// CandidateDigest names the built candidate the step is about. Submitting
	// a candidate records it; the steps into ReleaseCandidate and Canary must
	// carry the recorded one, and the QA receipt must be for it.
	CandidateDigest Digest `json:"candidate_digest,omitempty"`
	// ReleaseDigest names the release the step is about. Starting the rollout
	// records it; the steps into Monitoring and Verified must carry the
	// recorded one, and the observed revision must be that release.
	ReleaseDigest ReleaseDigest `json:"release_digest,omitempty"`
}

// Creates reports whether the transition creates the record instead of
// changing an existing one.
func (t Transition) Creates() bool { return t.From == "" }

// Validate checks the structural rules of a request. Whether the edge exists
// and its guards hold is decided by the transition table.
func (t Transition) Validate() error {
	if err := t.IssueID.Validate(); err != nil {
		return err
	}
	switch {
	case !t.To.Valid():
		return invalid("unknown target state %q", string(t.To))
	case !t.Event.Valid():
		return invalid("unknown lifecycle event %q", string(t.Event))
	case t.ExpectedVersion < 0:
		return invalid("expected version %d must not be negative", t.ExpectedVersion)
	case t.IdempotencyKey == "":
		return invalid("transition needs an idempotency key")
	case t.Attempt < 0:
		return invalid("attempt %d must not be negative", t.Attempt)
	}
	if t.Creates() {
		if t.Event != EventIssueReceived || t.To != StateIntake || t.ExpectedVersion != 0 {
			return invalid("a record is created only in %s by %s at version 0", StateIntake, EventIssueReceived)
		}
	} else if !t.From.Valid() {
		return invalid("unknown source state %q", string(t.From))
	}
	if t.RunID != "" {
		if err := t.RunID.Validate(); err != nil {
			return err
		}
	}
	if t.CandidateDigest != "" {
		if err := t.CandidateDigest.Validate(); err != nil {
			return err
		}
	}
	if t.ReleaseDigest != "" {
		if err := t.ReleaseDigest.Validate(); err != nil {
			return err
		}
	}
	return validateLease(t.LeaseOwner, t.FencingToken)
}

func validateLease(owner string, token FencingToken) error {
	switch {
	case token < 0:
		return invalid("fencing token %d must not be negative", token)
	case token > 0 && owner == "":
		return invalid("a fencing token needs the lease owner it was issued to")
	}
	return nil
}
