package stages

import (
	"context"
	"fmt"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/internal/supervisor/release"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// VerifiedFinisher lands and closes an issue whose release was verified
// but whose landing could not be decided; *release.Pipeline is one.
type VerifiedFinisher interface {
	FinishVerified(ctx context.Context, req release.LandRequest) (release.Outcome, error)
}

// resumeVerified finishes the issues left Verified with their landing
// pending: the supervisor restarted, or GitHub or an emergency stop held
// the merge, after the release was verified. Nothing before the landing is
// done again, and the pull request is merged at most once.
func (s *ReleaseStage) resumeVerified(ctx context.Context, res *ReleasePassResult) {
	finisher, ok := s.Pipeline.(VerifiedFinisher)
	if !ok {
		return
	}
	type job struct {
		record core.StateRecord
		req    release.LandRequest
	}
	var jobs []job
	err := s.State.View(ctx, func(ctx context.Context, tx state.Tx) error {
		records, err := tx.ListRecords(ctx, state.RecordFilter{States: []core.State{core.StateVerified}})
		if err != nil {
			return err
		}
		for _, r := range records {
			if _, now := res.Outcomes[r.IssueID]; now {
				continue // released in this pass: its landing is tried again in the next
			}
			pr, err := handoff.ReadCandidatePR(ctx, tx, r.RunID)
			if err != nil || pr.State != handoff.LandingPending {
				continue
			}
			qa, err := ReadQAResult(ctx, tx, r.RunID, r.Attempt)
			if err != nil {
				res.Errors = append(res.Errors, fmt.Errorf("%s: %w", r.IssueID, err))
				continue
			}
			jobs = append(jobs, job{record: r, req: release.LandRequest{
				IssueID: r.IssueID, RunID: r.RunID, Release: r.ReleaseDigest, Candidate: qa.Candidate, Receipt: qa.Receipt,
			}})
		}
		return nil
	})
	if err != nil {
		res.Errors = append(res.Errors, err)
		return
	}
	for _, j := range jobs {
		out, err := finisher.FinishVerified(ctx, j.req)
		if out.Result == release.ResultClosed {
			rec := ReleaseRecord{Result: string(out.Result), State: out.State, Release: out.Release, At: s.Clock.Now().UTC()}
			err = s.State.Update(ctx, func(ctx context.Context, tx state.Tx) error {
				return put(ctx, tx, ReleaseKey(j.record.RunID, j.record.Attempt), rec)
			})
			res.Outcomes[j.record.IssueID] = out
		}
		if err != nil {
			res.Errors = append(res.Errors, fmt.Errorf("%s: finish the verified release: %w", j.record.IssueID, err))
		}
	}
}
