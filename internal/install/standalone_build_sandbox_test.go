package install

import (
	"slices"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// sandboxed turns options into those of a production Standalone install
// with real agents that builds in a Docker sandbox.
func sandboxed(t *testing.T, o *Options) {
	t.Helper()
	production(o)
	o.Agents = AgentOptions{Coder: "claude", AgentHome: t.TempDir()}
	o.QAAgent, o.QAAgentBinary, o.BuildCommand = "codex", "/opt/sisf/qa-codex-launcher", "/usr/local/bin/sisf-build-candidate"
	o.BuildSandbox = BuildSandbox{Endpoint: "http://127.0.0.1:17480", Network: "sisf-shop-sandbox",
		EgressProxy: "http://egress:3128", RunnerImage: "127.0.0.1:17481/sisf-runner@sha256:" + strings.Repeat("ab", 32)}
}

func TestAStandaloneInstallBuildsInItsDockerSandboxWithTheQAAgentLaunched(t *testing.T) {
	t.Parallel()
	in, _, _ := newTestInstaller(t)
	o := initOptions(in, manifest.ModeStandalone)
	sandboxed(t, &o)
	o.Agents.QAModel = "gpt-5-codex"
	if _, err := in.Init(t.Context(), o); err != nil {
		t.Fatal(err)
	}
	m, err := in.Layout.LoadManifest()
	must(t, err)
	if !m.TrustedCodeOnly || m.Adapters.Execution.Name != "local" {
		t.Errorf("the agents no longer run as local processes under trusted_code_only: %+v %v", m.Adapters.Execution, m.TrustedCodeOnly)
	}
	args, err := in.StandaloneSupervisorArgs(m)
	must(t, err)
	for _, want := range []string{
		"--exec=docker", "--exec-docker-endpoint=http://127.0.0.1:17480", "--exec-docker-network=sisf-shop-sandbox",
		"--egress-proxy=http://egress:3128", "--runner-image=" + o.BuildSandbox.RunnerImage,
		"--build-command=/usr/local/bin/sisf-build-candidate", "--qa-judge=agent", "--qa-agent=codex",
		"--qa-agent-binary=/opt/sisf/qa-codex-launcher", "--qa-model=gpt-5-codex", "--coder-agent=claude",
	} {
		if !slices.Contains(args, want) {
			t.Errorf("the supervisor lacks %s: %v", want, args)
		}
	}
	if slices.Contains(args, "--exec=local") {
		t.Errorf("the supervisor is given two execution backends: %v", args)
	}
}

func TestAStandaloneInstallWithoutASandboxKeepsTheLocalBackend(t *testing.T) {
	t.Parallel()
	in, _, _ := newTestInstaller(t)
	o := initOptions(in, manifest.ModeStandalone)
	production(&o)
	o.Agents = AgentOptions{Coder: "claude", QAHome: t.TempDir()}
	if _, err := in.Init(t.Context(), o); err != nil {
		t.Fatal(err)
	}
	m, err := in.Layout.LoadManifest()
	must(t, err)
	args, err := in.StandaloneSupervisorArgs(m)
	must(t, err)
	if !slices.Contains(args, "--exec=local") || !slices.Contains(args, "--qa-home="+o.Agents.QAHome) ||
		slices.ContainsFunc(args, func(a string) bool { return strings.HasPrefix(a, "--exec-docker") }) {
		t.Errorf("the supervisor = %v, want the local backend as before", args)
	}
}

func TestInitRefusesAnIncompleteOrMisplacedBuildSandbox(t *testing.T) {
	t.Parallel()
	for name, mutate := range map[string]func(*Options){
		"no endpoint":      func(o *Options) { o.BuildSandbox.Endpoint = "" },
		"no network":       func(o *Options) { o.BuildSandbox.Network = "" },
		"no egress proxy":  func(o *Options) { o.BuildSandbox.EgressProxy = "" },
		"no runner image":  func(o *Options) { o.BuildSandbox.RunnerImage = "" },
		"no build command": func(o *Options) { o.BuildCommand = "" },
		"no QA launcher":   func(o *Options) { o.QAAgentBinary = "" },
		"the QA home":      func(o *Options) { o.Agents.QAHome = "/srv/operator" },
		"a filter off loopback": func(o *Options) {
			o.BuildSandbox.Endpoint = "http://10.0.0.5:2375"
		},
		"a filter by name":        func(o *Options) { o.BuildSandbox.Endpoint = "http://docker-filter:2375" },
		"a filter over https":     func(o *Options) { o.BuildSandbox.Endpoint = "https://127.0.0.1:2375" },
		"a filter without a port": func(o *Options) { o.BuildSandbox.Endpoint = "http://127.0.0.1" },
		"a network with a slash":  func(o *Options) { o.BuildSandbox.Network = "sandbox/x" },
		"a proxy with a path":     func(o *Options) { o.BuildSandbox.EgressProxy = "http://egress:3128/x" },
		"a proxy with a user":     func(o *Options) { o.BuildSandbox.EgressProxy = "http://u:p@egress:3128" },
		"an image with a space":   func(o *Options) { o.BuildSandbox.RunnerImage = "runner latest" },
		"a local image ID":        func(o *Options) { o.BuildSandbox.RunnerImage = "sha256:" + strings.Repeat("ab", 32) },
		"an image by tag":         func(o *Options) { o.BuildSandbox.RunnerImage = "sisf-runner:latest" },
		"Docker mode":             func(o *Options) { o.Mode = manifest.ModeDocker; o.Agents = AgentOptions{} },
		"the simulation": func(o *Options) {
			o.Sim, o.Identity, o.Agents = true, Identity{}, AgentOptions{}
		},
	} {
		err := initErr(t, manifest.ModeStandalone, func(o *Options) { sandboxed(t, o); mutate(o) })
		if !core.HasCode(err, core.CodeInvalidArgument) {
			t.Errorf("%s: Init = %v, want INVALID_ARGUMENT", name, err)
		}
	}
	// A manifest edited by hand is checked again when the units start.
	if err := (LifecycleChoice{BuildSandbox: BuildSandbox{Endpoint: "http://127.0.0.1:1"}}).check(); err == nil {
		t.Error("an incomplete sandbox in the manifest was accepted")
	}
}

// The doctor reads a Standalone install's build sandbox network through its
// runner filter, as the docker backend does, and fails a network that is
// not internal, missing or not reported.
func TestTheDoctorFailsABuildSandboxNetworkThatIsNotInternal(t *testing.T) {
	t.Parallel()
	const route = "127.0.0.1:17480/v1.44/networks/sisf-shop-sandbox"
	for name, tc := range map[string]struct {
		answer *reply
		want   Verdict
	}{
		"internal":              {&reply{code: 200, body: `{"Name":"sisf-shop-sandbox","Internal":true}`}, VerdictOK},
		"an ordinary bridge":    {&reply{code: 200, body: `{"Name":"sisf-shop-sandbox","Internal":false}`}, VerdictFail},
		"missing":               {&reply{code: 404, body: `{"message":"no such network"}`}, VerdictFail},
		"another network":       {&reply{code: 200, body: `{"Name":"bridge","Internal":true}`}, VerdictFail},
		"a filter that is down": {nil, VerdictFail},
	} {
		net := healthyNet()
		if tc.answer != nil {
			net = net.with(route, *tc.answer)
		}
		in := builtStandalone(t, net, func(o *Options) { sandboxed(t, o) })
		r := runDoctor(t, in)
		if v := verdictOf(r, sandboxNetworkCheck); len(v) != 1 || v[0] != tc.want {
			t.Errorf("%s: %s = %v, want %s (%s)", name, sandboxNetworkCheck, v, tc.want, detailOf(r, sandboxNetworkCheck))
		}
	}
	// Without a build sandbox there is no sandbox network to check.
	r := runDoctor(t, builtStandalone(t, healthyNet(), nil))
	if v := verdictOf(r, sandboxNetworkCheck); len(v) != 0 {
		t.Errorf("a Standalone install without a build sandbox: %s = %v", sandboxNetworkCheck, v)
	}
}
