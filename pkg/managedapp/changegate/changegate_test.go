package changegate

import (
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/appbundle"
	"github.com/bestagentkits/sisf/pkg/managedapp"
)

func shopSpec(t *testing.T) *managedapp.Spec {
	t.Helper()
	s, err := managedapp.Load("../../../examples/woocommerce-app/" + managedapp.FileName)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func lock(t *testing.T, lines ...string) *appbundle.Lock {
	t.Helper()
	l, err := appbundle.ParseLock(strings.NewReader(strings.Join(lines, "\n")))
	if err != nil {
		t.Fatal(err)
	}
	return &l
}

const (
	core704  = "core wordpress 7.0.4 f0885ad10c35ac87e70517bc5789275cd54ccac052222a58acfb7b9f870082b0"
	core708  = "core wordpress 7.0.8 0c67b8c0af90cc77787f1e48ed32e33353d6775a20d4c892b5654afd52445284"
	woo1121  = "plugin woocommerce 11.2.1 5317434cb381b5fdc7c1dd6ee258324f08c390976aaf8d1199f2a0e9a3ab8586"
	woo1122  = "plugin woocommerce 11.2.2 a4746183efd951de5837396aad119f7cf508f06cb699ccb590a01cada4c44932"
	cf7      = "plugin contact-form-7 6.1.3 a5feaa9988f31cea6b052865d1c3ee080daa04ec23ffab7266a63f448e9c90d1"
	newThing = "plugin hello-dolly 1.7.2 " + "1111111111111111111111111111111111111111111111111111111111111111"
)

// The good update of the trial: a WooCommerce patch release and a change to
// the shop's own plugin are the factory's to release.
func TestAPatchAndAShopPluginFixAreTheFactorys(t *testing.T) {
	t.Parallel()
	v := Judge(Input{
		Baseline: shopSpec(t), BaselineLock: lock(t, core704, woo1121, cf7), CandidateLock: lock(t, core704, woo1122, cf7),
		Changed: []string{"content.lock", "plugins/sisf-shop/sisf-shop.php"},
	})
	if v.Owner || len(v.Lock) != 1 || v.Lock[0].Class != managedapp.LockPatch {
		t.Fatalf("%+v", v)
	}
}

// Every sensitive class the Owner named needs a signed approval: a Core
// upgrade, a new plugin, the payment plugin, the site configuration and the
// Spec itself.
func TestSensitiveChangesNeedTheOwner(t *testing.T) {
	t.Parallel()
	base := lock(t, core704, woo1121, cf7)
	cases := map[string]struct {
		cand    *appbundle.Lock
		changed []string
		want    string
	}{
		"core upgrade":    {lock(t, core708, woo1121, cf7), []string{"content.lock"}, "is a core change"},
		"new plugin":      {lock(t, core704, woo1121, cf7, newThing), []string{"content.lock"}, "is a added change"},
		"payment plugin":  {base, []string{"plugins/sisf-sandbox-pay/sisf-sandbox-pay.php"}, "protected"},
		"payment subtree": {base, []string{"plugins/sisf-sandbox-pay/inc/refund.php"}, "protected"},
		"site config":     {base, []string{"wp-config.php"}, "protected"},
		"must-use plugin": {base, []string{"mu-plugins/sisf-health.php"}, "protected"},
		"the spec":        {base, []string{"sisf-app.yaml"}, "edits sisf-app.yaml"},
		"outside the app": {base, []string{"../other/file"}, "outside"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			v := Judge(Input{Baseline: shopSpec(t), BaselineLock: base, CandidateLock: c.cand, Changed: c.changed})
			if !v.Owner || !strings.Contains(strings.Join(v.Reasons, ";"), c.want) {
				t.Fatalf("want an Owner decision mentioning %q, got %+v", c.want, v)
			}
		})
	}
}

// What the gate cannot read is never released by the factory alone.
func TestAnUnreadableChangeIsTheOwners(t *testing.T) {
	t.Parallel()
	if v := Judge(Input{Baseline: shopSpec(t), Changed: []string{"content.lock"}}); !v.Owner {
		t.Fatalf("an unread lockfile change: %+v", v)
	}
	if v := Judge(Input{Changed: []string{"plugins/sisf-shop/x.php"}}); !v.Owner {
		t.Fatalf("no baseline: %+v", v)
	}
}
