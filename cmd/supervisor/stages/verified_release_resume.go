package stages

import (
	"context"
	"fmt"

	"github.com/bestagentkits/sisf/internal/supervisor/release"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// VerifiedFinisher finishes the release of an issue left Verified and
// records its LKG before the LKG is adopted; *release.Pipeline is one.
type VerifiedFinisher interface {
	FinishVerified(ctx context.Context, req release.Request) (release.Outcome, error)
	RecordVerifiedLKG(ctx context.Context, req release.Request) error
}

// FinishWaits records what finishing a verified release waits on where the
// issue's progress and runs.inspect show it; *CandidatePRs is one. It
// reports whether the record changed.
type FinishWaits interface {
	WaitToFinish(ctx context.Context, run core.RunID, why string) (bool, error)
}

// verifiedJob is an issue left Verified and the release request of its run.
type verifiedJob struct {
	record core.StateRecord
	req    release.Request
}

// verifiedJobs rebuilds the release request of every issue in Verified that
// this pass did not release, from the supervisor's own records.
func (s *ReleaseStage) verifiedJobs(ctx context.Context, skip map[core.IssueRef]release.Outcome, res *ReleasePassResult) []verifiedJob {
	var jobs []verifiedJob
	err := s.State.View(ctx, func(ctx context.Context, tx state.Tx) error {
		records, err := tx.ListRecords(ctx, state.RecordFilter{States: []core.State{core.StateVerified}})
		if err != nil {
			return err
		}
		for _, r := range records {
			if _, now := skip[r.IssueID]; now {
				continue // released in this pass: it is finished in the next
			}
			q, doc, build, err := readRequestRecords(ctx, tx, r)
			if err != nil {
				res.Errors = append(res.Errors, fmt.Errorf("%s: the records of its verified release: %w", r.IssueID, err))
				continue
			}
			req, err := s.request(ctx, r, q, doc, build)
			if err != nil {
				res.Errors = append(res.Errors, fmt.Errorf("%s: %w", r.IssueID, err))
				continue
			}
			jobs = append(jobs, verifiedJob{record: r, req: req})
		}
		return nil
	})
	if err != nil {
		res.Errors = append(res.Errors, err)
	}
	return jobs
}

// resumeVerified finishes every issue left Verified: the supervisor
// restarted, an emergency stop, an outage or a failed write of the LKG held
// it after its release was verified. A record an earlier build left without
// anything recorded as due is finished as well. Nothing done already is
// done again (see release.Pipeline.FinishVerified). What a release still
// waits on is recorded and reported once per change, not on every pass.
func (s *ReleaseStage) resumeVerified(ctx context.Context, res *ReleasePassResult) {
	finisher, ok := s.Pipeline.(VerifiedFinisher)
	if !ok {
		return
	}
	for _, j := range s.verifiedJobs(ctx, res.Outcomes, res) {
		out, err := finisher.FinishVerified(ctx, j.req)
		switch {
		case out.Result == release.ResultClosed:
			s.forgetWait(j.record.IssueID)
			rec := ReleaseRecord{Result: string(out.Result), State: out.State, Release: out.Release, At: s.Clock.Now().UTC()}
			if recErr := s.State.Update(ctx, func(ctx context.Context, tx state.Tx) error {
				return put(ctx, tx, ReleaseKey(j.record.RunID, j.record.Attempt), rec)
			}); recErr != nil {
				res.Errors = append(res.Errors, fmt.Errorf("%s: record its finished release: %w", j.record.IssueID, recErr))
			}
			res.Outcomes[j.record.IssueID] = out
		case out.Waits != "":
			if s.reportWait(ctx, j.record, out.Waits, res) {
				res.Outcomes[j.record.IssueID] = out
			}
		case core.HasCode(err, core.CodeLeaseHeld) && out.Result == "":
			// Driven by a release or a recovery of this pipeline right now.
		case err != nil:
			res.Errors = append(res.Errors, fmt.Errorf("%s: finish the verified release: %w", j.record.IssueID, err))
		}
	}
}

// reportWait records what the verified release of record waits on and
// reports whether that changed since it was last reported.
func (s *ReleaseStage) reportWait(ctx context.Context, record core.StateRecord, why string, res *ReleasePassResult) bool {
	if s.Waits != nil {
		if _, err := s.Waits.WaitToFinish(ctx, record.RunID, why); err != nil {
			res.Errors = append(res.Errors, fmt.Errorf("%s: record what its verified release waits on: %w", record.IssueID, err))
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reported == nil {
		s.reported = map[core.IssueRef]string{}
	}
	if s.reported[record.IssueID] == why {
		return false
	}
	s.reported[record.IssueID] = why
	return true
}

// forgetWait drops what was reported for issue, once it no longer waits.
func (s *ReleaseStage) forgetWait(issue core.IssueRef) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.reported, issue)
}

// RecordVerifiedLKGs makes the release of every issue left Verified the
// LKG of its unit when it is not yet and no later release replaced it. It
// runs at startup, before the LKG is adopted, so that a supervisor that
// stopped between a verification and the write of its LKG starts the
// verified release, as the record says, and not the one before it.
func (s *ReleaseStage) RecordVerifiedLKGs(ctx context.Context) ReleasePassResult {
	res := ReleasePassResult{Outcomes: map[core.IssueRef]release.Outcome{}}
	finisher, ok := s.Pipeline.(VerifiedFinisher)
	if !ok {
		return res
	}
	for _, j := range s.verifiedJobs(ctx, nil, &res) {
		if err := finisher.RecordVerifiedLKG(ctx, j.req); err != nil {
			res.Errors = append(res.Errors, fmt.Errorf("%s: record its verified release as the last known good release: %w", j.record.IssueID, err))
		}
	}
	return res
}
