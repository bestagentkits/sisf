package install

import "testing"

// Negative cases: each breaks exactly one rule and must be caught.
func TestTheCheckerCatchesEveryWayOfBreakingTheBoundaries(t *testing.T) {
	t.Parallel()
	in, out := renderCompose(t, nil)
	svc := func(m map[string]any, name string) map[string]any { return m[name].(map[string]any) }
	cases := map[string]struct {
		rule string
		edit func(services, root map[string]any)
	}{
		"zone 1 reads the control env file": {ruleSecretMount, func(s, _ map[string]any) {
			svc(s, "app")["env_file"] = []any{in.Layout.RuntimeEnv(areaControl)}
		}},
		"dispatcher reads the control env file": {ruleSecretMount, func(s, _ map[string]any) {
			svc(s, "dispatcher")["env_file"] = []any{in.Layout.RuntimeEnv(areaControl)}
		}},
		"the secrets directory is mounted": {ruleSecretMount, func(s, _ map[string]any) {
			svc(s, "app")["volumes"] = []any{in.Layout.Dir + "/secrets:/secrets:ro"}
		}},
		"dispatcher gets the raw socket": {ruleSocket, func(s, _ map[string]any) {
			svc(s, "dispatcher")["volumes"] = []any{"/var/run/docker.sock:/var/run/docker.sock"}
		}},
		"dispatcher bypasses the proxy": {ruleSocket, func(s, _ map[string]any) {
			svc(s, "dispatcher")["environment"].(map[string]any)["DOCKER_HOST"] = "unix:///var/run/docker.sock"
		}},
		"the proxy allows exec": {ruleProxy, func(s, _ map[string]any) {
			svc(s, "socket-proxy")["environment"].(map[string]any)["EXEC"] = "1"
		}},
		"the proxy leaves images to a default": {ruleProxy, func(s, _ map[string]any) {
			delete(svc(s, "socket-proxy")["environment"].(map[string]any), "IMAGES")
		}},
		"zone 1 joins the control network": {ruleNetwork, func(s, _ map[string]any) {
			svc(s, "app")["networks"] = []any{"app", "control"}
		}},
		"the state store joins the app network": {ruleNetwork, func(s, _ map[string]any) {
			svc(s, "state")["networks"] = []any{"app", "factory", "control"}
		}},
		"the dispatcher joins the sandbox": {ruleNetwork, func(s, _ map[string]any) {
			svc(s, "dispatcher")["networks"] = []any{"factory", "sandbox"}
		}},
		"a service uses host networking": {ruleNetwork, func(s, _ map[string]any) {
			delete(svc(s, "app"), "networks")
			svc(s, "app")["network_mode"] = "host"
		}},
		"the sandbox is not internal": {ruleNetwork, func(_, root map[string]any) {
			root["networks"].(map[string]any)["sandbox"].(map[string]any)["internal"] = false
		}},
		"an image moves with its tag":  {ruleImagePinned, func(s, _ map[string]any) { svc(s, "edge")["image"] = "nginx:latest" }},
		"a port is public":             {rulePort, func(s, _ map[string]any) { svc(s, "edge")["ports"] = []any{"0.0.0.0:7460:7460"} }},
		"a port is outside the range":  {rulePort, func(s, _ map[string]any) { svc(s, "edge")["ports"] = []any{"127.0.0.1:8080:7460"} }},
		"a container lacks the prefix": {ruleNaming, func(s, _ map[string]any) { svc(s, "app")["container_name"] = "hello" }},
		"a service is privileged":      {ruleHardening, func(s, _ map[string]any) { svc(s, "app")["privileged"] = true }},
		"a secret is written inline": {ruleSecretInline, func(s, _ map[string]any) {
			svc(s, "control")["environment"].(map[string]any)["SISF_OWNER_TOKEN"] = "x"
		}},
		"zone 1 mounts a control volume": {ruleVolume, func(s, _ map[string]any) {
			svc(s, "app")["volumes"] = []any{"trust:/trust"}
		}},
		"an unknown service appears": {ruleService, func(s, _ map[string]any) {
			s["miner"] = map[string]any{"image": fakeID}
		}},
	}
	for name, tc := range cases {
		vs, err := CheckCompose(in.Layout, edited(t, out, tc.edit))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		found := false
		for _, v := range vs {
			found = found || v.Rule == tc.rule
		}
		if !found {
			t.Errorf("%s: want a %s violation, got [%s]", name, tc.rule, violationRules(vs))
		}
	}
}
