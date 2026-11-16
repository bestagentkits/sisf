package install

import (
	"net/netip"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// Ports inside the deployment that are never published on the host.
const (
	// PortEgress is the edge's egress proxy, the runners' only way out.
	PortEgress = 7466
	// PortDispatcherAdmin is the dispatcher's run control route in Docker
	// mode, on the factory network; Standalone binds it on loopback.
	PortDispatcherAdmin = 7466
	// PortDockerFilter is where each Docker request filter listens, on the
	// supervisor's private docker network.
	PortDockerFilter = 7468
	// PortEdgeAdmin is the edge's admin listener: the traffic split and the
	// counters, on the control network only.
	PortEdgeAdmin = 7469
)

// Settings of the simulation profile, which keep the end-to-end suite fast:
// a scripted agent run takes seconds, and a release window is the shortest
// the edge's ten-second counters can judge.
const (
	simAgentWork         = "20s"
	simReleaseWindow     = "10s"
	simReleaseMinSamples = 20
	lifecycleInterval    = "2s"
	// simPatchFile is the scripted agent's patch, below SimAgentDir.
	simPatchFile = "patch.diff"
)

// QA agents the agent judge can run.
var qaAgents = []string{"claude", "codex", "pi"}

// defaultQAAgent is the QA agent of a production install that names none.
const defaultQAAgent = "codex"

// LifecycleChoice is how the supervisor runs the stages after development.
// The simulation runs the scripted judge; a production install runs the
// agent judge, which in Docker mode needs a sandboxing launcher for the QA
// agent. Without one the stages are off and the doctor says why. Docker
// mode outside the simulation runs no stages at all: its builds would be
// accepted only on images the supervisor cannot observe.
type LifecycleChoice struct {
	Enabled bool
	// Off says why the stages are off, and OffCheck names the doctor check
	// that reports it; both empty when they run.
	Off, OffCheck string
	CoderAgent    string
	// QAJudge is fake in the simulation and agent otherwise. QAAgent and
	// QAAgentBinary configure the agent judge.
	QAJudge, QAAgent, QAAgentBinary string
	// BuildCommand is the build command of a candidate.
	BuildCommand string
	// AgentWork, ReleaseWindow and ReleaseMinSamples are the simulation's
	// short settings; empty or zero elsewhere.
	AgentWork, ReleaseWindow string
	ReleaseMinSamples        int
	Interval                 string
	// Agents are the real agents of a Standalone install.
	Agents AgentOptions
	// BuildSandbox is where a Standalone install builds candidates.
	BuildSandbox BuildSandbox
	// RunEstimateMicros is what each run sets aside from a capped budget;
	// zero when the budget is not capped.
	RunEstimateMicros int64
}

// lifecycleChoice decides the stages of an install.
func lifecycleChoice(o Options, mode manifest.Mode) LifecycleChoice {
	c := LifecycleChoice{Enabled: true, CoderAgent: "fake", Interval: lifecycleInterval, Agents: o.Agents, BuildSandbox: o.BuildSandbox}
	if o.Budget.Confirmed && o.Budget.Capped() {
		c.RunEstimateMicros = int64(o.Budget.withDefaults().RunEstimateMicros)
	}
	if o.Sim {
		c.QAJudge, c.AgentWork, c.ReleaseWindow, c.ReleaseMinSamples = "fake", simAgentWork, simReleaseWindow, simReleaseMinSamples
		return c
	}
	if o.Agents.real() {
		c.CoderAgent = o.Agents.Coder
	}
	c.QAJudge, c.QAAgent, c.QAAgentBinary, c.BuildCommand = "agent", o.QAAgent, o.QAAgentBinary, o.BuildCommand
	if c.QAAgent == "" {
		c.QAAgent = defaultQAAgent
	}
	if mode == manifest.ModeDocker {
		// The Compose backend runs images, and the supervisor can observe
		// no image a sandboxed build reports: it would start a supervisor
		// that refuses to build (cmd/supervisor/stages/image_builders.go).
		// No build command or launcher changes that, so init refuses both
		// in Docker mode; a manifest written before may still name them.
		c.Enabled, c.OffCheck = false, "build evidence"
		c.Off = "Docker mode deploys images, and the supervisor cannot observe an image a sandboxed build reports, " +
			"only a program it receives; it accepts no build on a report's word, so it builds, evaluates and releases nothing " +
			"(a Standalone install with a build sandbox deploys the program its build exports)"
	}
	return c
}

// lifecycleExtensions reads the lifecycle settings from the manifest's
// extensions: the QA agent, its launcher, the build command and the agents.
func (o *Options) lifecycleExtensions(m *manifest.Manifest) {
	o.QAAgent, _ = m.Extensions["qa_agent"].(string)
	o.QAAgentBinary, _ = m.Extensions["qa_agent_binary"].(string)
	o.BuildCommand, _ = m.Extensions["build_command"].(string)
	o.Agents = agentsOf(m)
	o.BuildSandbox = buildSandboxOf(m)
}

// lifecycleValue is what a setting written into a command line may hold.
var lifecycleValue = regexp.MustCompile(`^[A-Za-z0-9._-]*$`)

// buildCommandPattern is a build command: words of plain characters
// separated by single spaces, as the supervisor splits it. No quote, shell
// syntax or YAML indicator can be carried into the Compose file.
var buildCommandPattern = regexp.MustCompile(`^[A-Za-z0-9_./+=,@%-]+( [A-Za-z0-9_./+=,@%-]+){0,31}$`)

func (c LifecycleChoice) check() error {
	for _, v := range []string{c.CoderAgent, c.QAJudge, c.QAAgent, c.AgentWork, c.ReleaseWindow, c.Interval} {
		if !lifecycleValue.MatchString(v) {
			return invalidf("the lifecycle setting %q has characters a command line here cannot carry", v)
		}
	}
	if c.QAAgent != "" && !slices.Contains(qaAgents, c.QAAgent) {
		return invalidf("the QA agent must be one of %s", strings.Join(qaAgents, ", "))
	}
	if c.QAAgentBinary != "" && (!filepath.IsAbs(c.QAAgentBinary) || !pathPattern.MatchString(c.QAAgentBinary)) {
		return invalidf("the QA agent launcher must be an absolute path of letters, digits and . _ + - /")
	}
	if c.BuildCommand != "" && (len(c.BuildCommand) > 512 || !buildCommandPattern.MatchString(c.BuildCommand)) {
		return invalidf("the build command must be words of letters, digits and . _ / + = , @ %% - separated by single spaces")
	}
	a := c.Agents
	if a.real() && !slices.Contains(coderAgents, a.Coder) {
		return invalidf("the coding agent must be fake or one of %s", strings.Join(coderAgents, ", "))
	}
	for _, v := range []string{a.CoderModel, a.QAModel} {
		if !lifecycleValue.MatchString(v) {
			return invalidf("the model name %q has characters a command line here cannot carry", v)
		}
	}
	for _, home := range []string{a.AgentHome, a.QAHome} {
		if home != "" && (!filepath.IsAbs(home) || !pathPattern.MatchString(home)) {
			return invalidf("an agent home must be an absolute path of letters, digits and . _ + - /")
		}
	}
	if a.WorkspaceSource != "" {
		if err := checkWorkspaceSource(a.WorkspaceSource); err != nil {
			return err
		}
	}
	if c.BuildSandbox.set() {
		return c.BuildSandbox.check()
	}
	return nil
}

// HostFacts are what `sisf up` learns from the Docker host before it
// renders the project.
type HostFacts struct {
	// SocketGID is the group that owns the Docker socket, which the two
	// Docker request filters, and nothing else, are added to.
	SocketGID string
	// TrustedProxies is the control network, from which the edge forwards
	// the client address of a report; empty until the network exists.
	TrustedProxies string
}

var gidPattern = regexp.MustCompile(`^[0-9]{1,10}$`)

func (h HostFacts) check() error {
	if !gidPattern.MatchString(h.SocketGID) {
		return invalidf("the group of the Docker socket is not a number")
	}
	if h.TrustedProxies == "" {
		return nil
	}
	for raw := range strings.SplitSeq(h.TrustedProxies, ",") {
		if _, err := netip.ParsePrefix(raw); err != nil {
			return invalidf("the control network %q is not a network prefix", raw)
		}
	}
	return nil
}

// deployedContainer is the name the supervisor's Compose deployment backend
// gives the container of a unit's release: the factory, the unit and the
// first twelve hex digits of the digest.
func deployedContainer(factory core.FactoryID, unit string, digest core.Digest) string {
	hex := string(digest)
	if _, h, ok := strings.Cut(hex, ":"); ok {
		hex = h
	}
	if len(hex) > 12 {
		hex = hex[:12]
	}
	return string(factory) + "-" + unit + "-" + hex
}
