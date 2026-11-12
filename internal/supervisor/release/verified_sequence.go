package release

import (
	"context"
	"math"
	"strconv"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// verifiedSequenceKey is the key, in RecoveryNamespace, of the counter of
// Verified steps: its value is the number the last Verified step was given.
// It is written in the transaction of each step, at the version it was read
// at, so two steps never take the same number and a step takes a number
// greater than that of every step committed before it, whatever the host's
// clock says.
const verifiedSequenceKey = "verified-sequence"

// nextVerifiedSequence takes the next number of the counter of Verified
// steps in tx, the transaction of the step it orders. The counter is a
// document of the control zone, which factoryd shares, so it is not the
// only floor of the number: the number is also greater than floor, the
// number of the unit's LKG on the trust volume, and than the number of
// every release still Verified. A counter deleted or set back thus cannot
// give a step a number that orders it before a release verified earlier.
func nextVerifiedSequence(ctx context.Context, tx state.Tx, floor int64) (int64, error) {
	version, last := int64(0), floor
	existing, err := tx.GetKV(ctx, RecoveryNamespace, verifiedSequenceKey)
	switch {
	case err == nil:
		version = existing.Version
		if n, err := strconv.ParseInt(string(existing.Value), 10, 64); err == nil {
			last = max(last, n)
		}
	case !core.HasCode(err, core.CodeNotFound):
		return 0, err
	}
	pending, err := tx.ListRecords(ctx, state.RecordFilter{States: []core.State{core.StateVerified}})
	if err != nil {
		return 0, err
	}
	for _, r := range pending {
		due, err := ReadFinishDue(ctx, tx, r.RunID)
		switch {
		case err == nil:
			last = max(last, due.Sequence)
		case !core.HasCode(err, core.CodeNotFound) && !core.HasCode(err, core.CodeTampered):
			return 0, err
		}
	}
	if last < 0 || last == math.MaxInt64 {
		return 0, core.NewError(core.CodeTampered, "the order of Verified steps cannot be continued from "+strconv.FormatInt(last, 10))
	}
	next := last + 1
	if _, err := tx.PutKV(ctx, state.KVEntry{
		Namespace: RecoveryNamespace, Key: verifiedSequenceKey, Value: []byte(strconv.FormatInt(next, 10)), Version: version,
	}); err != nil {
		return 0, err
	}
	return next, nil
}

// verifiedDue is what a release still owes once its Verified step is
// written: due with the install in force at the step, which the root-signed
// release manifest record names by when it was recorded, and, set in the
// step's transaction by putVerifiedDue, the step's place in the order of
// Verified steps, which is greater than floor, the number of the unit's LKG.
// A manifest record or an LKG that cannot be read now fails the step.
func (p *Pipeline) verifiedDue(ctx context.Context, due FinishDue) (_ FinishDue, floor int64, _ error) {
	installed, found, err := p.cfg.LKG.fromManifest(ctx, due.Unit)
	if err != nil {
		return FinishDue{}, 0, err
	}
	if found {
		due.Install = installed.RecordedAt.UTC()
	}
	lkg, found, err := p.cfg.LKG.Get(ctx, due.Unit)
	if err != nil {
		return FinishDue{}, 0, err
	}
	if found {
		floor = lkg.VerifiedSequence
	}
	return due, floor, nil
}

// putVerifiedDue numbers the Verified step of due's release, above floor,
// and records what the release owes, in the step's transaction.
func putVerifiedDue(ctx context.Context, tx state.Tx, due *FinishDue, floor int64) error {
	seq, err := nextVerifiedSequence(ctx, tx, floor)
	if err != nil {
		return err
	}
	due.Sequence = seq
	return putFinishDue(ctx, tx, *due)
}

// installedSince reports whether the install pinning lkg is another than
// the one in force when the release that owes due was verified. A release
// whose due names no install, verified before any was recorded or by an
// earlier build, is not taken as superseded by one.
func installedSince(due FinishDue, lkg LKG) bool {
	return !due.Install.IsZero() && !lkg.RecordedAt.Equal(due.Install)
}
