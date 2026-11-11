package agentenv_test

import (
	"slices"
	"testing"

	"github.com/bestagentkits/sisf/pkg/agent/agentenv"
)

// An agent running with the operator's own home gets the operator's USER,
// which a tool keeping its login in the OS keychain looks it up under. An
// agent with a private home never learns the account's name.
func TestOnlyAnAgentInTheOperatorsHomeLearnsTheOperatorsAccount(t *testing.T) {
	t.Parallel()
	vars := map[string]string{"PATH": "/usr/bin", "USER": "operator", "LOGNAME": "operator"}
	own, err := agentenv.Build(agentenv.Spec{Agent: "claude", Home: "/Users/operator", OperatorHome: true, TempDir: "/ws/tmp", LookupEnv: lookup(vars)})
	if err != nil || !slices.Equal(own, []string{"PATH=/usr/bin", "HOME=/Users/operator", "TMPDIR=/ws/tmp", "USER=operator"}) {
		t.Errorf("with the operator's home: %v, %v", own, err)
	}
	private, err := agentenv.Build(agentenv.Spec{Agent: "claude", Home: "/ws/.home", LookupEnv: lookup(vars)})
	if err != nil || slices.Contains(private, "USER=operator") {
		t.Errorf("with a private home: %v, %v", private, err)
	}
}
