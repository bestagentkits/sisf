package conformancetest

import (
	"strings"
	"time"

	"github.com/bestagentkits/sisf/internal/core"
	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/deploy"
	"github.com/bestagentkits/sisf/pkg/execution"
)

const verified = core.SupportSupported

// NewExecution returns an execution backend that declares, and shows, every
// area the kit grades.
func NewExecution(name string) *Execution {
	return &Execution{BackendName: name, Fail: map[string]error{}, Declared: core.CapabilitySet{
		core.CapLifecycle: verified, core.CapRollback: verified, core.CapObservability: verified,
		core.CapExecIsolation: verified, core.CapExecEgressEnforcement: verified,
	}}
}

// NewDeploy returns a deployment backend that declares, and shows, every
// area the kit grades.
func NewDeploy(name string) *Deploy {
	return &Deploy{BackendName: name, Fail: map[string]error{}, Declared: core.CapabilitySet{
		core.CapLifecycle: verified, core.CapIsolation: verified, core.CapDeployBlueGreen: verified,
		core.CapDeployRollback: verified, core.CapDeployObservedRevision: verified, core.CapDeployMetrics: verified,
	}}
}

// NewAgent returns an agent backend that declares, and shows, every area the
// kit grades.
func NewAgent(name string) *Agent {
	return &Agent{BackendName: name, Fail: map[string]error{}, Declared: core.CapabilitySet{
		core.CapLifecycle: verified, core.CapIsolation: verified, core.CapObservability: verified,
		core.CapAgentCancel: verified,
	}}
}

// SandboxSpec returns a valid sandbox for a probe run.
func SandboxSpec() execution.SandboxSpec {
	return execution.SandboxSpec{
		RunID: "probe-run", Unit: "runner", Image: "example.test/runner@sha256:" + strings.Repeat("a", 64),
		Trust:  execution.TrustUntrusted,
		Egress: core.EgressPolicy{Mode: core.EgressAllowlist},
		Limits: execution.Limits{Timeout: time.Minute},
	}
}

// Artifacts returns two signed builds of the same unit that differ in content.
func Artifacts() (baseline, candidate deploy.Artifact) {
	build := func(fill string) deploy.Artifact {
		digest := "sha256:" + strings.Repeat(fill, 64)
		return deploy.Artifact{
			Unit: "app", Digest: core.ReleaseDigest(digest), Image: "example.test/app@" + digest,
			Signature: core.Signature{KeyID: "probe-key", Algorithm: "ed25519", Value: []byte("probe")},
		}
	}
	return build("a"), build("b")
}

// Task returns a valid task for a probe run.
func Task() agent.Task {
	sha := core.CommitSHA(strings.Repeat("c", 40))
	return agent.Task{
		RunID: "probe-run", IssueID: core.IssueRef{Owner: "acme", Repo: "hello-app", Number: 1},
		Role: agent.RoleCoder, Step: core.StepCoding,
		Handoff: agent.Handoff{SoW: "Answer the probe.", DoD: "The probe run ends."},
		Policy: core.PolicyRefs{
			PolicySHA: sha, ReviewSHA: sha, DecisionSHA: sha, FeatureCatalogSHA: sha,
			ModelRoute: core.ModelRoute{Provider: "example", Model: "small"},
		},
		Limits:         agent.Limits{Timeout: time.Minute},
		IdempotencyKey: "probe-run-1",
	}
}
