package release

import (
	"context"
	"strconv"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// verifiedSequenceKey is the key, in RecoveryNamespace, of the counter of
// Verified steps. The store raises a document's version by one on every
// write, in the transaction of the write, so the version the counter is
// written at is a number no other Verified step was given and greater than
// that of every step committed before it, whatever the host's clock says.
const verifiedSequenceKey = "verified-sequence"

// nextVerifiedSequence takes the next number of the counter of Verified
// steps in tx, the transaction of the step it orders.
func nextVerifiedSequence(ctx context.Context, tx state.Tx) (int64, error) {
	read := int64(0)
	existing, err := tx.GetKV(ctx, RecoveryNamespace, verifiedSequenceKey)
	switch {
	case err == nil:
		read = existing.Version
	case !core.HasCode(err, core.CodeNotFound):
		return 0, err
	}
	written, err := tx.PutKV(ctx, state.KVEntry{
		Namespace: RecoveryNamespace, Key: verifiedSequenceKey, Value: []byte(strconv.FormatInt(read+1, 10)), Version: read,
	})
	if err != nil {
		return 0, err
	}
	return written.Version, nil
}

// verifiedDue is what a release still owes once its Verified step is
// written: due with the install in force at the step, which the root-signed
// release manifest record names by when it was recorded, and, set in the
// step's transaction by putVerifiedDue, the step's place in the order of
// Verified steps. A manifest record that cannot be read now fails the step.
func (p *Pipeline) verifiedDue(ctx context.Context, due FinishDue) (FinishDue, error) {
	installed, found, err := p.cfg.LKG.fromManifest(ctx, due.Unit)
	if err != nil {
		return FinishDue{}, err
	}
	if found {
		due.Install = installed.RecordedAt.UTC()
	}
	return due, nil
}

// putVerifiedDue numbers the Verified step of due's release and records
// what the release owes, in the step's transaction.
func putVerifiedDue(ctx context.Context, tx state.Tx, due *FinishDue) error {
	seq, err := nextVerifiedSequence(ctx, tx)
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
