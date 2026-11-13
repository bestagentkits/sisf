package stages

import (
	"context"
	"errors"
	"testing"
	"time"

	execlocal "github.com/bestagentkits/sisf/adapters/exec/local"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/execution"
)

// networkGone stands in for the docker backend while its sandbox network
// does not exist yet, as at boot before it is created.
type networkGone struct{ execution.Backend }

func (networkGone) Name() string { return "docker" }
func (networkGone) Capabilities(context.Context) (core.CapabilitySet, error) {
	return nil, core.NewError(core.CodeNotSupported, "the sandbox network sisf-sandbox does not exist; it must be an internal Docker network")
}

// Only a refusal the configuration alone decides is marked as one: starting
// again with that configuration refuses again. What the environment
// reports, such as a sandbox network that is not there yet, can change by
// the next start, so it is not marked, whatever its code.
func TestOnlyARefusalTheConfigurationDecidesIsMarkedAsOne(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	local, err := execlocal.New(execlocal.Config{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	for name, choice := range map[string]ExecutionChoice{
		"only the local backend": {Profile: ProfileProduction, Agent: "claude", Local: local},
		"no backend at all":      {Profile: ProfileProduction, Agent: "claude"},
		"no build command":       {Profile: ProfileProduction, Agent: "claude", Docker: isolating{}, BuildTimeout: time.Minute},
		"no program store":       {Profile: ProfileProduction, Agent: "claude", Docker: isolating{}, BuildArgv: []string{"build"}, BuildTimeout: time.Minute},
	} {
		if _, err := SelectImageBuilder(ctx, choice); err == nil || !errors.Is(err, ErrConfiguration) {
			t.Errorf("%s: %v, want a refusal marked as the configuration's", name, err)
		}
	}
	_, err = SelectImageBuilder(ctx, ExecutionChoice{Profile: ProfileProduction, Agent: "claude", Docker: networkGone{},
		BuildArgv: []string{"build"}, BuildTimeout: time.Minute, Programs: newProgramStore(t)})
	if !core.HasCode(err, core.CodeNotSupported) || errors.Is(err, ErrConfiguration) {
		t.Fatalf("a missing sandbox network: %v, want NOT_SUPPORTED not marked as the configuration's", err)
	}
}
