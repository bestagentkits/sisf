//go:build e2e

package install

import (
	"database/sql"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// startPostgres runs a throwaway server on 127.0.0.1:7469 (the one port the
// project reserves for it) and returns the superuser DSN. The container and
// its volume are removed when the test ends.
func startPostgres(t *testing.T) string {
	t.Helper()
	requireDocker(t)
	name := e2eName(t, "pg")
	password := randomToken(t)
	image := imageOr(e2eImageEnv, PostgresImage)
	removeLater(t, name)
	if _, err := docker(t, "run", "-d", "--name", name, "-p", "127.0.0.1:7469:5432",
		"-e", "POSTGRES_PASSWORD="+password, "-e", "POSTGRES_DB=sisf", image); err != nil {
		t.Skipf("cannot start the throwaway PostgreSQL (is 127.0.0.1:7469 free, is %s available?): %v", image, err)
	}
	dsn := "postgres://postgres:" + url.QueryEscape(password) + "@127.0.0.1:7469/sisf?sslmode=disable"
	// Readiness is polled against a deadline: the server needs a few seconds,
	// and there is no event to wait for from outside the container.
	deadline := time.Now().Add(60 * time.Second)
	for {
		db, err := sql.Open("pgx", dsn)
		if err != nil {
			t.Fatal(err)
		}
		pingErr := db.PingContext(t.Context())
		_ = db.Close()
		if pingErr == nil {
			return dsn
		}
		if time.Now().After(deadline) {
			t.Fatalf("PostgreSQL did not become ready: %v", pingErr)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func TestZone1CannotReachTheControlStoreRoleOnALivePostgres(t *testing.T) {
	dsn := startPostgres(t)
	ctx := t.Context()
	passwords := map[core.Zone]string{}
	for _, z := range core.Zones() {
		passwords[z] = randomToken(t)
	}
	err := Bootstrap(ctx, BootstrapConfig{
		FactoryID: "sisf-e2e", Dialect: sqlstore.DialectPostgres, DSN: dsn,
		Roles: zoneRoles, Passwords: passwords, Clock: sqlstoretest.NewClock(fixedStart),
	})
	if err != nil {
		t.Fatal(err)
	}
	asRole := func(role, password string) *sql.DB {
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		u.User = url.UserPassword(role, password)
		db, err := sql.Open("pgx", u.String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		return db
	}
	app := asRole(zoneRoles[core.ZoneApplication], passwords[core.ZoneApplication])

	// denied fails the test unless the statement is refused for lack of privilege.
	denied := func(what, query string) {
		t.Helper()
		_, err := app.ExecContext(ctx, query)
		var pg *pgconn.PgError
		switch {
		case err == nil:
			t.Errorf("Zone 1 could %s", what)
		case errors.As(err, &pg) && pg.Code != "42501":
			t.Errorf("Zone 1 %s was refused for another reason: %s %s", what, pg.Code, pg.Message)
		}
	}
	for _, table := range []string{"records", "inbox", "audit_log", "kv", "leases", "outbox"} {
		denied("read "+table, `SELECT count(*) FROM `+table)
	}
	denied("write the budget confirmation", `INSERT INTO kv(factory_id, namespace, entry_key, entry_value, version, updated_at) VALUES ('sisf-e2e','install','budget_confirmation','\x7b7d',1,0)`)
	denied("become the control role", `SET ROLE `+zoneRoles[core.ZoneControl])
	denied("create objects", `CREATE TABLE zone1_scratch(x int)`)
	denied("read the password hashes", `SELECT rolpassword FROM pg_authid`)

	// Zone 1's password opens Zone 1's role and no other.
	for _, z := range []core.Zone{core.ZoneAgent, core.ZoneControl} {
		other := asRole(zoneRoles[z], passwords[core.ZoneApplication])
		if other.PingContext(ctx) == nil {
			t.Errorf("Zone 1's password opened the %s zone's role", z)
		}
	}
	// What Zone 1 may do still works: the roles are not simply locked out.
	if err := app.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations`).Scan(new(int)); err != nil {
		t.Errorf("Zone 1 cannot read the schema version: %v", err)
	}
}

// A container that is built the way a Zone 1 or runner service is in the
// Compose project (no secrets mounted, no raw Docker socket, an internal
// network, read-only, no capabilities) cannot see the secrets of the other
// zones and cannot leave its network.
func TestContainersOfTheSandboxCannotSeeZoneTwoOrThreeSecretsOrReachTheFactory(t *testing.T) {
	requireDocker(t)
	in, _, _ := newTestInstaller(t)
	mustInit(t, in, manifest.ModeDocker)

	network := e2eName(t, "sandbox")
	if _, err := docker(t, "network", "create", "--internal", network); err != nil {
		t.Skipf("cannot create a network: %v", err)
	}
	removeNetworkLater(t, network)

	name := e2eName(t, "runner")
	removeLater(t, name)
	image := imageOr(e2eRunnerImageEnv, "busybox:1.37")
	// Only the application area's env file is given, as a Zone 1 service gets.
	out, err := docker(t, "run", "--rm", "--name", name, "--network", network, "--read-only", "--cap-drop", "ALL",
		"--security-opt", "no-new-privileges:true", "--env-file", in.Layout.RuntimeEnv(areaApplication), image,
		"sh", "-c", `env; echo ---; ls -d /etc/sisf /run/secrets /var/run/docker.sock 2>&1; echo ---; wget -q -T 3 -O /dev/null http://host.docker.internal:7463/healthz 2>&1; echo "wget=$?"`)
	if err != nil {
		t.Fatalf("the runner-like container failed: %v", err)
	}
	seen := 0
	for _, area := range []string{areaAgent, areaControl, areaState} {
		raw, err := os.ReadFile(in.Layout.RuntimeEnv(area))
		if err != nil {
			continue // the state area has no such file
		}
		for _, line := range strings.Split(string(raw), "\n") {
			_, value, ok := strings.Cut(line, "=")
			if !ok || len(value) < 16 {
				continue
			}
			seen++
			if strings.Contains(out, value) {
				t.Errorf("the container sees a secret of the %s area", area)
			}
		}
	}
	if seen == 0 {
		t.Fatal("the test compared nothing: no secret of another area was found to look for")
	}
	for _, path := range []string{"/etc/sisf", "/run/secrets", "/var/run/docker.sock"} {
		if !strings.Contains(out, path+": No such file") && !strings.Contains(out, "ls: "+path+": No such file") {
			t.Errorf("%s is not reported missing in the container:\n%s", path, out)
		}
	}
	if strings.Contains(out, "wget=0") {
		t.Error("the container on the internal network reached a service outside it")
	}
}
