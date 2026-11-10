package install

import (
	"net/url"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bestagentkits/sisf/pkg/agent/cliagent"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// GitHubCredentialGHCLI takes the GitHub token from the local gh login.
const GitHubCredentialGHCLI = "gh-cli"

// Extension keys of the agent settings in factory.yaml.
const (
	extCoderAgent       = "coder_agent"
	extCoderModel       = "coder_model"
	extQAModel          = "qa_model"
	extAgentHome        = "agent_home"
	extQAHome           = "qa_home"
	extWorkspaceSource  = "workspace_source"
	extGitHubCredential = "github_auth_via"
)

// coderAgents are the real coding agents a Standalone install runs as
// local processes under trusted_code_only. Pi has no permission system of
// its own and runs only in a sandbox, which a Standalone install has not.
var coderAgents = []string{"claude", "codex"}

// AgentOptions configure the real coding and QA agents of a Standalone
// install. The zero value runs the scripted agent, as before.
type AgentOptions struct {
	// Coder is the coding agent: "" or "fake" for the scripted one, or one
	// of coderAgents.
	Coder string
	// CoderModel and QAModel replace the model names of the coding and QA
	// routes, whose tier names the real tools do not accept.
	CoderModel, QAModel string
	// AgentHome and QAHome are the absolute HOME, such as the operator's
	// own, the coding and the QA agent run with to find their login.
	AgentHome, QAHome string
	// WorkspaceSource is where the coding agent's workspaces are checked
	// out from: a repository URL or path without credentials. It defaults
	// to the repository on github.com.
	WorkspaceSource string
}

// real reports whether a real coding agent runs.
func (a AgentOptions) real() bool { return a.Coder != "" && a.Coder != "fake" }

// normalize checks the agent settings against the install's mode and trust
// model and fills the workspace source.
func (a AgentOptions) normalize(o Options) (AgentOptions, error) {
	if !a.real() {
		if a.CoderModel != "" || a.AgentHome != "" || a.WorkspaceSource != "" {
			return a, invalidf("--coder-model, --agent-home and --workspace-source are for a real coding agent (--coder-agent)")
		}
	} else {
		switch {
		case !slices.Contains(coderAgents, a.Coder):
			return a, invalidf("the coding agent must be fake or one of %s", strings.Join(coderAgents, ", "))
		case o.Sim:
			return a, invalidf("the simulation runs the scripted agent only")
		case o.Mode != manifest.ModeStandalone:
			return a, invalidf("a real coding agent runs as a local process of a Standalone install under trusted_code_only")
		}
		if a.WorkspaceSource == "" {
			a.WorkspaceSource = "https://github.com/" + o.Repo.Owner + "/" + o.Repo.Repo + ".git"
		}
		if err := checkWorkspaceSource(a.WorkspaceSource); err != nil {
			return a, err
		}
	}
	if (a.QAModel != "" || a.QAHome != "") && o.Sim {
		return a, invalidf("the simulation judges with the scripted judge; --qa-model and --qa-home are for the agent judge")
	}
	for _, m := range []string{a.CoderModel, a.QAModel} {
		if m == "" {
			continue
		}
		if err := cliagent.ValidateModel(m); err != nil || !lifecycleValue.MatchString(m) {
			return a, invalidf("the model name %q is not one a command line here can carry", m)
		}
	}
	for flag, home := range map[string]string{"--agent-home": a.AgentHome, "--qa-home": a.QAHome} {
		if home == "" {
			continue
		}
		if o.Mode != manifest.ModeStandalone {
			return a, invalidf("%s is allowed only in a Standalone install, which runs under trusted_code_only", flag)
		}
		if !filepath.IsAbs(home) || !pathPattern.MatchString(home) {
			return a, invalidf("%s must be an absolute path of letters, digits and . _ + - /", flag)
		}
		if within(o.Dir, filepath.Clean(home)) || within(filepath.Clean(home), o.Dir) {
			return a, invalidf("the install directory must lie outside %s and %s outside it, so the agent cannot read the factory's secrets", flag, flag)
		}
	}
	return a, nil
}

// within reports whether path is dir or below it.
func within(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}

// checkWorkspaceSource accepts an absolute path or an https URL that
// carries no credential.
func checkWorkspaceSource(raw string) error {
	if filepath.IsAbs(raw) {
		if !pathPattern.MatchString(raw) {
			return invalidf("the workspace source path has characters a command line here cannot carry")
		}
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !pathPattern.MatchString(u.Path) {
		return invalidf("the workspace source must be an absolute path or an https URL without credentials")
	}
	return nil
}

// checkDev keeps the development profile and the gh credential to a
// Standalone install on loopback.
func (o Options) checkDev() error {
	switch {
	case o.GitHubCredential != "" && o.GitHubCredential != GitHubCredentialGHCLI:
		return invalidf("the GitHub credential source must be %s", GitHubCredentialGHCLI)
	case o.GitHubCredential != "" && !o.Dev:
		return invalidf("--github-credential %s is for a development install (--dev) only", GitHubCredentialGHCLI)
	case o.GitHubCredential != "" && !o.GitHubToken.IsZero():
		return invalidf("give the GitHub token one way: --github-credential or --github-token-env")
	case !o.Dev:
		return nil
	case o.Sim:
		return invalidf("--dev and the simulation profile are different profiles; choose one")
	case o.Mode != manifest.ModeStandalone:
		return invalidf("--dev runs a Standalone install only, where every listen address is loopback")
	}
	return nil
}

// agentExtensions records the agent settings in the manifest.
func (o Options) agentExtensions(ext map[string]any) {
	set := func(key, value string) {
		if value != "" {
			ext[key] = value
		}
	}
	a := o.Agents
	if a.real() {
		set(extCoderAgent, a.Coder)
	}
	set(extCoderModel, a.CoderModel)
	set(extQAModel, a.QAModel)
	set(extAgentHome, a.AgentHome)
	set(extQAHome, a.QAHome)
	set(extWorkspaceSource, a.WorkspaceSource)
	set("qa_agent", o.QAAgent)
	set("qa_agent_binary", o.QAAgentBinary)
	set("build_command", o.BuildCommand)
	set(extGitHubCredential, o.GitHubCredential)
	o.BuildSandbox.extensions(ext)
	if o.Dev {
		ext[extDev] = true
	}
}

// agentsOf reads the agent settings of an install.
func agentsOf(m *manifest.Manifest) AgentOptions {
	get := func(key string) string { v, _ := m.Extensions[key].(string); return v }
	return AgentOptions{Coder: get(extCoderAgent), CoderModel: get(extCoderModel), QAModel: get(extQAModel),
		AgentHome: get(extAgentHome), QAHome: get(extQAHome), WorkspaceSource: get(extWorkspaceSource)}
}
