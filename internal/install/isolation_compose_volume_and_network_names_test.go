package install

import "testing"

// Each volume and network is named after its own key, so that one zone's
// key cannot point at another zone's volume or network on the host.
func TestEveryVolumeAndNetworkIsNamedAfterItsOwnKey(t *testing.T) {
	t.Parallel()
	in, out := renderCompose(t, nil)
	project := composeFileOf(t, out).Name
	cases := map[string]func(services, root map[string]any){
		"the application's volume is the control volume": func(_, root map[string]any) {
			root["volumes"].(map[string]any)["app-data"].(map[string]any)["name"] = project + "-control-data"
		},
		"the agents' workspace is the trust volume": func(_, root map[string]any) {
			root["volumes"].(map[string]any)["workspaces"].(map[string]any)["name"] = project + "-trust"
		},
		"a volume under a longer name of its own": func(_, root map[string]any) {
			root["volumes"].(map[string]any)["app-data"].(map[string]any)["name"] = project + "-app-data-2"
		},
		"the application's network is the control network": func(_, root map[string]any) {
			root["networks"].(map[string]any)["app"].(map[string]any)["name"] = project + "-control"
		},
		"the sandbox is the factory network": func(_, root map[string]any) {
			root["networks"].(map[string]any)["sandbox"].(map[string]any)["name"] = project + "-factory"
		},
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			vs, err := CheckCompose(in.Layout, edited(t, out, edit))
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, v := range vs {
				found = found || v.Rule == ruleNaming
			}
			if !found {
				t.Fatalf("want a %s violation, got [%s]", ruleNaming, violationRules(vs))
			}
		})
	}
}
