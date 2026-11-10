package install

import (
	"strings"
	"testing"
)

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
		"dispatcher is pointed at a Docker API": {ruleSocket, func(s, _ map[string]any) {
			svc(s, "dispatcher")["environment"].(map[string]any)["DOCKER_HOST"] = "tcp://socket-proxy:2375"
		}},
		"a service mounts the socket read-only": {ruleSocket, func(s, _ map[string]any) {
			svc(s, "control")["volumes"] = []any{"/var/run/docker.sock:/var/run/docker.sock:ro"}
		}},
		"a service mounts another runtime socket": {ruleSocket, func(s, _ map[string]any) {
			svc(s, "dispatcher")["volumes"] = []any{"/run/containerd/containerd.sock:/c.sock"}
		}},
		"the dispatcher is privileged": {ruleSocket, func(s, _ map[string]any) { svc(s, "dispatcher")["privileged"] = true }},
		"a socket proxy service comes back": {ruleProxy, func(s, _ map[string]any) {
			s["socket-proxy"] = map[string]any{"image": "tecnativa/docker-socket-proxy:v0.5.0@sha256:" + strings.Repeat("a", 64)}
		}},
		"a service runs a socket proxy image": {ruleProxy, func(s, _ map[string]any) {
			svc(s, "edge")["image"] = "tecnativa/docker-socket-proxy@sha256:" + strings.Repeat("a", 64)
		}},
		"the edge joins the factory network with the state store": {ruleNetwork, func(s, _ map[string]any) {
			svc(s, "edge")["networks"] = []any{"app", "control", "factory"}
		}},
		"the state store joins the edge's control network": {ruleNetwork, func(s, _ map[string]any) {
			svc(s, "state")["networks"] = []any{"factory", "control"}
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
			svc(s, "control")["environment"].(map[string]any)["EDGE_ADMIN_TOKEN"] = "x"
		}},
		"the sign-in client secret is written inline": {ruleSecretInline, func(s, _ map[string]any) {
			svc(s, "control")["environment"].(map[string]any)["SISF_AUTH_OIDC_CLIENT_SECRET"] = "x"
		}},
		"the counters token is written inline": {ruleSecretInline, func(s, _ map[string]any) {
			svc(s, "control")["environment"].(map[string]any)["EDGE_COUNTERS_TOKEN"] = "x"
		}},
		"the fake identity provider is published on every interface": {rulePort, func(s, _ map[string]any) {
			svc(s, "control")["ports"] = append(svc(s, "control")["ports"].([]any), "0.0.0.0:7468:7468")
		}},
		"the fake identity provider leaves control's namespace": {ruleNetwork, func(s, _ map[string]any) {
			svc(s, "sim-oidc")["network_mode"] = "service:app"
		}},
		"the app mounts the supervisor's admin socket": {ruleVolume, func(s, _ map[string]any) {
			svc(s, "app")["volumes"] = []any{"supervisor-admin:/run/sisf/admin"}
		}},
		"the dispatcher mounts the supervisor's admin socket": {ruleVolume, func(s, _ map[string]any) {
			svc(s, "dispatcher")["volumes"] = []any{"supervisor-admin:/run/sisf/admin"}
		}},
		"the edge mounts the supervisor's admin socket": {ruleVolume, func(s, _ map[string]any) {
			svc(s, "edge")["volumes"] = []any{"supervisor-admin:/run/sisf/admin"}
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
