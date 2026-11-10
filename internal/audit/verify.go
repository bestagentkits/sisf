package audit

import (
	"context"

	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
)

// verifyPage is how many records Verify reads at a time.
const verifyPage = 500

// Verify walks the whole chain, from the first record to the head, on one
// snapshot of the store. It returns TAMPERED, naming the first record at
// which the chain is broken, when
//
//   - a record's content no longer matches its hash (edited),
//   - a record does not follow the one before it, by number or by hash
//     (removed, inserted or reordered), or
//   - the chain does not end where the head says it ends (records cut off
//     the end, added past it, or the head itself rewritten).
//
// A nil error means every record is the one that was appended, in the order
// it was appended, and none is missing.
func (l *Log) Verify(ctx context.Context) error {
	return l.store.ViewAudit(ctx, func(view *sqlstore.AuditView) error {
		headSeq, headHash, err := view.Head(ctx)
		if err != nil {
			return err
		}
		var (
			lastSeq  int64
			lastHash string
		)
		for {
			rows, err := view.Rows(ctx, lastSeq, verifyPage)
			if err != nil {
				return err
			}
			for _, row := range rows {
				if row.Seq != lastSeq+1 {
					return tampered(lastSeq+1, "the record is missing or out of order")
				}
				if row.PrevHash != lastHash {
					return tampered(row.Seq, "it does not follow the record before it")
				}
				if _, err := l.open(row); err != nil {
					return err
				}
				lastSeq, lastHash = row.Seq, row.Hash
			}
			if len(rows) < verifyPage {
				break
			}
		}
		switch {
		case lastSeq < headSeq:
			return tampered(lastSeq+1, "the record is missing from the end of the chain")
		case lastSeq > headSeq:
			return tampered(headSeq+1, "the record lies past the end of the chain")
		case lastHash != headHash:
			return tampered(headSeq, "it is not the record the chain ended with")
		}
		return nil
	})
}
