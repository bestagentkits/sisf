package wiring

import (
	"os"
	"path/filepath"

	"github.com/bestagentkits/sisf/cmd/supervisor/runcontrol"
	"github.com/bestagentkits/sisf/pkg/agent/agentenv"
	kit "github.com/bestagentkits/sisf/pkg/conformance"
	"github.com/bestagentkits/sisf/pkg/core"
)

// agentHomeDir is the real agent's private HOME, below the workspace root.
const agentHomeDir = ".agent-home"

// childAgentAllowed decides whether a real coding agent may run as a child
// of the dispatcher. Even with an environment of its own, a child of the
// same OS user can read the dispatcher's /proc/<pid>/environ, which holds
// the state store's credentials. So the start is refused unless one of
// these holds:
//
//   - the operator declared, with --sandboxed, that the dispatcher runs in
//     a sandbox, and the manifest's execution backend is one declared to
//     isolate runs and enforce egress;
//   - --agent-binary names an absolute launcher that runs the agent in a
//     sandbox of its own;
//   - the manifest states trusted_code_only, the declared trust model of a
//     Standalone install on the operator's own machine, which `sisf doctor`
//     warns about.
func childAgentAllowed(cfg AgentZoneConfig) error {
	if cfg.Binary != "" && !filepath.IsAbs(cfg.Binary) {
		return core.NewError(core.CodeInvalidArgument, "--agent-binary must be an absolute path")
	}
	switch {
	case cfg.Binary != "":
		return nil
	case cfg.Manifest != nil && cfg.Manifest.TrustedCodeOnly:
		return nil
	case cfg.Sandboxed && cfg.Manifest != nil && executionIsolates(cfg.Manifest.Adapters.Execution.Name):
		return nil
	}
	return core.NewError(core.CodePermissionDenied,
		"a real coding agent would run as the dispatcher's own OS user and could read its environment: "+
			"run the dispatcher --sandboxed with an isolating execution backend, give --agent-binary a sandboxing launcher, "+
			"or state trusted_code_only in the manifest")
}

// executionIsolates reports whether the named execution backend is
// declared, in the capability matrix, to isolate runs and enforce the
// egress allowlist.
func executionIsolates(name string) bool {
	d, ok := kit.Find(kit.Builtin(), kit.KindExecution, name)
	return ok && d.Capabilities.Level(core.CapExecIsolation) != core.SupportNotSupported &&
		d.Capabilities.Level(core.CapExecEgressEnforcement) != core.SupportNotSupported
}

// agentEnvironment is the real agent's whole environment: a PATH, a
// private HOME and TMPDIR below the workspace root, the credential
// variables of the chosen agent and the names --agent-env adds. None of
// the dispatcher's own variables is ever among them. With --agent-home the
// HOME is the operator's own instead, with the operator's USER so the
// agent finds the login kept under that account; TMPDIR stays private.
func agentEnvironment(cfg AgentZoneConfig) ([]string, error) {
	lookup := cfg.LookupEnv
	if lookup == nil {
		lookup = func(string) (string, bool) { return "", false }
	}
	home := filepath.Join(cfg.WorkspaceRoot, agentHomeDir)
	tmp := filepath.Join(home, "tmp")
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		return nil, core.NewError(core.CodeUnavailable, "the agent's home directory cannot be made").WithCause(err)
	}
	if cfg.Home != "" {
		if err := operatorHomeAllowed(cfg); err != nil {
			return nil, err
		}
		home = cfg.Home
	}
	return agentenv.Build(agentenv.Spec{
		Agent: cfg.Agent, Home: home, OperatorHome: cfg.Home != "", TempDir: tmp, Extra: cfg.AgentEnv, LookupEnv: lookup,
		Forbidden: []string{EnvStateDSN, runcontrol.EnvToken, EnvWebhookSecret, EnvGitHubToken, EnvApprovalKey},
	})
}

// operatorHomeAllowed decides whether the agent may run with the
// operator's own HOME. Only under trusted_code_only, where the agent
// already runs as the operator's user on the operator's machine: a
// sandbox or a launcher owns the home of the agent it runs, and handing it
// the operator's would carry the operator's files into it.
func operatorHomeAllowed(cfg AgentZoneConfig) error {
	if cfg.Manifest == nil || !cfg.Manifest.TrustedCodeOnly || cfg.Sandboxed || cfg.Binary != "" {
		return core.NewError(core.CodePermissionDenied,
			"--agent-home is accepted only under trusted_code_only, without --sandboxed or --agent-binary, whose sandbox owns the agent's home")
	}
	return agentenv.CheckOperatorHome(cfg.Home, cfg.ProtectedDirs...)
}
