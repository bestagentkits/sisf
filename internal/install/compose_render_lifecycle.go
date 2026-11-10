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
// agent. Without one the stages are off and the doctor says why.
type LifecycleChoice struct {
	Enabled bool
	// Off says why the stages are off; empty when they run.
	Off        string
	CoderAgent string
	// QAJudge is fake in the simulation and agent otherwise. QAAgent and
	// QAAgentBinary configure the agent judge.
	QAJudge, QAAgent, QAAgentBinary string
	// AgentWork, ReleaseWindow and ReleaseMinSamples are the simulation's
	// short settings; empty or zero elsewhere.
	AgentWork, ReleaseWindow string
	ReleaseMinSamples        int
	Interval                 string
}

// lifecycleChoice decides the stages of an install.
func lifecycleChoice(o Options, mode manifest.Mode) LifecycleChoice {
	c := LifecycleChoice{Enabled: true, CoderAgent: "fake", Interval: lifecycleInterval}
	if o.Sim {
		c.QAJudge, c.AgentWork, c.ReleaseWindow, c.ReleaseMinSamples = "fake", simAgentWork, simReleaseWindow, simReleaseMinSamples
		return c
	}
	c.QAJudge, c.QAAgent, c.QAAgentBinary = "agent", o.QAAgent, o.QAAgentBinary
	if c.QAAgent == "" {
		c.QAAgent = defaultQAAgent
	}
	if mode == manifest.ModeDocker && c.QAAgentBinary == "" {
		c.Enabled = false
		c.Off = "no sandboxing launcher for the QA agent is configured (qa_agent_binary), so the supervisor builds, evaluates and releases nothing"
	}
	return c
}

// lifecycleValue is what a setting written into a command line may hold.
var lifecycleValue = regexp.MustCompile(`^[A-Za-z0-9._-]*$`)

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
