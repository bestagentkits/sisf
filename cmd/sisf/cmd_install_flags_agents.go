package main

import (
	"flag"

	"github.com/bestagentkits/sisf/internal/install"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// agentFlags choose the real agents of a Standalone install and how
// candidates are built.
type agentFlags struct {
	coderAgent, coderModel        string
	qaJudge, qaAgent, qaModel     string
	agentHome, qaHome             string
	workspaceSource, buildCommand string
	exec                          string
	qaAgentBinary                 string
	sandbox                       install.BuildSandbox
}

func (f *agentFlags) bind(fs *flag.FlagSet) {
	fs.StringVar(&f.coderAgent, "coder-agent", "", "coding agent: fake (default), claude or codex; a real one runs only in Standalone mode under trusted_code_only")
	fs.StringVar(&f.coderModel, "coder-model", "", "model the coding agent runs in place of the coding route's tier name")
	fs.StringVar(&f.qaJudge, "qa-judge", "", "judge of candidates: agent (default outside the simulation); the simulation uses its scripted judge")
	fs.StringVar(&f.qaAgent, "qa-agent", "", "QA agent of the agent judge: claude, codex (default) or pi")
	fs.StringVar(&f.qaModel, "qa-model", "", "model the QA agent runs in place of the QA route's tier name; never the coder's")
	fs.StringVar(&f.agentHome, "agent-home", "", "absolute HOME, such as your own, the coding agent runs with to find its login; Standalone only, outside the install directory")
	fs.StringVar(&f.qaHome, "qa-home", "", "absolute HOME, such as your own, the QA agent runs with to find its login; Standalone only, outside the install directory")
	fs.StringVar(&f.workspaceSource, "workspace-source", "", "repository URL or absolute path, without credentials, the coding agent's workspaces are checked out from (default https://github.com/<repo>.git)")
	fs.StringVar(&f.buildCommand, "build-command", "", "command, split on spaces, that builds a candidate")
	fs.StringVar(&f.exec, "exec", "", "execution backend of candidate builds: local (Standalone default, which builds nothing an agent wrote) or docker (Docker mode; Standalone with the build sandbox flags)")
	fs.StringVar(&f.qaAgentBinary, "qa-agent-binary", "", "absolute path of a launcher that runs the QA agent in a sandbox and keeps its standard input and output; needed by Docker mode and by a Standalone build sandbox")
	fs.StringVar(&f.sandbox.Endpoint, "exec-docker-endpoint", "", "Standalone --exec docker: http URL, on loopback, of the runner Docker request filter (sisf docker-filter --role runner)")
	fs.StringVar(&f.sandbox.Network, "exec-docker-network", "", "Standalone --exec docker: the internal sandbox network candidate builds run on")
	fs.StringVar(&f.sandbox.EgressProxy, "egress-proxy", "", "Standalone --exec docker: http URL of the egress proxy on the sandbox network, its only way out")
	fs.StringVar(&f.sandbox.RunnerImage, "runner-image", "", "Standalone --exec docker: image candidate builds run in, pinned as name@sha256:<digest> or by its full local image ID sha256:<64 hex>, which the filter allows")
}

// apply sets the agent options, checking the choices only the command line
// knows of: the judge and the execution backend follow from the profile
// and the mode.
func (f agentFlags) apply(o *install.Options) error {
	switch {
	case f.qaJudge != "" && f.qaJudge != "agent" && (!o.Sim || f.qaJudge != "fake"):
		return usagef("--qa-judge must be agent; the scripted judge is the simulation's own")
	case f.qaJudge == "agent" && o.Sim:
		return usagef("the simulation judges with its scripted judge; do not pass --qa-judge agent")
	case f.exec != "" && f.exec != "local" && f.exec != "docker":
		return usagef("--exec must be local or docker")
	case o.Mode == manifest.ModeDocker && f.exec == "local":
		return usagef("--exec local does not fit docker mode, which builds with docker")
	case o.Mode == manifest.ModeDocker && f.sandbox != (install.BuildSandbox{}):
		return usagef("docker mode runs its own build sandbox; --exec-docker-endpoint, --exec-docker-network, --egress-proxy and --runner-image are for Standalone")
	case o.Mode == manifest.ModeStandalone && f.exec == "docker" && f.sandbox == (install.BuildSandbox{}):
		return usagef("--exec docker in Standalone mode needs the build sandbox: --exec-docker-endpoint, --exec-docker-network, --egress-proxy and --runner-image")
	case o.Mode == manifest.ModeStandalone && f.exec != "docker" && f.sandbox != (install.BuildSandbox{}):
		return usagef("the build sandbox flags need --exec docker")
	}
	o.QAAgent, o.QAAgentBinary, o.BuildCommand, o.BuildSandbox = f.qaAgent, f.qaAgentBinary, f.buildCommand, f.sandbox
	o.Agents = install.AgentOptions{Coder: f.coderAgent, CoderModel: f.coderModel, QAModel: f.qaModel,
		AgentHome: f.agentHome, QAHome: f.qaHome, WorkspaceSource: f.workspaceSource}
	return nil
}
