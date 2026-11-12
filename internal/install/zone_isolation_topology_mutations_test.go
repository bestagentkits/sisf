package install

import (
	"slices"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
)

// replaceArg swaps one argument of a service's command for another, or
// appends the new one when old is "".
func replaceArg(svc map[string]any, old, repl string) {
	cmd := svc["command"].([]any)
	if old == "" {
		svc["command"] = append(cmd, repl)
		return
	}
	for i, a := range cmd {
		if a == old {
			cmd[i] = repl
		}
	}
}

// Negative cases for the release topology: each widens one element the
// supervisor's lifecycle added (the Docker request filters, the docker
// network, the edge's inner listeners, the app's release labels, the trust
// step, the relay and the new env files) and must be refused.
func TestTheCheckerRefusesEveryWideningOfTheReleaseTopology(t *testing.T) {
	t.Parallel()
	in, out := renderCompose(t, nil)
	svc := func(m map[string]any, name string) map[string]any { return m[name].(map[string]any) }
	f := composeFileOf(t, out)
	digestLabel := f.Services["app"].Labels["sisf.digest"]
	cases := map[string]struct {
		rule string
		edit func(services, root map[string]any)
	}{
		"a filter mounts the socket writable": {ruleSocket, func(s, _ map[string]any) {
			svc(s, filterRunner)["volumes"] = []any{"/var/run/docker.sock:/var/run/docker.sock"}
		}},
		"a filter mounts a host directory too": {ruleVolume, func(s, _ map[string]any) {
			svc(s, filterDeploy)["volumes"] = []any{socketMount, "/:/host:ro"}
		}},
		"a filter joins the factory network": {ruleNetwork, func(s, _ map[string]any) {
			svc(s, filterRunner)["networks"] = []any{dockerNetwork, "factory"}
		}},
		"the dispatcher joins the docker network": {ruleNetwork, func(s, _ map[string]any) {
			svc(s, "dispatcher")["networks"] = []any{"factory", dockerNetwork}
		}},
		"the app joins the docker network": {ruleNetwork, func(s, _ map[string]any) {
			svc(s, "app")["networks"] = []any{"app", dockerNetwork}
		}},
		"the docker network is not internal": {ruleNetwork, func(_, root map[string]any) {
			root["networks"].(map[string]any)[dockerNetwork].(map[string]any)["internal"] = false
		}},
		"the runner filter serves the deploy role": {ruleSocket, func(s, _ map[string]any) {
			replaceArg(svc(s, filterRunner), "--role=runner", "--role=deploy")
		}},
		"the runner filter creates on the app network": {ruleSocket, func(s, _ map[string]any) {
			replaceArg(svc(s, filterRunner), "--network="+f.Name+"-sandbox", "--network="+f.Name+"-app")
		}},
		"the deploy filter deploys on the control network": {ruleSocket, func(s, _ map[string]any) {
			replaceArg(svc(s, filterDeploy), "--network="+f.Name+"-app", "--network="+f.Name+"-control")
		}},
		"a filter is pointed at another socket": {ruleSocket, func(s, _ map[string]any) {
			replaceArg(svc(s, filterDeploy), "", "--socket=/run/containerd/containerd.sock")
		}},
		"the runner filter is given simulated images": {ruleSocket, func(s, _ map[string]any) {
			replaceArg(svc(s, filterRunner), "", "--sim-image=app=alpine")
		}},
		"the dispatcher is added to the socket's group": {ruleSocket, func(s, _ map[string]any) {
			svc(s, "dispatcher")["group_add"] = []any{"0"}
		}},
		"a filter is added to a group by name": {ruleSocket, func(s, _ map[string]any) {
			svc(s, filterRunner)["group_add"] = []any{"docker"}
		}},
		"a filter is added to two groups": {ruleSocket, func(s, _ map[string]any) {
			svc(s, filterRunner)["group_add"] = []any{"0", "1"}
		}},
		"the supervisor is pointed at the raw Docker API": {ruleSocket, func(s, _ map[string]any) {
			svc(s, "supervisor")["environment"].(map[string]any)["DOCKER_HOST"] = "unix:///var/run/docker.sock"
		}},
		"the edge admin listens on every interface": {ruleReach, func(s, _ map[string]any) {
			replaceArg(svc(s, "edge"), "--admin-listen=edge-admin:7469", "--admin-listen=0.0.0.0:7469")
		}},
		"the egress proxy listens on every interface": {ruleReach, func(s, _ map[string]any) {
			replaceArg(svc(s, "edge"), "--egress-listen=egress:7466", "--egress-listen=0.0.0.0:7466")
		}},
		"the edge admin listener is dropped to its default": {ruleReach, func(s, _ map[string]any) {
			replaceArg(svc(s, "edge"), "--admin-listen=edge-admin:7469", "--revision=x")
		}},
		"the edge admin is published": {rulePort, func(s, _ map[string]any) {
			svc(s, "edge")["ports"] = []any{"127.0.0.1:7460:7460", "127.0.0.1:7469:7469"}
		}},
		"the edge admin alias moves to the app network": {ruleReach, func(s, _ map[string]any) {
			svc(s, "edge")["networks"] = map[string]any{
				"app": map[string]any{"aliases": []any{"edge-admin"}}, "control": map[string]any{}, "sandbox": map[string]any{"aliases": []any{"egress"}},
			}
		}},
		"the dispatcher takes the edge admin alias": {ruleReach, func(s, _ map[string]any) {
			svc(s, "dispatcher")["networks"] = map[string]any{"factory": map[string]any{"aliases": []any{"edge-admin"}}}
		}},
		"the dispatcher reads the edge's env file": {ruleSecretMount, func(s, _ map[string]any) {
			svc(s, "dispatcher")["env_file"] = []any{in.Layout.RuntimeEnv(areaAgent), in.Layout.RuntimeEnv(areaEdge)}
		}},
		"the dispatcher reads the supervisor's env file": {ruleSecretMount, func(s, _ map[string]any) {
			svc(s, "dispatcher")["env_file"] = []any{in.Layout.Secret(areaControl, "supervisor.env")}
		}},
		"the app reads the trust step's env file": {ruleSecretMount, func(s, _ map[string]any) {
			svc(s, "app")["env_file"] = []any{in.Layout.Secret(areaControl, trustEnvFile)}
		}},
		"the supervisor joins the sandbox": {ruleNetwork, func(s, _ map[string]any) {
			svc(s, "supervisor")["networks"] = []any{"control", "factory", "app", dockerNetwork, "sandbox"}
		}},
		"the relay moves into control's namespace": {ruleNetwork, func(s, _ map[string]any) {
			svc(s, "sim-github-relay")["network_mode"] = "service:control"
		}},
		"the trust step gets a network": {ruleNetwork, func(s, _ map[string]any) {
			delete(svc(s, "trust-bootstrap"), "network_mode")
			svc(s, "trust-bootstrap")["networks"] = []any{"control"}
		}},
		"the dispatcher mounts the trust volume": {ruleVolume, func(s, _ map[string]any) {
			svc(s, "dispatcher")["volumes"] = []any{"trust:/trust"}
		}},
		"the dispatcher mounts the agent's directory writable": {ruleVolume, func(s, _ map[string]any) {
			svc(s, "dispatcher")["volumes"] = []any{in.Layout.Config() + ":/etc/sisf/factory.yaml:ro", in.Layout.SimAgentDir() + ":/etc/sisf/agent"}
		}},
		"the app mounts the agent's directory": {ruleVolume, func(s, _ map[string]any) {
			svc(s, "app")["volumes"] = []any{in.Layout.SimAgentDir() + ":/agent:ro"}
		}},
		"the app carries a runner label": {ruleReach, func(s, _ map[string]any) {
			svc(s, "app")["labels"].(map[string]any)["sisf.runner"] = f.Name
		}},
		"the app is labelled with another release": {ruleReach, func(s, _ map[string]any) {
			svc(s, "app")["labels"].(map[string]any)["sisf.digest"] = "sha256:" + strings.Repeat("f", 64)
		}},
		"the app reports another revision than its label": {ruleReach, func(s, _ map[string]any) {
			svc(s, "app")["environment"].(map[string]any)["SISF_REVISION"] = "sha256:" + strings.Repeat("f", 64)
		}},
		"control carries release labels": {ruleReach, func(s, _ map[string]any) {
			svc(s, "control")["labels"] = map[string]any{"sisf.factory": f.Name, "sisf.unit": "app", "sisf.digest": digestLabel}
		}},
	}
	for name, tc := range cases {
		vs, err := CheckCompose(in.Layout, edited(t, out, tc.edit))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if !slices.ContainsFunc(vs, func(v Violation) bool { return v.Rule == tc.rule }) {
			t.Errorf("%s: want a %s violation, got [%s]", name, tc.rule, violationRules(vs))
		}
	}
	// A network attachment with a setting other than aliases, such as a
	// fixed address, makes the file unreadable.
	fixed := edited(t, out, func(s, _ map[string]any) {
		svc(s, "edge")["networks"].(map[string]any)["control"] = map[string]any{"ipv4_address": "172.30.0.9"}
	})
	if _, err := CheckCompose(in.Layout, fixed); err == nil {
		t.Error("a fixed address on a network was accepted")
	}
}

