package install

import (
	"context"
	"os"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
)

// checkState opens the Standalone state file as the control zone and checks
// that it is migrated and holds the budget confirmation the gate looks for.
// In Docker mode the store is not reachable from the host (that is the point
// of the network layout), so its check is the control zone's verdict above.
func (in *Installer) checkState(ctx context.Context, c *collector, m *manifest.Manifest) {
	if m.Mode != manifest.ModeStandalone {
		return
	}
	// Opening would create an empty database; a missing file is a finding.
	if _, err := os.Stat(in.Layout.StateDB()); err != nil {
		c.fail("state store", "no state database at %s: %s", in.Layout.StateDB(), startHint)
		return
	}
	store, err := sqlstore.Open(ctx, sqlstore.Config{
		Dialect: sqlstore.DialectSQLite, DSN: in.Layout.StateDB(), FactoryID: m.FactoryID, Zone: core.ZoneControl, Clock: in.Clock,
	})
	if err != nil {
		c.fail("state store", "cannot open the database: %v", err)
		return
	}
	defer func() { _ = store.Close() }() // read-only use; nothing is pending
	_, confirmed, err := ReadBudgetConfirmation(ctx, store)
	if err != nil {
		c.fail("state store", "the database is not usable: %v", err)
		return
	}
	if !confirmed {
		c.warn("state store", "open and migrated, but holds no budget confirmation: the dispatcher refuses every claim until `sisf up --confirm-unlimited`")
		return
	}
	c.ok("state store", "open and migrated, budget confirmation recorded")
}
