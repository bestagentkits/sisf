package main

import (
	"context"
	"log/slog"
	"slices"

	"github.com/bestagentkits/sisf/internal/supervisor/programstore"
	"github.com/bestagentkits/sisf/internal/supervisor/release"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// inFlightStates are the states of an issue whose candidate was built and
// whose release has not ended.
var inFlightStates = []core.State{core.StateQAEvaluation, core.StateReleaseCandidate, core.StateCanary, core.StateMonitoring, core.StateVerified}

// inFlightDigests are the digests the records of issues in inFlightStates
// name: the programs a release may still start.
func inFlightDigests(records []core.StateRecord) []core.Digest {
	var out []core.Digest
	for _, r := range records {
		if !slices.Contains(inFlightStates, r.State) {
			continue
		}
		for _, d := range []core.Digest{r.CandidateDigest, core.Digest(r.ReleaseDigest)} {
			if d != "" && !slices.Contains(out, d) {
				out = append(out, d)
			}
		}
	}
	return out
}

// programRetention keeps the program store down to what can still run:
// the last known good release and the candidates in flight. A program
// whose release was rolled back, refused by QA or replaced as the last
// known good is removed.
type programRetention struct {
	store    *programstore.Store
	lkg      func(ctx context.Context) (core.Digest, error)
	inFlight func(ctx context.Context) ([]core.Digest, error)
}

// newProgramRetention reads the last known good release from book and the
// candidates in flight from the state store.
func newProgramRetention(store *programstore.Store, book *release.LKGBook, records state.Store) *programRetention {
	return &programRetention{
		store: store,
		lkg: func(ctx context.Context) (core.Digest, error) {
			lkg, _, err := book.Get(ctx, adoptedUnit)
			return core.Digest(lkg.Digest), err
		},
		inFlight: func(ctx context.Context) ([]core.Digest, error) {
			var digests []core.Digest
			err := records.View(ctx, func(ctx context.Context, tx state.Tx) error {
				found, err := tx.ListRecords(ctx, state.RecordFilter{States: inFlightStates})
				digests = inFlightDigests(found)
				return err
			})
			return digests, err
		},
	}
}

// sweep removes the programs that are neither the last known good release
// nor in flight and returns their names. It runs between rounds of the
// stages, when no build is exporting. When either cannot be read it
// removes nothing: a program is only ever removed on evidence.
func (r *programRetention) sweep(ctx context.Context) ([]string, error) {
	lkg, err := r.lkg(ctx)
	if err != nil {
		return nil, err
	}
	keep, err := r.inFlight(ctx)
	if err != nil {
		return nil, err
	}
	return r.store.Retain(append(keep, lkg)...)
}

// sweepPrograms runs the retention after a round of the stages, when the
// factory keeps programs, and logs what it removed.
func (l *lifecycleFlags) sweepPrograms(ctx context.Context, log *slog.Logger) {
	if l.retention == nil {
		return
	}
	removed, err := l.retention.sweep(ctx)
	if err != nil && ctx.Err() == nil {
		log.Warn("the program store could not be swept; nothing was removed", "code", string(core.CodeOf(err)), "error", err.Error())
	}
	for _, name := range removed {
		log.Info("removed a program that is neither the last known good release nor in flight", "program", name)
	}
}
