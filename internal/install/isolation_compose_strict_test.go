package install

import (
	"strings"
	"testing"
)

// The checker reads the Compose file strictly and refuses every setting that
// would widen a zone's reach past the mount, network and hardening rules.
// Each case starts from the generated file and changes one thing; the file
// as generated passes.
func TestTheCheckerRefusesEverySettingThatWidensAZonesReach(t *testing.T) {
	t.Parallel()
	in, out := renderCompose(t, nil)
	if vs, err := CheckCompose(in.Layout, out); err != nil || len(vs) != 0 {
		t.Fatalf("the generated file must pass: violations [%s] (%v)", violationRules(vs), err)
	}
	svc := func(m map[string]any, name string) map[string]any { return m[name].(map[string]any) }
	secrets := in.Layout.Dir + "/secrets"
	cases := map[string]struct {
		rule, says string
		edit       func(services, root map[string]any)
	}{
		"a named volume binds the control secrets": {ruleVolume, "driver", func(_, root map[string]any) {
			root["volumes"].(map[string]any)["app-data"] = map[string]any{
				"name": "sisf-x-app-data", "driver_opts": map[string]any{"type": "none", "o": "bind", "device": secrets + "/control"},
			}
		}},
		"a named volume has another driver": {ruleVolume, "driver", func(_, root map[string]any) {
			root["volumes"].(map[string]any)["app-data"].(map[string]any)["driver"] = "local-persist"
		}},
		"a config exposes the control env file": {ruleSecretMount, "config", func(_, root map[string]any) {
			root["configs"] = map[string]any{"leak": map[string]any{"file": in.Layout.RuntimeEnv(areaControl)}}
		}},
		"a secret exposes the agent env file": {ruleSecretMount, "secret", func(_, root map[string]any) {
			root["secrets"] = map[string]any{"leak": map[string]any{"file": in.Layout.RuntimeEnv(areaAgent)}}
		}},
		"a service is handed a secret": {ruleSecretMount, "secrets", func(s, _ map[string]any) {
			svc(s, "app")["secrets"] = []any{"leak"}
		}},
		"zone 1 takes the mounts of control": {ruleVolume, "mounts of", func(s, _ map[string]any) {
			svc(s, "app")["volumes_from"] = []any{"control:ro"}
		}},
		"a service gets a host device": {ruleHardening, "devices", func(s, _ map[string]any) {
			svc(s, "dispatcher")["devices"] = []any{"/dev/sda:/dev/sda"}
		}},
		"a service leaves user namespacing": {ruleHardening, "userns_mode", func(s, _ map[string]any) {
			svc(s, "dispatcher")["userns_mode"] = "host"
		}},
		"a service shares the host cgroup namespace": {ruleHardening, "cgroup", func(s, _ map[string]any) {
			svc(s, "dispatcher")["cgroup"] = "host"
		}},
		"a service shares the host UTS namespace": {ruleHardening, "uts", func(s, _ map[string]any) {
			svc(s, "app")["uts"] = "host"
		}},
		"a service shares the host IPC namespace": {ruleHardening, "ipc", func(s, _ map[string]any) {
			svc(s, "app")["ipc"] = "host"
		}},
		"a service shares the host PID namespace": {ruleHardening, "pid", func(s, _ map[string]any) {
			svc(s, "dispatcher")["pid"] = "host"
		}},
		"zone 1 joins the PID namespace of control": {ruleHardening, "pid", func(s, _ map[string]any) {
			svc(s, "app")["pid"] = "service:control"
		}},
		"seccomp is turned off": {ruleHardening, "seccomp:unconfined", func(s, _ map[string]any) {
			svc(s, "dispatcher")["security_opt"] = []any{confinementOption, "seccomp:unconfined"}
		}},
		"AppArmor is turned off": {ruleHardening, "apparmor:unconfined", func(s, _ map[string]any) {
			svc(s, "dispatcher")["security_opt"] = []any{confinementOption, "apparmor:unconfined"}
		}},
		"SELinux labels are turned off": {ruleHardening, "label:disable", func(s, _ map[string]any) {
			svc(s, "app")["security_opt"] = []any{confinementOption, "label:disable"}
		}},
		"a capability is added": {ruleHardening, "capabilities", func(s, _ map[string]any) {
			svc(s, "dispatcher")["cap_add"] = []any{"SYS_ADMIN"}
		}},
		"a service is privileged": {ruleHardening, "privileged", func(s, _ map[string]any) {
			svc(s, "edge")["privileged"] = true
		}},
	}
	for name, tc := range cases {
		vs, err := CheckCompose(in.Layout, edited(t, out, tc.edit))
		if err != nil {
			t.Errorf("%s: want a %s violation, got the error %v", name, tc.rule, err)
			continue
		}
		found := false
		for _, v := range vs {
			found = found || (v.Rule == tc.rule && strings.Contains(v.Message, tc.says))
		}
		if !found {
			t.Errorf("%s: want a %s violation that says %q, got %v", name, tc.rule, tc.says, vs)
		}
	}
}

