package wiring_test

import (
	"testing"

	"github.com/bestagentkits/sisf/cmd/factoryd/wiring"
	"github.com/bestagentkits/sisf/internal/budget"
	"github.com/bestagentkits/sisf/pkg/agent/agentenv"
	"github.com/bestagentkits/sisf/pkg/core"
)

// Each admin key needs the one workspace or project the factory's runs
// are charged to; neither is accepted alone.
func TestAProviderCostKeyNeedsTheFactorysOwnScope(t *testing.T) {
	t.Parallel()
	for name, vars := range map[string]map[string]string{
		"anthropic key alone":       {wiring.EnvAnthropicAdminKey: "k"},
		"anthropic workspace alone": {wiring.EnvAnthropicCostWorkspace: "wrkspc_factory"},
		"openai key alone":          {wiring.EnvOpenAIAdminKey: "k"},
		"openai project alone":      {wiring.EnvOpenAICostProject: "proj_factory"},
	} {
		if _, err := wiring.ProviderCostFromEnv(envOf(vars)); !core.HasCode(err, core.CodeInvalidArgument) {
			t.Errorf("%s: %v, want INVALID_ARGUMENT", name, err)
		}
	}
	sources, err := wiring.ProviderCostFromEnv(envOf(map[string]string{
		wiring.EnvAnthropicAdminKey: "k", wiring.EnvAnthropicCostWorkspace: "wrkspc_factory",
		wiring.EnvOpenAIAdminKey: "k", wiring.EnvOpenAICostProject: "proj_factory",
	}))
	if err != nil || sources[budget.ProviderAnthropic] == nil || sources[budget.ProviderOpenAI] == nil {
		t.Fatalf("both providers configured: %v, %v", sources, err)
	}
	if sources, err := wiring.ProviderCostFromEnv(envOf(nil)); err != nil || len(sources) != 0 {
		t.Fatalf("nothing configured: %v, %v", sources, err)
	}
}

// The admin keys that read the providers' spend stay in the control zone:
// no agent environment may carry them, even when an operator asks.
func TestAProviderAdminKeyNeverReachesAnAgent(t *testing.T) {
	t.Parallel()
	for _, name := range []string{wiring.EnvAnthropicAdminKey, wiring.EnvOpenAIAdminKey} {
		if err := agentenv.Allowed(name); !core.HasCode(err, core.CodePermissionDenied) {
			t.Errorf("%s may be passed to an agent: %v", name, err)
		}
		_, err := agentenv.Build(agentenv.Spec{Agent: "claude", Home: "/home/agent", Extra: []string{name},
			LookupEnv: func(string) (string, bool) { return "secret", true }})
		if !core.HasCode(err, core.CodePermissionDenied) {
			t.Errorf("an agent environment carried %s: %v", name, err)
		}
	}
}
