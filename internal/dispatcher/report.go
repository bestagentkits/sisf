package dispatcher

import (
	"context"
	"slices"
	"strings"

	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// Bounds on what one report may add, so a report cannot bloat the record.
const (
	maxReportedItems  = 64
	maxPullRequestRef = 512
)

// Report is what the holder of a run's issue lease says about the run: the
// artifacts and pull requests it produced. It carries the lease it is made
// under; only the latest holder of a lease that is still live is heard.
type Report struct {
	Issue        core.IssueRef
	RunID        core.RunID
	LeaseOwner   string
	FencingToken core.FencingToken
	Artifacts    []agent.Artifact
	// PullRequests are references to pull requests the run opened, such as
	// "owner/repo#12" or their URL.
	PullRequests []string
}

// validate refuses a report that is malformed before any lease is checked.
func (r Report) validate() error {
	if err := r.Issue.Validate(); err != nil {
		return err
	}
	if err := r.RunID.Validate(); err != nil {
		return err
	}
	switch {
	case r.LeaseOwner == "" || r.FencingToken <= 0:
		return core.NewError(core.CodeStaleFencingToken, "a report must carry the issue lease and its fencing token")
	case len(r.Artifacts)+len(r.PullRequests) == 0:
		return invalid("a report must carry an artifact or a pull request")
	case len(r.Artifacts) > maxReportedItems || len(r.PullRequests) > maxReportedItems:
		return invalid("a report carries too many items")
	}
	for _, a := range r.Artifacts {
		if strings.TrimSpace(a.Name) == "" || !a.Kind.Valid() || a.Size < 0 {
			return invalid("a reported artifact needs a name, a known kind and a non-negative size")
		}
		if err := a.Digest.Validate(); err != nil {
			return err
		}
	}
	for _, pr := range r.PullRequests {
		if trimmed := strings.TrimSpace(pr); trimmed == "" || trimmed != pr || len(pr) > maxPullRequestRef {
			return invalid("a reported pull request needs a short reference without surrounding space")
		}
	}
	return nil
}

// Report records what a run produced. It is refused with
// STALE_FENCING_TOKEN when the token is not the latest issued for the issue
// or the lease was given up, with EXPIRED when the lease lapsed, and with
// CONFLICT when the issue is no longer in that run or the run has stopped
// working. Nothing is written by a refused report. An accepted report also
// renews the lease it was made under.
func (d *Dispatcher) Report(ctx context.Context, r Report) error {
	if err := r.validate(); err != nil {
		return err
	}
	lease := state.Lease{Resource: state.IssueLeaseResource(r.Issue), Owner: r.LeaseOwner, Token: r.FencingToken}
	_, _, err := d.fenced(ctx, r.Issue, r.RunID, lease, 0, func(_ context.Context, _ state.Tx, _ core.StateRecord, run *RunRecord) error {
		if run.Status != RunWorking {
			return core.NewError(core.CodeConflict, "the run is "+string(run.Status)+" and takes no more reports")
		}
		run.Artifacts = mergeArtifacts(run.Artifacts, r.Artifacts)
		for _, pr := range r.PullRequests {
			if !slices.Contains(run.PullRequests, pr) {
				run.PullRequests = append(run.PullRequests, pr)
			}
		}
		return nil
	})
	return err
}

// fenced applies one write about a run in a single transaction, and only
// when it comes from the latest holder of a live issue lease. It proves the
// token is the latest one issued, renews the lease (which fails for a lease
// that lapsed or was given up), checks that the issue is still in the run,
// and reads the run record. When expect is not zero the record must still be
// at that version, so a decision taken on an older read is not applied. fn
// may change the record and write lifecycle steps through tx; the record is
// written back after it. It returns the renewed lease and the version of
// the run record written.
func (d *Dispatcher) fenced(ctx context.Context, issue core.IssueRef, runID core.RunID, lease state.Lease, expect int64,
	fn func(ctx context.Context, tx state.Tx, record core.StateRecord, run *RunRecord) error,
) (state.Lease, int64, error) {
	var (
		renewed state.Lease
		written int64
	)
	err := d.cfg.State.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		if err := tx.CheckFencing(ctx, lease.Resource, lease.Token); err != nil {
			return err
		}
		var err error
		if renewed, err = tx.RenewLease(ctx, lease, d.cfg.LeaseTTL); err != nil {
			return err
		}
		record, err := tx.GetRecord(ctx, issue)
		if err != nil {
			return err
		}
		if record.RunID != runID {
			return core.NewError(core.CodeConflict, "the issue is no longer in run "+string(runID))
		}
		run, version, err := readRunRecord(ctx, tx, runID)
		if err != nil {
			return err
		}
		if expect != 0 && version != expect {
			return core.NewError(core.CodeConflict, "the run record changed since it was read")
		}
		if err := fn(ctx, tx, record, &run); err != nil {
			return err
		}
		run.UpdatedAt = d.cfg.Clock.Now()
		written, err = writeRunRecord(ctx, tx, run, version)
		return err
	})
	return renewed, written, err
}

// mergeArtifacts adds the artifacts not yet recorded, by digest and name.
func mergeArtifacts(have, add []agent.Artifact) []agent.Artifact {
	for _, a := range add {
		if !slices.ContainsFunc(have, func(h agent.Artifact) bool { return h.Digest == a.Digest && h.Name == a.Name }) {
			have = append(have, a)
		}
	}
	return have
}