// A key the deployment does not write refuses the whole file, wherever it
// is: in a service, at the top level, in a network or a volume, or in the
// hardening block the services merge.
func TestTheCheckerRefusesAComposeFileWithAKeyTheDeploymentDoesNotWrite(t *testing.T) {
	t.Parallel()
	in, out := renderCompose(t, nil)
	svc := func(m map[string]any, name string) map[string]any { return m[name].(map[string]any) }
	cases := map[string][]byte{
		"a service key": edited(t, out, func(s, _ map[string]any) {
			svc(s, "app")["extra_hosts"] = []any{"control:10.0.0.2"}
		}),
		"a long-form mount": edited(t, out, func(s, _ map[string]any) {
			svc(s, "app")["volumes"] = []any{map[string]any{"type": "bind", "source": "/", "target": "/host"}}
		}),
		"a top-level key": edited(t, out, func(_, root map[string]any) {
			root["include"] = []any{"other.yaml"}
		}),
		"an external volume": edited(t, out, func(_, root map[string]any) {
			root["volumes"].(map[string]any)["app-data"].(map[string]any)["external"] = true
		}),
		"a network driver": edited(t, out, func(_, root map[string]any) {
			root["networks"].(map[string]any)["app"].(map[string]any)["driver"] = "host"
		}),
		"a key merged from the hardening block": mergedIntoHardening(t, out, "  extra_hosts: [\"control:10.0.0.2\"]"),
	}
	for name, data := range cases {
		if _, err := CheckCompose(in.Layout, data); err == nil {
			t.Errorf("%s: the file must be refused as unreadable", name)
		}
	}
}

// What the hardening block sets is checked in every service that merges it.
func TestTheCheckerChecksWhatTheServicesMergeFromTheHardeningBlock(t *testing.T) {
	t.Parallel()
	in, out := renderCompose(t, nil)
	vs, err := CheckCompose(in.Layout, mergedIntoHardening(t, out, "  pid: host"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"app", "control", "dispatcher", "edge", "supervisor"} {
		found := false
		for _, v := range vs {
			found = found || (v.Rule == ruleHardening && v.Service == name && strings.Contains(v.Message, "pid"))
		}
		if !found {
			t.Errorf("%s merges pid: host from the hardening block unrefused: %v", name, vs)
		}
	}
}

// mergedIntoHardening adds line to the template's hardening block, which the
// services merge, in the text of the file so the merge is kept.
func mergedIntoHardening(t *testing.T, data []byte, line string) []byte {
	t.Helper()
	const anchor = "x-hardened: &hardened\n"
	if !strings.Contains(string(data), anchor) {
		t.Fatalf("the generated file has no hardening block")
	}
	return []byte(strings.Replace(string(data), anchor, anchor+line+"\n", 1))
}