func TestTheReleaseTopologyIsWhatTheSupervisorNeeds(t *testing.T) {
	t.Parallel()
	_, out := renderCompose(t, nil)
	f := composeFileOf(t, out)
	if !f.Networks[dockerNetwork].Internal {
		t.Error("the docker network is not internal")
	}
	for name, s := range f.Services {
		if slices.Contains(s.Networks, dockerNetwork) && name != "supervisor" && name != filterRunner && name != filterDeploy {
			t.Errorf("%s is on the docker network", name)
		}
	}
	text := string(out)
	for _, want := range []string{
		"--deploy=compose", "--exec=docker", "--qa-judge=fake", "--release-window=10s", "--edge-admin-url=http://edge-admin:7469",
		"--exec-docker-endpoint=http://docker-filter-runner:7468", "--deploy-docker-endpoint=http://docker-filter-deploy:7468",
		"--dispatcher-admin-url=http://dispatcher:7466", "--admin-listen=0.0.0.0:7466", "--route=/report=http://control:7463",
		`SISF_TRUSTED_PROXIES: "172.30.0.0/16"`, "--sim-image=app=",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the project lacks %s", want)
		}
	}
	app := f.Services["app"]
	if app.Labels["sisf.unit"] != "app" || app.ContainerName != deployedContainer("sisf-hello-app", "app", core.Digest(fakeID)) {
		t.Errorf("the app container is not named and labelled as the deployment backend's: %s %v", app.ContainerName, app.Labels)
	}
}

