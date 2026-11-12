package audit

import (
	"context"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

var ctx = context.Background()

// approval is an Owner approving a scope through the dashboard.
func approval(issue int) core.AuditEvent {
	return core.AuditEvent{
		ActorID: "owner-1", ActorKind: core.ActorHuman, Surface: core.SurfaceDashboard,
		Action: "approvals.approve", Resource: "approval/a-1",
		SubjectDigest: "sha256:0000000000000000000000000000000000000000000000000000000000000001",
		PolicySHA:     "1111111111111111111111111111111111111111",
		IssueID:       core.IssueRef{Owner: "acme", Repo: "app", Number: issue},
		Outcome:       core.OutcomeExecuted,
		Details:       map[string]string{"decision": "approve", "scope": "kind:feature"},
	}
}

// openLog returns an audit log of the control zone on db.
func openLog(t *testing.T, db *sqlstoretest.Database) *Log {
	t.Helper()
	return New(db.Open(t, core.ZoneControl), db.Clock)
}

// appendEvents writes n approvals and returns them as stored.
func appendEvents(t *testing.T, log *Log, n int) []core.AuditEvent {
	t.Helper()
	events := make([]core.AuditEvent, 0, n)
	for i := range n {
		stored, err := log.Append(ctx, approval(i+1))
		if err != nil {
			t.Fatalf("append event %d: %v", i+1, err)
		}
		events = append(events, stored)
	}
	return events
}

// wantCode fails the test unless err carries exactly code.
func wantCode(t *testing.T, err error, code core.ErrorCode) {
	t.Helper()
	if !core.HasCode(err, code) {
		t.Fatalf("want %s, got %v", code, err)
	}
}

// tamper runs one statement straight on the database, past the store, after
// removing the triggers that keep the audit table append-only. It stands for
// an intruder who owns the database.
func tamper(t *testing.T, db *sqlstoretest.Database, statement string, args ...any) {
	t.Helper()
	db.DisableAppendOnlyGuards(t, "audit_log")
	result, err := db.Raw(t).ExecContext(ctx, db.Dialect.Rebind(statement), args...)
	if err != nil {
		t.Fatalf("tamper: %v", err)
	}
	if changed, err := result.RowsAffected(); err != nil || changed == 0 {
		t.Fatalf("the tampering statement changed nothing (%d rows, %v)", changed, err)
	}
}
