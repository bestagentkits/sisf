package sqlstoretest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
)

// ZoneLogins are PostgreSQL login roles made for one test, one per zone,
// each granted what its zone may reach, as the installer sets them up.
type ZoneLogins struct {
	Roles sqlstore.ZoneRoles
	// DSN connects to the test database as the zone's role, with its own
	// password, the way the zone's process connects.
	DSN map[core.Zone]string
}

// ZoneLogins creates the login roles and grants them on the test database.
// The roles are dropped when the test ends. It skips the test on SQLite,
// which has no roles, and when the test connection cannot create roles.
func (d *Database) ZoneLogins(t *testing.T) ZoneLogins {
	t.Helper()
	if d.Dialect != sqlstore.DialectPostgres {
		t.Skip("database roles exist on PostgreSQL only")
	}
	ctx := context.Background()
	raw := d.Raw(t)
	suffix := randomHex(t, 6)
	logins := ZoneLogins{Roles: sqlstore.ZoneRoles{}, DSN: map[core.Zone]string{}}
	for _, zone := range core.Zones() {
		role := "sisf_t_" + string(zone) + "_" + suffix
		password := randomHex(t, 16)
		// Both values are generated hex, so they are safe in the statement.
		if _, err := raw.ExecContext(ctx, `CREATE ROLE `+role+` LOGIN NOBYPASSRLS PASSWORD '`+password+`'`); err != nil {
			t.Skipf("the test connection cannot create roles: %v", err)
		}
		t.Cleanup(func() {
			_, _ = raw.ExecContext(ctx, `DROP OWNED BY `+role)
			_, _ = raw.ExecContext(ctx, `DROP ROLE `+role)
		})
		logins.Roles[zone] = role
		logins.DSN[zone] = withLogin(d.DSN, role, password)
	}
	if err := d.Open(t, core.ZoneControl).GrantZoneRoles(ctx, logins.Roles); err != nil {
		t.Fatalf("grant the zone roles: %v", err)
	}
	return logins
}

// OpenWith opens the store as zone over dsn, for a test that connects with
// another role than the database owner. The store is closed when the test
// ends.
func (d *Database) OpenWith(t testing.TB, dsn string, zone core.Zone) *sqlstore.Store {
	t.Helper()
	store, err := sqlstore.Open(context.Background(), sqlstore.Config{
		Dialect: d.Dialect, DSN: dsn, FactoryID: Factory, Zone: zone, Clock: d.Clock,
	})
	if err != nil {
		t.Fatalf("open %s store as %s: %v", d.Dialect, zone, err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// withLogin replaces the user and password of a connection string. It
// handles both the URL and the keyword/value form; in the latter the last
// value of a keyword wins.
func withLogin(dsn, user, password string) string {
	if !strings.Contains(dsn, "://") {
		return dsn + " user=" + user + " password=" + password
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		return dsn
	}
	parsed.User = url.UserPassword(user, password)
	return parsed.String()
}

// randomHex returns n random bytes in hex.
func randomHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("read random bytes: %v", err)
	}
	return hex.EncodeToString(b)
}
