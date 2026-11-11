package install

import (
	"slices"
	"testing"
)

// The dispatcher learns the operator's account name only when its coding
// agent runs with the operator's own home, where that agent's login is
// kept under the name.
func TestTheDispatcherLearnsTheOperatorsAccountOnlyForAnAgentInTheOperatorsHome(t *testing.T) {
	t.Parallel()
	user := func(string) string { return "operator" }
	if got := operatorAccountEnv("/Users/operator", user); !slices.Equal(got, []string{"USER=operator"}) {
		t.Errorf("with the operator's home: %v", got)
	}
	if got := operatorAccountEnv("", user); got != nil {
		t.Errorf("with a private home: %v", got)
	}
	if got := operatorAccountEnv("/Users/operator", func(string) string { return "" }); got != nil {
		t.Errorf("with no account name set: %v", got)
	}
}
