package sqlstore_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// everyOperation calls each operation of a transaction once. The arguments
// are well formed, so the only reason for a refusal is the zone of the store.
func everyOperation(lease state.Lease) map[string]func(tx state.Tx) error {
	return map[string]func(tx state.Tx) error{
		"get record":   func(tx state.Tx) error { _, err := tx.GetRecord(ctx, issue(7)); return err },
		"list records": func(tx state.Tx) error { _, err := tx.ListRecords(ctx, state.RecordFilter{}); return err },
		"transition": func(tx state.Tx) error {
			_, err := tx.Transition(ctx, step(issue(7), core.StateIntake, core.EventInfoMissing, core.StateClarifying, 1, "asked"))
			return err
		},
		"acquire lease":  func(tx state.Tx) error { _, err := tx.AcquireLease(ctx, "other", "owner", time.Minute); return err },
		"renew lease":    func(tx state.Tx) error { _, err := tx.RenewLease(ctx, lease, time.Minute); return err },
		"check fencing":  func(tx state.Tx) error { return tx.CheckFencing(ctx, lease.Resource, lease.Token) },
		"release lease":  func(tx state.Tx) error { return tx.ReleaseLease(ctx, lease) },
		"pending inbox":  func(tx state.Tx) error { _, err := tx.PendingDeliveries(ctx, 10); return err },
		"mark processed": func(tx state.Tx) error { return tx.MarkDeliveryProcessed(ctx, "github", "d-1") },
		"enqueue":        func(tx state.Tx) error { return tx.Enqueue(ctx, state.OutboxMessage{Topic: "t", Key: "k"}) },
		"pending outbox": func(tx state.Tx) error { _, err := tx.PendingOutbox(ctx, 10); return err },
		"mark sent":      func(tx state.Tx) error { return tx.MarkOutboxSent(ctx, 1) },
		"mark failed":    func(tx state.Tx) error { return tx.MarkOutboxFailed(ctx, 2, time.Time{}, "x") },
		"reserve":        func(tx state.Tx) error { return tx.PutReservation(ctx, reservation("r-2", 1)) },
		"get reserved":   func(tx state.Tx) error { _, err := tx.GetReservation(ctx, "r-1"); return err },
		"append usage":   func(tx state.Tx) error { return tx.AppendUsage(ctx, spend(1, true, time.Time{})) },
		"budget totals":  func(tx state.Tx) error { _, err := tx.BudgetTotals(ctx, time.Time{}); return err },
		"get document":   func(tx state.Tx) error { _, err := tx.GetKV(ctx, "policy", "revision"); return err },
		"put document":   func(tx state.Tx) error { _, err := tx.PutKV(ctx, state.KVEntry{Namespace: "n", Key: "k"}); return err },
		"list documents": func(tx state.Tx) error { _, err := tx.ListKV(ctx, "policy", "", 10); return err },
		"delete document": func(tx state.Tx) error {
			return tx.DeleteKV(ctx, "policy", "revision", 1)
		},
	}
}

// seed gives every operation something to act on and returns a held lease.
func seed(t *testing.T, control *sqlstore.Store) state.Lease {
	t.Helper()
	var lease state.Lease
	mustUpdate(t, control, func(tx state.Tx) (err error) {
		if _, err = tx.Transition(ctx, received(issue(7))); err != nil {
			return err
		}
		if _, err = tx.AcceptDelivery(ctx, state.InboxEntry{Source: "github", DeliveryID: "d-1"}); err != nil {
			return err
		}
		for _, key := range []string{"first", "second"} {
			if err = tx.Enqueue(ctx, state.OutboxMessage{Topic: "t", Key: key}); err != nil {
				return err
			}
		}
		if err = tx.PutReservation(ctx, reservation("r-1", 1)); err != nil {
			return err
		}
		if _, err = tx.PutKV(ctx, state.KVEntry{Namespace: "policy", Key: "revision"}); err != nil {
			return err
		}
		lease, err = tx.AcquireLease(ctx, "dispatcher", "dispatcher-1", time.Hour)
		return err
	})
	return lease
}

