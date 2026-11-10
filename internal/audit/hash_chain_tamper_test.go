package audit

import (
	"errors"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// forge rewrites who acted in a stored record and returns the new body with
// a hash that is correct for it, as a careful intruder would produce.
func forge(t *testing.T, stored core.AuditEvent) (hash string, body []byte) {
	t.Helper()
	stored.ActorID = "someone-else"
	hash, body, err := seal(stored)
	if err != nil {
		t.Fatalf("forge a record: %v", err)
	}
	return hash, body
}

// wantTamperedAt fails the test unless Verify reports tampering at seq.
func wantTamperedAt(t *testing.T, log *Log, seq string) {
	t.Helper()
	err := log.Verify(ctx)
	wantCode(t, err, core.CodeTampered)
	var typed *core.Error
	if !errors.As(err, &typed) || typed.Details["seq"] != seq {
		t.Fatalf("tampering must be reported at record %s, got %v with %v", seq, err, typed)
	}
}

func TestUntouchedChainVerifies(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		log := openLog(t, db)
		if err := log.Verify(ctx); err != nil {
			t.Fatalf("an empty log must verify: %v", err)
		}
		appendEvents(t, log, 5)
		if err := log.Verify(ctx); err != nil {
			t.Fatalf("an untouched log must verify: %v", err)
		}
	})
}

func TestDatabaseRefusesToEditOrDeleteAnAuditRecord(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		log := openLog(t, db)
		appendEvents(t, log, 3)
		raw := db.Raw(t)
		for name, statement := range map[string]string{
			"edit":        `UPDATE audit_log SET body = body WHERE seq = 2`,
			"renumber":    `UPDATE audit_log SET seq = 9 WHERE seq = 2`,
			"delete one":  `DELETE FROM audit_log WHERE seq = 2`,
			"delete all":  `DELETE FROM audit_log`,
			"delete last": `DELETE FROM audit_log WHERE seq = 3`,
		} {
			if _, err := raw.ExecContext(ctx, statement); err == nil {
				t.Errorf("%s: the database accepted a change to a written audit record", name)
			}
		}
		if err := log.Verify(ctx); err != nil {
			t.Fatalf("refused changes must leave the chain intact: %v", err)
		}
		if events, err := log.Events(ctx, 0, 0); err != nil || len(events) != 3 {
			t.Fatalf("events after the refused changes = %d, %v", len(events), err)
		}
	})
}

func TestEditedRecordIsDetected(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		log := openLog(t, db)
		events := appendEvents(t, log, 4)
		_, body := forge(t, events[1])
		tamper(t, db, `UPDATE audit_log SET body = ? WHERE seq = 2`, body)
		wantTamperedAt(t, log, "2")
	})
}

func TestEditedRecordWithAMatchingHashIsDetectedByTheNextLink(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		log := openLog(t, db)
		events := appendEvents(t, log, 4)
		hash, body := forge(t, events[1])
		tamper(t, db, `UPDATE audit_log SET body = ?, hash = ? WHERE seq = 2`, body, hash)
		// Record 2 is consistent in itself; record 3 still points at the old one.
		wantTamperedAt(t, log, "3")
	})
}

func TestEditedLastRecordWithAMatchingHashIsDetectedByTheHead(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		log := openLog(t, db)
		events := appendEvents(t, log, 4)
		hash, body := forge(t, events[3])
		tamper(t, db, `UPDATE audit_log SET body = ?, hash = ? WHERE seq = 4`, body, hash)
		wantTamperedAt(t, log, "4")
	})
}

func TestDeletedRecordIsDetected(t *testing.T) {
	t.Parallel()
	for name, deletion := range map[string]struct{ statement, reportedAt string }{
		"first record":  {`DELETE FROM audit_log WHERE seq = 1`, "1"},
		"middle record": {`DELETE FROM audit_log WHERE seq = 3`, "3"},
		"last record":   {`DELETE FROM audit_log WHERE seq = 4`, "4"},
		"every record":  {`DELETE FROM audit_log`, "1"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
				log := openLog(t, db)
				appendEvents(t, log, 4)
				tamper(t, db, deletion.statement)
				wantTamperedAt(t, log, deletion.reportedAt)
			})
		})
	}
}

func TestReorderedRecordsAreDetected(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		log := openLog(t, db)
		appendEvents(t, log, 4)
		// Records 2 and 3 trade places.
		tamper(t, db, `UPDATE audit_log SET seq = 99 WHERE seq = 2`)
		tamper(t, db, `UPDATE audit_log SET seq = 2 WHERE seq = 3`)
		tamper(t, db, `UPDATE audit_log SET seq = 3 WHERE seq = 99`)
		wantTamperedAt(t, log, "2")
	})
}

func TestRecordAddedPastTheLogIsDetected(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		log := openLog(t, db)
		events := appendEvents(t, log, 2)
		// A well-formed record is slipped in after the last one, bypassing Append.
		last := events[1]
		planted := approval(9)
		planted.FactoryID, planted.At = last.FactoryID, last.At
		planted.Seq, planted.PrevHash = last.Seq+1, last.Hash
		hash, body, err := seal(planted)
		if err != nil {
			t.Fatal(err)
		}
		tamper(t, db, `INSERT INTO audit_log (factory_id, seq, prev_hash, hash, body) VALUES (?, ?, ?, ?, ?)`,
			string(planted.FactoryID), planted.Seq, planted.PrevHash, hash, body)
		wantTamperedAt(t, log, "3")
	})
}

func TestRewrittenHeadIsDetected(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		log := openLog(t, db)
		appendEvents(t, log, 3)
		// The head is rolled back so that the last record looks never written.
		tamper(t, db, `UPDATE audit_head SET seq = 2`)
		wantCode(t, log.Verify(ctx), core.CodeTampered)
	})
}

func TestReadingATamperedRecordFails(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		log := openLog(t, db)
		appendEvents(t, log, 2)
		tamper(t, db, `UPDATE audit_log SET body = ? WHERE seq = 2`, []byte(`{"actor_id":"someone-else"}`))
		_, err := log.Events(ctx, 0, 0)
		wantCode(t, err, core.CodeTampered)
	})
}
