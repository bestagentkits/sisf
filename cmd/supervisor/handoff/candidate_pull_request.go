package handoff

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// LandingState is where a candidate's pull request stands.
type LandingState string

const (
	// LandingOpen: the pull request is open; the release of its candidate
	// is not verified yet.
	LandingOpen LandingState = "open"
	// LandingPending: the release was verified and the supervisor is
	// merging; a restart finishes the merge before the issue closes.
	LandingPending LandingState = "pending"
	// LandingMerged: the supervisor merged the candidate after its release
	// was verified.
	LandingMerged LandingState = "merged"
	// LandingLeftOpen: the release was verified but the supervisor did not
	// merge; the Owner decides, and Reason says why.
	LandingLeftOpen LandingState = "left_for_owner"
	// LandingRolledBack: the release was rolled back; the pull request is
	// never merged by the factory.
	LandingRolledBack LandingState = "rolled_back"
	// LandingRefused: the release was refused before anything was
	// deployed; the pull request is never merged by the factory and stays
	// open for the Owner.
	LandingRefused LandingState = "refused"
)

// Final reports whether nothing more happens to the pull request by itself.
func (s LandingState) Final() bool {
	return s == LandingMerged || s == LandingLeftOpen || s == LandingRolledBack || s == LandingRefused
}

// CandidatePR is the supervisor's record of the pull request of a run's
// passed candidate and of what became of it. It holds numbers, commits and
// fixed reasons only, so the control zone can show it on the issue as it is.
type CandidatePR struct {
	IssueID core.IssueRef `json:"issue_id"`
	RunID   core.RunID    `json:"run_id"`
	// Attempt is the evaluator attempt whose candidate passed.
	Attempt int    `json:"attempt"`
	Branch  string `json:"branch"`
	Base    string `json:"base"`
	// Number is 0 when the factory opened no pull request: another was on
	// the branch, or GitHub refused to open one.
	Number int    `json:"number"`
	URL    string `json:"url"`
	// Commit is the candidate commit QA judged and the release was built
	// from; the pull request is merged only at exactly this head.
	Commit      core.CommitSHA `json:"commit"`
	State       LandingState   `json:"state"`
	Reason      string         `json:"reason,omitempty"`
	MergeCommit core.CommitSHA `json:"merge_commit,omitempty"`
	// Retries counts the merges GitHub asked to try again, as when the
	// default branch moved just before; they are bounded.
	Retries int `json:"retries,omitempty"`
	// Waiting says what a landing that cannot be decided now waits on, as a
	// token GitHub refuses, since WaitingSince; empty while nothing holds it.
	// The supervisor writes it; it is cleared once the landing is decided.
	Waiting      string    `json:"waiting,omitempty"`
	WaitingSince time.Time `json:"waiting_since,omitzero"`
	OpenedAt     time.Time `json:"opened_at"`
	DecidedAt    time.Time `json:"decided_at,omitzero"`
}

// CandidatePRKey is the key of the pull request of a run's candidate.
func CandidatePRKey(run core.RunID) string { return "candidate-pr/" + string(run) }

// PutCandidatePR records the pull request of a run's candidate.
func PutCandidatePR(ctx context.Context, kv state.KV, pr CandidatePR) error {
	return putReplacing(ctx, kv, CandidatePRKey(pr.RunID), pr)
}

// ReadCandidatePR reads the pull request of a run's candidate, NOT_FOUND
// before one was opened.
func ReadCandidatePR(ctx context.Context, kv state.KV, run core.RunID) (CandidatePR, error) {
	var pr CandidatePR
	if err := readDoc(ctx, kv, BuildNamespace, CandidatePRKey(run), &pr); err != nil {
		return CandidatePR{}, err
	}
	// Number is 0 only for a candidate left for the Owner without a pull
	// request of the factory's.
	if pr.RunID != run || pr.Number < 0 || pr.Number == 0 && pr.State != LandingLeftOpen {
		return CandidatePR{}, core.NewError(core.CodeTampered, "the stored pull request of run "+string(run)+" cannot be read")
	}
	return pr, nil
}

// QANote is one QA verdict as the issue's progress shows it: the verdict,
// the score against the pass mark and the hard gates that failed, never
// the text of a refusal.
type QANote struct {
	IssueID     core.IssueRef `json:"issue_id"`
	RunID       core.RunID    `json:"run_id"`
	Attempt     int           `json:"attempt"`
	MaxAttempts int           `json:"max_attempts"`
	Verdict     string        `json:"verdict"`
	Built       bool          `json:"built"`
	Receipt     bool          `json:"receipt"`
	FailedGates []string      `json:"failed_gates,omitempty"`
	Score       int           `json:"score"`
	PassScore   int           `json:"pass_score"`
	At          time.Time     `json:"at"`
}

// QANoteKey is the key of the note of one evaluator attempt; QANotePrefix
// lists a run's.
func QANoteKey(run core.RunID, attempt int) string {
	return fmt.Sprintf("%s%d", QANotePrefix(run), attempt)
}

// QANotePrefix is the key prefix of a run's QA notes.
func QANotePrefix(run core.RunID) string { return "qa-note/" + string(run) + "/" }

// PutQANote records the note of a verdict, in the verdict's transaction.
func PutQANote(ctx context.Context, kv state.KV, n QANote) error {
	return putReplacing(ctx, kv, QANoteKey(n.RunID, n.Attempt), n)
}

// ReadQANotes reads a run's QA notes in attempt order.
func ReadQANotes(ctx context.Context, kv state.KV, run core.RunID) ([]QANote, error) {
	entries, err := kv.ListKV(ctx, BuildNamespace, QANotePrefix(run), 100)
	if err != nil {
		return nil, err
	}
	notes := make([]QANote, 0, len(entries))
	for _, e := range entries {
		var n QANote
		if json.Unmarshal(e.Value, &n) != nil || n.RunID != run || n.Attempt < 1 {
			return nil, core.NewError(core.CodeTampered, "a stored QA note of run "+string(run)+" cannot be read")
		}
		notes = append(notes, n)
	}
	// Keys sort as text, attempt 10 before 2.
	slices.SortFunc(notes, func(a, b QANote) int { return a.Attempt - b.Attempt })
	return notes, nil
}

// putReplacing writes a document of the build namespace over what is there.
func putReplacing(ctx context.Context, kv state.KV, key string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return core.NewError(core.CodeInternal, "a supervisor record cannot be encoded").WithCause(err)
	}
	version := int64(0)
	if entry, err := kv.GetKV(ctx, BuildNamespace, key); err == nil {
		version = entry.Version
	} else if !core.HasCode(err, core.CodeNotFound) {
		return err
	}
	_, err = kv.PutKV(ctx, state.KVEntry{Namespace: BuildNamespace, Key: key, Value: raw, Version: version})
	return err
}