func TestApplicationZoneReachesNothingInTheFactoryStore(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		control := db.Open(t, core.ZoneControl)
		lease := seed(t, control)
		application := db.Open(t, core.ZoneApplication)

		operations := everyOperation(lease)
		operations["accept delivery"] = func(tx state.Tx) error {
			_, err := tx.AcceptDelivery(ctx, state.InboxEntry{Source: "github", DeliveryID: "forged"})
			return err
		}
		for name, operation := range operations {
			if err := update(application, operation); !core.HasCode(err, core.CodePermissionDenied) {
				t.Errorf("%s from the application zone: want %s, got %v", name, core.CodePermissionDenied, err)
			}
		}
		_, err := application.AppendAuditRow(ctx, func(int64, string) (string, []byte, error) { return "h", []byte("{}"), nil })
		wantCode(t, err, core.CodePermissionDenied)
		wantCode(t, application.ViewAudit(ctx, func(*sqlstore.AuditView) error { return nil }), core.CodePermissionDenied)

		// Nothing the application zone tried took effect.
		if got := record(t, control, issue(7)); got.State != core.StateIntake || got.Version != 1 {
			t.Fatalf("record after the refused writes = %+v", got)
		}
		if got := pendingDeliveries(t, control, 0); len(got) != 1 || got[0] != "d-1" {
			t.Fatalf("inbox after the refused writes = %v", got)
		}
	})
}

func TestAgentZoneWorksOnRunsAndReachesNothingElse(t *testing.T) {
	t.Parallel()
	// What the dispatcher does: it reads records, holds leases, reserves
	// budget and meters usage. The rest of everyOperation is the control
	// zone's: an intake step, the inbox, the outbox, the budget totals and
	// the policy documents.
	agentMay := map[string]bool{
		"get record": true, "list records": true, "acquire lease": true, "renew lease": true,
		"check fencing": true, "release lease": true, "reserve": true, "get reserved": true, "append usage": true,
	}
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		control := db.Open(t, core.ZoneControl)
		lease := seed(t, control)
		agent := db.Open(t, core.ZoneAgent)

		// A delivery is trusted as verified, so only the zone that verifies
		// webhooks may store one.
		wantCode(t, update(agent, func(tx state.Tx) error {
			_, err := tx.AcceptDelivery(ctx, state.InboxEntry{Source: "github", DeliveryID: "forged"})
			return err
		}), core.CodePermissionDenied)
		if got := pendingDeliveries(t, control, 0); len(got) != 1 {
			t.Fatalf("inbox after the refused delivery = %v", got)
		}
		_, err := agent.AppendAuditRow(ctx, func(int64, string) (string, []byte, error) { return "h", []byte("{}"), nil })
		wantCode(t, err, core.CodePermissionDenied)
		wantCode(t, agent.ViewAudit(ctx, func(*sqlstore.AuditView) error { return nil }), core.CodePermissionDenied)

		// Each operation is undone again, so one does not disturb the next.
		undone := errors.New("undo")
		for name, operation := range everyOperation(lease) {
			err := update(agent, func(tx state.Tx) error {
				if err := operation(tx); err != nil {
					return err
				}
				return undone
			})
			switch {
			case agentMay[name] && !errors.Is(err, undone):
				t.Errorf("%s from the agent zone: %v", name, err)
			case !agentMay[name] && !core.HasCode(err, core.CodePermissionDenied):
				t.Errorf("%s from the agent zone: want %s, got %v", name, core.CodePermissionDenied, err)
			}
		}
	})
}

