package stages

import (
	"context"
	"time"

	"github.com/bestagentkits/sisf/internal/intake/prgate"
	"github.com/bestagentkits/sisf/internal/supervisor/programstore"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/execution"
	"github.com/bestagentkits/sisf/pkg/github"
)

// Profiles a factory runs in.
const (
	// ProfileSimulation is the trusted simulation: a scripted fake agent on
	// a fake GitHub, where no code an agent wrote is ever run.
	ProfileSimulation = "sim"
	// ProfileProduction runs real agents on real code.
	ProfileProduction = "production"
)

// ExecutionChoice is what the factory may build candidates with.
type ExecutionChoice struct {
	Profile string
	// Agent names the coding agent backend, for example "fake" or "claude".
	Agent string
	// Docker is the isolating backend, nil when none is configured.
	Docker execution.Backend
	// Local is the local process backend, nil when none is configured.
	Local execution.Backend
	// Specs builds the sandboxes of a build.
	Specs SandboxSpecs
	// BuildArgv and BuildTimeout are the sandboxed build command.
	BuildArgv    []string
	BuildTimeout time.Duration
	// Programs keeps the program each sandboxed build exports, where the
	// deploy backend runs programs; nil otherwise, and then no candidate is
	// built in a sandbox at all.
	Programs *programstore.Store
	// Suite judges commits in the simulation profile; see SimulatedImages.
	Suite func(ctx context.Context, req ImageRequest) (bool, string)
}

// SelectImageBuilder picks how candidates are built. Code an agent wrote is
// untrusted: it is built only in a sandbox of an isolating backend. With
// only the local backend there is none, and the choice is refused with
// PERMISSION_DENIED, except in the trusted simulation profile with the
// scripted fake agent, whose builds run no code at all.
func SelectImageBuilder(ctx context.Context, c ExecutionChoice) (ImageBuilder, error) {
	if c.Profile == ProfileSimulation && c.Agent == "fake" {
		return SimulatedImages{Suite: c.Suite}, nil
	}
	if c.Docker != nil {
		caps, err := c.Docker.Capabilities(ctx)
		if err != nil {
			return nil, err
		}
		if !execution.UntrustedCodeAllowed(caps) {
			return nil, untrustedRefused(c.Docker.Name())
		}
		// Without a build command every build would fail; refusing here
		// keeps a configuration error from being charged to agents' patches.
		if len(c.BuildArgv) == 0 || c.BuildTimeout <= 0 {
			return nil, core.NewError(core.CodeInvalidArgument,
				"candidates are built in a sandbox, which needs a build command (--build-command) and a positive build timeout (--build-timeout)")
		}
		if c.Programs == nil {
			return nil, unobservedImagesRefused()
		}
		return SandboxImages{Exec: c.Docker, Specs: c.Specs, Argv: c.BuildArgv, Timeout: c.BuildTimeout, Programs: c.Programs}, nil
	}
	if c.Local != nil {
		return nil, untrustedRefused(c.Local.Name())
	}
	return nil, core.NewError(core.CodeNotSupported, "no execution backend is configured to build candidates")
}

// SandboxSpecs builds the sandbox specs of builds. A build never holds a
// secret: the build of an agent's patch is untrusted, and the build of a
// pull request goes through the fork pull-request gate before its spec is
// built.
type SandboxSpecs struct {
	Image  string
	Egress core.EgressPolicy
	Limits execution.Limits
	// Gate decides how far a pull request may go; nil refuses every one.
	Gate *prgate.Gate
}

// ForRun is the sandbox of the build of an agent run's patch.
func (s SandboxSpecs) ForRun(run core.RunID) execution.SandboxSpec {
	return execution.SandboxSpec{
		RunID: run, Unit: "runner", Image: s.Image, Trust: execution.TrustUntrusted,
		Egress: s.Egress, Limits: s.Limits, ReadOnlyRootFS: true,
		Labels: map[string]string{"sisf.run": string(run), "sisf.purpose": "build"},
	}
}

// ForPullRequest is the sandbox of the checks of a pull request at its
// current head. The gate is applied before the spec exists: a rejected pull
// request gets no sandbox, and one not yet accepted gets no secret.
func (s SandboxSpecs) ForPullRequest(ctx context.Context, pr github.PullRequest, run core.RunID, grants []string) (execution.SandboxSpec, prgate.Decision, error) {
	if s.Gate == nil {
		return execution.SandboxSpec{}, prgate.Decision{}, core.NewError(core.CodePermissionDenied, "no pull request gate is configured")
	}
	decision, err := s.Gate.Evaluate(ctx, pr)
	if err != nil {
		return execution.SandboxSpec{}, decision, err
	}
	spec := s.ForRun(run)
	spec.Trust, spec.SecretGrantIDs = execution.TrustTrusted, grants
	spec.Labels["sisf.purpose"] = "pull_request"
	if err := decision.ApplyTo(&spec); err != nil {
		return execution.SandboxSpec{}, decision, err
	}
	return spec, decision, spec.Validate()
}
