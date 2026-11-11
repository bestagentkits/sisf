package install

import (
	"time"

	"github.com/bestagentkits/sisf/cmd/factoryd/wiring"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// extReconcileInterval is the manifest extension holding the control
// zone's interval of reconciliation with GitHub, as a Go duration; absent
// means control's default.
const extReconcileInterval = "github_reconcile_interval"

// checkReconcileInterval refuses an interval control would refuse at start.
func checkReconcileInterval(d time.Duration) error {
	if d != 0 && (d < wiring.MinReconcileInterval || d > wiring.MaxReconcileInterval) {
		return invalidf("the GitHub reconciliation interval must be between %s and %s", wiring.MinReconcileInterval, wiring.MaxReconcileInterval)
	}
	return nil
}

// reconcileExtensions records a chosen interval in the manifest.
func (o Options) reconcileExtensions(ext map[string]any) {
	if o.ReconcileInterval != 0 {
		ext[extReconcileInterval] = o.ReconcileInterval.String()
	}
}

// reconcileIntervalOf reads the interval an install was given; one that
// cannot be read is left to control, which refuses it at start.
func reconcileIntervalOf(m *manifest.Manifest) time.Duration {
	raw, _ := m.Extensions[extReconcileInterval].(string)
	d, _ := time.ParseDuration(raw)
	return d
}

// reconcileEnv is the control unit's setting for the interval, or nothing
// for the default.
func (o Options) reconcileEnv() []string {
	if o.ReconcileInterval == 0 {
		return nil
	}
	return []string{wiring.EnvReconcileInterval + "=" + o.ReconcileInterval.String()}
}

// reconcileString is the interval as the Compose file carries it.
func reconcileString(d time.Duration) string {
	if d == 0 {
		return ""
	}
	return d.String()
}
