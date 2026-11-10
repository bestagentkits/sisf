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
	fs.StringVar(&f.exec, "exec", "", "execution backend of candidate builds, fixed by the mode: local (Standalone, under trusted_code_only) or docker")
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
	case f.exec != "" && f.exec != execFor(o.Mode):
		return usagef("--exec %s does not fit %s mode, which builds with %s", f.exec, o.Mode, execFor(o.Mode))
	}
	o.QAAgent, o.BuildCommand = f.qaAgent, f.buildCommand
	o.Agents = install.AgentOptions{Coder: f.coderAgent, CoderModel: f.coderModel, QAModel: f.qaModel,
		AgentHome: f.agentHome, QAHome: f.qaHome, WorkspaceSource: f.workspaceSource}
	return nil
}

// execFor is the execution backend a mode builds candidates with.
func execFor(mode manifest.Mode) string {
	if mode == manifest.ModeDocker {
		return "docker"
	}
	return "local"
}
