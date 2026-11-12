package install

import (
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/cmd/factoryd/wiring"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// The reconciliation interval chosen at init is a non-secret setting: it
// is kept in factory.yaml, read back as the install's options and handed
// to the control unit in both modes; one control would refuse is refused
// at init, and none given leaves control's default.
func TestTheReconcileIntervalTravelsFromInitToTheControlUnit(t *testing.T) {
	t.Parallel()
	o := testOptions(manifest.ModeStandalone)
	o.ReconcileInterval = 15 * time.Second
	o, err := o.normalize()
	if err != nil {
		t.Fatal(err)
	}
	m := buildManifest(o)
	if got := reconcileIntervalOf(m); got != 15*time.Second {
		t.Fatalf("factory.yaml holds %v, want 15s", got)
	}
	read := Options{ReconcileInterval: reconcileIntervalOf(m)}
	if env := strings.Join(read.reconcileEnv(), " "); env != wiring.EnvReconcileInterval+"=15s" {
		t.Fatalf("the control unit gets %q", env)
	}
	if env := (Options{}).reconcileEnv(); env != nil {
		t.Fatalf("no interval given still sets %v", env)
	}
	for _, bad := range []time.Duration{time.Second, 2 * time.Hour} {
		o := testOptions(manifest.ModeStandalone)
		o.ReconcileInterval = bad
		if _, err := o.normalize(); err == nil {
			t.Errorf("an interval of %s was accepted", bad)
		}
	}

	_, compose := renderCompose(t, func(o *Options) { o.ReconcileInterval = 20 * time.Second })
	if !strings.Contains(string(compose), `SISF_GITHUB_RECONCILE_INTERVAL: "20s"`) {
		t.Fatal("the Compose control service does not carry the interval")
	}
	_, compose = renderCompose(t, nil)
	if strings.Contains(string(compose), "SISF_GITHUB_RECONCILE_INTERVAL") {
		t.Fatal("the Compose file sets an interval nobody chose")
	}
}