func TestGrantsGiveEachZoneRoleOnlyWhatItsZoneNeeds(t *testing.T) {
	t.Parallel()
	roles := sqlstore.ZoneRoles{
		core.ZoneApplication: "sisf_application", core.ZoneAgent: "sisf_agent", core.ZoneControl: "sisf_control",
	}
	statements, err := sqlstore.GrantStatements("public", roles)
	if err != nil {
		t.Fatal(err)
	}
	grantsTo := func(role string) []string {
		var grants []string
		for _, statement := range statements {
			if strings.HasPrefix(statement, "GRANT ") && strings.HasSuffix(statement, ` TO "`+role+`"`) {
				grants = append(grants, statement)
			}
		}
		return grants
	}
	// Every role first loses whatever it held, so a rerun also narrows.
	for _, role := range roles {
		reset := false
		for _, statement := range statements {
			if strings.HasPrefix(statement, "REVOKE ALL PRIVILEGES ON TABLE ") && strings.HasSuffix(statement, ` FROM "`+role+`"`) {
				reset = strings.Contains(statement, `"audit_log"`) && strings.Contains(statement, `"records"`)
			}
		}
		if !reset {
			t.Errorf("role %s is not reset before it is granted", role)
		}
	}
	for _, grant := range grantsTo("sisf_application") {
		if strings.Contains(grant, " ON TABLE ") && !strings.Contains(grant, `"schema_migrations"`) {
			t.Errorf("the application role is granted a factory table: %s", grant)
		}
	}
	for role := range map[string]bool{"sisf_application": true, "sisf_agent": true, "sisf_control": true} {
		for _, grant := range grantsTo(role) {
			appendOnly := strings.Contains(grant, `"audit_log"`) || strings.Contains(grant, `"usage_ledger"`)
			if appendOnly && (strings.Contains(grant, "UPDATE") || strings.Contains(grant, "DELETE") || strings.Contains(grant, "TRUNCATE") || strings.Contains(grant, "ALL")) {
				t.Errorf("role %s may change an append-only table: %s", role, grant)
			}
		}
	}
	// The agent role reaches the inbox, the outbox and the audit log not at
	// all, creates no record and deletes nothing.
	for _, grant := range grantsTo("sisf_agent") {
		for _, table := range []string{`"inbox"`, `"outbox"`, `"audit_log"`, `"audit_head"`} {
			if strings.Contains(grant, table) {
				t.Errorf("the agent role is granted %s: %s", table, grant)
			}
		}
		if strings.Contains(grant, "DELETE") || (strings.Contains(grant, `"records"`) && strings.Contains(grant, "INSERT")) {
			t.Errorf("the agent role is granted more than it uses: %s", grant)
		}
	}
	controlInbox := ""
	for _, grant := range grantsTo("sisf_control") {
		if strings.Contains(grant, `"inbox"`) {
			controlInbox = grant
		}
	}
	if !strings.Contains(controlInbox, "INSERT") {
		t.Errorf("the control role must store deliveries, got %q", controlInbox)
	}
}

func TestGrantsRefuseUnsafeOrSharedRoleNames(t *testing.T) {
	t.Parallel()
	valid := func() sqlstore.ZoneRoles {
		return sqlstore.ZoneRoles{
			core.ZoneApplication: "sisf_application", core.ZoneAgent: "sisf_agent", core.ZoneControl: "sisf_control",
		}
	}
	for name, change := range map[string]func(sqlstore.ZoneRoles){
		"statement in a role name": func(r sqlstore.ZoneRoles) { r[core.ZoneAgent] = `x"; DROP TABLE audit_log; --` },
		"empty role":               func(r sqlstore.ZoneRoles) { r[core.ZoneAgent] = "" },
		"missing zone":             func(r sqlstore.ZoneRoles) { delete(r, core.ZoneApplication) },
		"role shared by two zones": func(r sqlstore.ZoneRoles) { r[core.ZoneApplication] = r[core.ZoneControl] },
		"role for a non-zone":      func(r sqlstore.ZoneRoles) { r[core.ZoneShared] = "sisf_shared" },
	} {
		roles := valid()
		change(roles)
		if _, err := sqlstore.GrantStatements("public", roles); !core.HasCode(err, core.CodeInvalidArgument) {
			t.Errorf("%s: want %s, got %v", name, core.CodeInvalidArgument, err)
		}
	}
	if _, err := sqlstore.GrantStatements(`public"; DROP SCHEMA public; --`, valid()); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Errorf("an unsafe schema name must be refused, got %v", err)
	}
}
