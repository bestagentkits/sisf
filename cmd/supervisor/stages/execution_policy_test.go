package stages

import (
	"context"
	"testing"
	"time"

	execlocal "github.com/bestagentkits/sisf/adapters/exec/local"
	"github.com/bestagentkits/sisf/internal/intake/prgate"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/execution"
	"github.com/bestagentkits/sisf/pkg/github"
)

// isolating stands in for the docker backend: it declares isolation and
// egress enforcement and runs nothing.
type isolating struct{ execution.Backend }

func (isolating) Name() string { return "docker" }
func (isolating) Capabilities(context.Context) (core.CapabilitySet, error) {
	return core.CapabilitySet{core.CapExecIsolation: core.SupportSupported, core.CapExecEgressEnforcement: core.SupportSupported}, nil
}

func TestUntrustedCodeIsBuiltOnlyInAnIsolatingSandbox(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	local, err := execlocal.New(execlocal.Config{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	refused := map[string]ExecutionChoice{
		"a real agent with only the local backend":           {Profile: ProfileProduction, Agent: "claude", Local: local},
		"the fake agent outside the simulation profile":      {Profile: ProfileProduction, Agent: "fake", Local: local},
		"a real agent in the simulation profile, local only": {Profile: ProfileSimulation, Agent: "codex", Local: local},
	}
	for name, choice := range refused {
		if _, err := SelectImageBuilder(ctx, choice); !core.HasCode(err, core.CodePermissionDenied) {
			t.Errorf("%s: %v, want PERMISSION_DENIED", name, err)
		}
	}
	if _, err := SelectImageBuilder(ctx, ExecutionChoice{Profile: ProfileProduction, Agent: "claude"}); !core.HasCode(err, core.CodeNotSupported) {
		t.Errorf("no backend at all: %v", err)
	}
	b, err := SelectImageBuilder(ctx, ExecutionChoice{Profile: ProfileSimulation, Agent: "fake", Local: local})
	if err != nil || b.Name() != "simulated" {
		t.Fatalf("the trusted simulation: %v, %v", b, err)
	}
	b, err = SelectImageBuilder(ctx, ExecutionChoice{Profile: ProfileProduction, Agent: "claude", Local: local, Docker: isolating{},
		BuildArgv: []string{"build"}, BuildTimeout: time.Minute})
	if err != nil || b.Name() != "sandbox:docker" {
		t.Fatalf("with the docker backend: %v, %v", b, err)
	}
	// A sandboxed builder with no command would fail every build, so the
	// choice itself is refused, before any patch is charged for it.
	for name, choice := range map[string]ExecutionChoice{
		"no command": {Profile: ProfileProduction, Agent: "claude", Docker: isolating{}, BuildTimeout: time.Minute},
		"no timeout": {Profile: ProfileProduction, Agent: "claude", Docker: isolating{}, BuildArgv: []string{"build"}},
	} {
		if _, err := SelectImageBuilder(ctx, choice); !core.HasCode(err, core.CodeInvalidArgument) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// The local backend cannot be passed off as the isolating one.
	if _, err := (SandboxImages{Exec: local, Argv: []string{"x"}, Timeout: time.Minute}).BuildImage(ctx, ImageRequest{Untrusted: true}); !core.HasCode(err, core.CodePermissionDenied) {
		t.Fatalf("an untrusted build on the local backend: %v", err)
	}
}

func TestTheForkPullRequestGateDecidesTheSandboxBeforeItIsBuilt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	gate, err := prgate.New(prgate.Config{Visibility: prgate.VisibilityPublic})
	if err != nil {
		t.Fatal(err)
	}
	specs := SandboxSpecs{Image: "runner@sha256:1", Egress: core.EgressPolicy{Mode: core.EgressAllowlist}, Limits: execution.Limits{Timeout: time.Minute}, Gate: gate}
	fork := github.PullRequest{Repo: github.RepoRef{Owner: "acme", Name: "app"}, Number: 7, IsFork: true,
		HeadSHA: "0123456789abcdef0123456789abcdef01234567", Author: github.User{Login: "stranger"}, AuthorAssociation: "NONE"}
	spec, decision, err := specs.ForPullRequest(ctx, fork, "run-1", []string{"grant-1"})
	if err != nil {
		t.Fatal(err)
	}
	if spec.Trust != execution.TrustUntrusted || len(spec.SecretGrantIDs) != 0 || decision.Stage != prgate.StageSandboxOnly {
		t.Fatalf("a fork pull request got %+v under %+v", spec, decision)
	}
	member := fork
	member.IsFork, member.AuthorAssociation = false, "MEMBER"
	spec, _, err = specs.ForPullRequest(ctx, member, "run-2", []string{"grant-1"})
	if err != nil || spec.Trust != execution.TrustTrusted || len(spec.SecretGrantIDs) != 1 {
		t.Fatalf("a collaborator's pull request got %+v, %v", spec, err)
	}
	private, _ := prgate.New(prgate.Config{Visibility: prgate.VisibilityPrivate})
	specs.Gate = private
	if _, _, err := specs.ForPullRequest(ctx, fork, "run-3", nil); !core.HasCode(err, core.CodePermissionDenied) {
		t.Fatalf("an outsider's pull request on a private repository got a sandbox: %v", err)
	}
	if run := specs.ForRun("run-4"); run.Trust != execution.TrustUntrusted || len(run.SecretGrantIDs) != 0 {
		t.Fatalf("an agent's build got %+v", run)
	}
}
