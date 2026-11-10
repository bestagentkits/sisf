package agentenv_test

import (
	"slices"
	"testing"

	"github.com/bestagentkits/sisf/pkg/agent/agentenv"
	"github.com/bestagentkits/sisf/pkg/core"
)

func lookup(vars map[string]string) func(string) (string, bool) {
	return func(n string) (string, bool) {
		v, ok := vars[n]
		return v, ok
	}
}

var parent = map[string]string{
	"PATH": "/opt/bin:/usr/bin", "ANTHROPIC_API_KEY": "sk-ant", "CLAUDE_CODE_OAUTH_TOKEN": "oauth", "OPENAI_API_KEY": "sk-oai",
	"SISF_STATE_DSN": "postgres://u:p@db", "SISF_DISPATCHER_ADMIN_TOKEN": "admin", "EDGE_ADMIN_TOKEN": "edge",
	"DOCKER_HOST": "unix:///var/run/docker.sock", "PROXY_CA": "/etc/ca.pem", "HOME": "/root",
}

func TestEachAgentGetsOnlyItsOwnCredentialsAPrivateHomeAndWhatTheOperatorNamed(t *testing.T) {
	t.Parallel()
	for agent, want := range map[string][]string{
		"claude": {"PATH=/opt/bin:/usr/bin", "HOME=/ws/.home", "TMPDIR=/ws/.home/tmp", "ANTHROPIC_API_KEY=sk-ant", "CLAUDE_CODE_OAUTH_TOKEN=oauth", "PROXY_CA=/etc/ca.pem"},
		"codex":  {"PATH=/opt/bin:/usr/bin", "HOME=/ws/.home", "TMPDIR=/ws/.home/tmp", "OPENAI_API_KEY=sk-oai", "PROXY_CA=/etc/ca.pem"},
		"pi":     {"PATH=/opt/bin:/usr/bin", "HOME=/ws/.home", "TMPDIR=/ws/.home/tmp", "PROXY_CA=/etc/ca.pem"},
	} {
		env, err := agentenv.Build(agentenv.Spec{Agent: agent, Home: "/ws/.home", Extra: []string{"PROXY_CA", "UNSET_VAR"}, LookupEnv: lookup(parent)})
		if err != nil || !slices.Equal(env, want) {
			t.Errorf("%s: %v, %v; want %v", agent, env, err, want)
		}
	}
	env, err := agentenv.Build(agentenv.Spec{Agent: "pi", Home: "/operator", TempDir: "/ws/.home/tmp", LookupEnv: lookup(parent)})
	if err != nil || !slices.Equal(env[:3], []string{"PATH=/opt/bin:/usr/bin", "HOME=/operator", "TMPDIR=/ws/.home/tmp"}) {
		t.Errorf("with the operator's home: %v, %v", env, err)
	}
	env, err = agentenv.Build(agentenv.Spec{Agent: "pi", Home: "/h", LookupEnv: lookup(nil)})
	if err != nil || env[0] != "PATH="+agentenv.DefaultPath {
		t.Errorf("with no PATH: %v, %v", env, err)
	}
}

func TestTheFactorysOwnVariablesAreAlwaysRefused(t *testing.T) {
	t.Parallel()
	for _, n := range []string{"SISF_STATE_DSN", "sisf_anything", "EDGE_ADMIN_TOKEN", "DOCKER_HOST", "HOME", "PATH", "TMPDIR", "MY_SECRET"} {
		_, err := agentenv.Build(agentenv.Spec{Agent: "codex", Home: "/h", Extra: []string{n}, Forbidden: []string{"MY_SECRET"}, LookupEnv: lookup(parent)})
		if !core.HasCode(err, core.CodePermissionDenied) {
			t.Errorf("passing %s: %v", n, err)
		}
	}
	for name, s := range map[string]agentenv.Spec{
		"a malformed name":  {Agent: "codex", Home: "/h", Extra: []string{"NOT A NAME"}, LookupEnv: lookup(parent)},
		"an unknown agent":  {Agent: "gpt", Home: "/h", LookupEnv: lookup(parent)},
		"a relative home":   {Agent: "codex", Home: "h", LookupEnv: lookup(parent)},
		"no lookup":         {Agent: "codex", Home: "/h"},
		"a relative TMPDIR": {Agent: "codex", Home: "/h", TempDir: "tmp", LookupEnv: lookup(parent)},
	} {
		if _, err := agentenv.Build(s); !core.HasCode(err, core.CodeInvalidArgument) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if got := agentenv.Names(" A, ,B,"); !slices.Equal(got, []string{"A", "B"}) {
		t.Errorf("names = %v", got)
	}
}