// A production Docker install runs the stages only with a sandboxing
// launcher for its QA agent and a command to build candidates with.
func TestAProductionDockerInstallRunsTheStagesOnlyWithAQALauncherAndABuildCommand(t *testing.T) {
	t.Parallel()
	real := production
	_, plain := renderCompose(t, real)
	if strings.Contains(string(plain), "--factory-manifest") || strings.Contains(string(plain), "--qa-judge") {
		t.Error("the stages run without a QA launcher")
	}
	_, unbuildable := renderCompose(t, func(o *Options) { real(o); o.QAAgent, o.QAAgentBinary = "claude", "/opt/sisf/qa-launcher" })
	if strings.Contains(string(unbuildable), "--factory-manifest") {
		t.Error("the stages run without a build command")
	}
	_, launched := renderCompose(t, func(o *Options) {
		real(o)
		o.QAAgent, o.QAAgentBinary, o.BuildCommand = "claude", "/opt/sisf/qa-launcher", "/opt/sisf/build-candidate --push"
	})
	for _, want := range []string{"--qa-judge=agent", "--qa-agent=claude", "--qa-agent-binary=/opt/sisf/qa-launcher", "--factory-manifest",
		`"--build-command=/opt/sisf/build-candidate --push"`} {
		if !strings.Contains(string(launched), want) {
			t.Errorf("the production project lacks %s", want)
		}
	}
	if strings.Contains(string(launched), "--release-window") || strings.Contains(string(launched), "--sim-image") {
		t.Error("a production project carries the simulation's settings")
	}
}
