package install

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

func envOf(pairs ...string) func(string) string {
	m := map[string]string{}
	for i := 0; i+1 < len(pairs); i += 2 {
		m[pairs[i]] = pairs[i+1]
	}
	return func(k string) string { return m[k] }
}

func bootstrapEnv(t *testing.T) BootstrapEnv {
	t.Helper()
	return BootstrapEnv{
		FactoryID: "sisf-hello-app", Dialect: sqlstore.DialectPostgres,
		Getenv: envOf(
			EnvStateOwnerDSN, "postgres://owner:owner-pw@state:5432/sisf",
			"SISF_ZONE_PASSWORD_APPLICATION", "pw-one", "SISF_ZONE_PASSWORD_AGENT", "pw-two", "SISF_ZONE_PASSWORD_CONTROL", "pw-three"),
		Clock: sqlstoretest.NewClock(fixedStart),
	}
}

func TestBootstrapFromTheEnvironmentRefusesMissingOrSharedCredentials(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		edit func(*BootstrapEnv)
		want string
	}{
		"no owner connection": {func(e *BootstrapEnv) { e.Getenv = envOf() }, EnvStateOwnerDSN},
		"no agent password": {func(e *BootstrapEnv) {
			base := e.Getenv
			e.Getenv = func(k string) string {
				if k == "SISF_ZONE_PASSWORD_AGENT" {
					return ""
				}
				return base(k)
			}
		}, "SISF_ZONE_PASSWORD_AGENT"},
		"two zones share a password": {func(e *BootstrapEnv) {
			base := e.Getenv
			e.Getenv = func(k string) string {
				if k == "SISF_ZONE_PASSWORD_AGENT" {
					return "pw-one"
				}
				return base(k)
			}
		}, "same password"},
		"unknown dialect":    {func(e *BootstrapEnv) { e.Dialect = "mysql" }, "dialect"},
		"unsettled alert":    {func(e *BootstrapEnv) { e.Budget = BudgetChoice{Confirmed: true} }, "alert"},
		"invalid factory id": {func(e *BootstrapEnv) { e.FactoryID = "Not Valid" }, ""},
	} {
		e := bootstrapEnv(t)
		tc.edit(&e)
		err := RunBootstrap(t.Context(), e)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to mention %q", name, err, tc.want)
		}
		if err != nil && strings.Contains(err.Error(), "pw-one") {
			t.Errorf("%s: the error repeats a password", name)
		}
	}
}

func TestBootstrapFromTheEnvironmentPreparesASQLiteStore(t *testing.T) {
	t.Parallel()
	e := BootstrapEnv{
		FactoryID: "sisf-hello-app", Dialect: sqlstore.DialectSQLite, SQLitePath: filepath.Join(t.TempDir(), "factory.db"),
		Budget: BudgetChoice{Confirmed: true, AlertOff: true}, Getenv: envOf(), Clock: sqlstoretest.NewClock(fixedStart),
	}
	if err := RunBootstrap(t.Context(), e); err != nil {
		t.Fatal(err)
	}
	store, err := sqlstore.Open(t.Context(), sqlstore.Config{
		Dialect: e.Dialect, DSN: e.SQLitePath, FactoryID: e.FactoryID, Zone: core.ZoneControl, Clock: e.Clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	rec, ok, err := ReadBudgetConfirmation(t.Context(), store)
	if err != nil || !ok || !rec.AlertOff {
		t.Fatalf("stored confirmation = %+v %v %v", rec, ok, err)
	}
	e.SQLitePath = ""
	if err := RunBootstrap(t.Context(), e); err == nil {
		t.Fatal("a SQLite store without a path was accepted")
	}
}
