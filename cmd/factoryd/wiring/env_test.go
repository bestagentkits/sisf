package wiring

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
)

func envOf(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func TestReadEnvNeedsTheManifestAndTheStore(t *testing.T) {
	t.Parallel()
	for name, vars := range map[string]map[string]string{
		"no manifest":      {EnvStateDSN: "/tmp/x.db"},
		"no store":         {EnvConfig: "/etc/sisf/factory.yaml"},
		"bad loopback":     {EnvConfig: "f", EnvStateDSN: "d", EnvAllowNonLoopback: "sometimes"},
		"bad factory name": {EnvConfig: "f", EnvStateDSN: "d", EnvFactoryID: "Not A Factory!"},
	} {
		if _, err := ReadEnv(envOf(vars)); !core.HasCode(err, core.CodeInvalidArgument) {
			t.Errorf("%s: err = %v, want INVALID_ARGUMENT", name, err)
		}
	}
	env, err := ReadEnv(envOf(map[string]string{EnvConfig: " f ", EnvStateDSN: "d", EnvAllowNonLoopback: "true",
		EnvWebhookSecret: "webhook-secret-value"}))
	if err != nil || env.Config != "f" || !env.AllowNonLoopback {
		t.Fatalf("env = %+v, %v", env, err)
	}
	if strings.Contains(env.WebhookSecret.String(), "webhook-secret-value") {
		t.Fatal("a secret read from the environment prints as itself")
	}
}

func TestTheApprovalKeyMustBeAWholeSeed(t *testing.T) {
	t.Parallel()
	seed := make([]byte, ed25519.SeedSize)
	seed[0] = 7
	good := base64.RawURLEncoding.EncodeToString(seed)
	key, err := ParseApprovalKey(core.NewSecretValue([]byte(good)))
	if err != nil || !key.Equal(ed25519.NewKeyFromSeed(seed)) {
		t.Fatalf("a valid seed was refused: %v", err)
	}
	for name, raw := range map[string]string{
		"missing":   "",
		"short":     base64.RawURLEncoding.EncodeToString(seed[:16]),
		"not b64":   "not*base64",
		"too long":  base64.RawURLEncoding.EncodeToString(append(seed, 1)),
		"std pad=1": base64.StdEncoding.EncodeToString(seed),
	} {
		_, err := ParseApprovalKey(core.NewSecretValue([]byte(raw)))
		if !core.HasCode(err, core.CodeInvalidArgument) {
			t.Errorf("%s: err = %v", name, err)
		}
		if raw != "" && err != nil && strings.Contains(err.Error(), raw) {
			t.Errorf("%s: the error repeats the key", name)
		}
	}
}

func TestDialectOfTellsPostgresFromASQLiteFile(t *testing.T) {
	t.Parallel()
	for dsn, want := range map[string]sqlstore.Dialect{
		"postgres://u:p@state:5432/sisf":   sqlstore.DialectPostgres,
		"postgresql://u:p@state:5432/sisf": sqlstore.DialectPostgres,
		"/var/lib/sisf/state/factory.db":   sqlstore.DialectSQLite,
		"data/state/factory.db":            sqlstore.DialectSQLite,
	} {
		if got := DialectOf(dsn); got != want {
			t.Errorf("DialectOf(%q) = %v, want %v", dsn, got, want)
		}
	}
}

func TestListenRefusesAnExposedAddressUnlessAllowed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	if _, err := Listen(ctx, "0.0.0.0:0", false); !core.HasCode(err, core.CodePermissionDenied) {
		t.Fatalf("an exposed address was opened: %v", err)
	}
	if _, err := Listen(ctx, "no-port", false); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Fatalf("a malformed address was accepted: %v", err)
	}
	ln, err := Listen(ctx, "127.0.0.1:0", false)
	if err != nil {
		t.Fatal(err)
	}
	_ = ln.Close()
}

func TestAZoneIsHealthyOnlyWhenItAnswers200(t *testing.T) {
	t.Parallel()
	var status atomic.Int32
	status.Store(http.StatusOK)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(int(status.Load())) }))
	t.Cleanup(srv.Close)
	probe := HTTPProbe(srv.URL)
	if err := probe.Probe(context.Background()); err != nil {
		t.Fatalf("a healthy zone failed its probe: %v", err)
	}
	for _, s := range []int{http.StatusServiceUnavailable, http.StatusFound, http.StatusNoContent} {
		status.Store(int32(s))
		if err := probe.Probe(context.Background()); !core.HasCode(err, core.CodeUnavailable) {
			t.Errorf("status %d counted as healthy: %v", s, err)
		}
	}
}

func TestReconciliationSettingsDefaultAndRefuseValuesOutOfBounds(t *testing.T) {
	t.Parallel()
	base := map[string]string{EnvConfig: "f", EnvStateDSN: "d"}
	env, err := ReadEnv(envOf(base))
	if err != nil {
		t.Fatal(err)
	}
	if rc, err := env.Reconcile.withDefaults(); err != nil || rc.Interval != DefaultReconcileInterval ||
		rc.MaxPages != DefaultReconcileMaxPages || rc.Timeout != DefaultReconcileTimeout {
		t.Fatalf("unset reconciliation settings = %+v, %v; want the defaults", rc, err)
	}
	with := func(k, v string) map[string]string {
		vars := map[string]string{k: v}
		for name, value := range base {
			vars[name] = value
		}
		return vars
	}
	env, err = ReadEnv(envOf(with(EnvReconcileInterval, "5m")))
	if err != nil || env.Reconcile.Interval != 5*time.Minute {
		t.Fatalf("interval = %s, %v", env.Reconcile.Interval, err)
	}
	env, err = ReadEnv(envOf(with(EnvReconcileMaxPages, "3")))
	if err != nil || env.Reconcile.MaxPages != 3 {
		t.Fatalf("max pages = %d, %v", env.Reconcile.MaxPages, err)
	}
	for name, vars := range map[string]map[string]string{
		"not a duration":   with(EnvReconcileInterval, "often"),
		"zero interval":    with(EnvReconcileInterval, "0s"),
		"too often":        with(EnvReconcileInterval, "1s"),
		"too rare":         with(EnvReconcileInterval, "2h"),
		"not a number":     with(EnvReconcileMaxPages, "many"),
		"no pages":         with(EnvReconcileMaxPages, "0"),
		"more than client": with(EnvReconcileMaxPages, "51"),
	} {
		if _, err := ReadEnv(envOf(vars)); !core.HasCode(err, core.CodeInvalidArgument) {
			t.Errorf("%s: err = %v, want INVALID_ARGUMENT", name, err)
		}
	}
}
