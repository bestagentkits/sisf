package wiring

import (
	"strings"

	"github.com/bestagentkits/sisf/internal/budget"
	"github.com/bestagentkits/sisf/internal/budget/providercost"
	"github.com/bestagentkits/sisf/pkg/core"
)

// The control zone's credentials for reading what the providers report
// was spent. Each admin key comes with the one workspace or project the
// factory's runs are charged to, so other work there is not counted as the
// factory's. Their names start with SISF_, which no agent environment
// accepts, so a key the control zone holds never reaches an agent.
const (
	EnvAnthropicAdminKey      = "SISF_ANTHROPIC_ADMIN_KEY"
	EnvAnthropicCostWorkspace = "SISF_ANTHROPIC_COST_WORKSPACE"
	EnvOpenAIAdminKey         = "SISF_OPENAI_ADMIN_KEY"
	EnvOpenAICostProject      = "SISF_OPENAI_COST_PROJECT"
)

// ProviderCostFromEnv builds the providers' cost sources the environment
// configures. A key without its workspace or project, or the reverse, is
// refused rather than read across the whole organization.
func ProviderCostFromEnv(getenv func(string) string) (map[string]budget.CostSource, error) {
	sources := map[string]budget.CostSource{}
	pair := func(keyName, scopeName string) (core.SecretValue, string, error) {
		key, scope := strings.TrimSpace(getenv(keyName)), strings.TrimSpace(getenv(scopeName))
		if (key == "") != (scope == "") {
			return core.SecretValue{}, "", core.NewError(core.CodeInvalidArgument, keyName+" and "+scopeName+" are set together or not at all")
		}
		return core.NewSecretValue([]byte(key)), scope, nil
	}
	key, workspace, err := pair(EnvAnthropicAdminKey, EnvAnthropicCostWorkspace)
	if err != nil {
		return nil, err
	}
	if workspace != "" {
		sources[budget.ProviderAnthropic] = providercost.Anthropic{AdminKey: key, WorkspaceID: workspace}
	}
	key, project, err := pair(EnvOpenAIAdminKey, EnvOpenAICostProject)
	if err != nil {
		return nil, err
	}
	if project != "" {
		sources[budget.ProviderOpenAI] = providercost.OpenAI{AdminKey: key, ProjectID: project}
	}
	return sources, nil
}
