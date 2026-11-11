package intake

import (
	"context"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// ExpireClarifications parks every issue whose wait for an answer was over
// by answersThrough and returns them. It is what a timer calls, with the
// time up to which every answer given on GitHub was taken in and handled: an issue
// whose deadline passed after that may have an answer still on its way,
// and waits. It reads only the operational store: an issue that nobody
// answers costs no GitHub call and no model call, however long it stays
// open.
func (s *Service) ExpireClarifications(ctx context.Context, answersThrough time.Time) ([]core.IssueRef, error) {
	cutoff := s.cfg.Clock.Now()
	if answersThrough.Before(cutoff) {
		cutoff = answersThrough
	}
	// Issues loaded before a failed load are still parked.
	overdue, loadErr := s.overdueClarifications(ctx, cutoff)
	var parked []core.IssueRef
	for _, snap := range overdue {
		p, err := park(snap, "No complete answer arrived in time.")
		if err != nil {
			return parked, err
		}
		switch err := s.apply(ctx, snap, p); {
		case core.HasCode(err, core.CodeConflict):
			// The issue moved while it was being parked; the next tick looks again.
		case err != nil:
			return parked, err
		default:
			parked = append(parked, snap.record.IssueID)
		}
	}
	return parked, loadErr
}

// OverdueClarification returns the earliest deadline among the issues whose
// wait for an answer is over and that are not parked yet; zero when there
// is none. Like ExpireClarifications it reads only the operational store.
func (s *Service) OverdueClarification(ctx context.Context) (time.Time, error) {
	overdue, err := s.overdueClarifications(ctx, s.cfg.Clock.Now())
	var earliest time.Time
	for _, snap := range overdue {
		if deadline := snap.doc.Clarify.Deadline; earliest.IsZero() || deadline.Before(earliest) {
			earliest = deadline
		}
	}
	return earliest, err
}

// overdueClarifications loads the issues waiting for an answer, not parked,
// whose deadline is not after by.
func (s *Service) overdueClarifications(ctx context.Context, by time.Time) ([]snapshot, error) {
	var waiting []core.StateRecord
	err := s.cfg.State.View(ctx, func(ctx context.Context, tx state.Tx) error {
		var err error
		waiting, err = tx.ListRecords(ctx, state.RecordFilter{States: []core.State{core.StateClarifying}})
		return err
	})
	if err != nil {
		return nil, err
	}
	var overdue []snapshot
	for _, record := range waiting {
		snap, err := s.load(ctx, record.IssueID)
		if err != nil {
			return overdue, err
		}
		if snap.record.State == core.StateClarifying && !snap.doc.Clarify.NeedsInfo && !by.Before(snap.doc.Clarify.Deadline) {
			overdue = append(overdue, snap)
		}
	}
	return overdue, nil
}
