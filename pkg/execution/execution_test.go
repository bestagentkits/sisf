package execution_test

import (
	"context"
	"io"
	"io/fs"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/execution"
)

func TestUntrustedCodeNeedsProvenIsolationAndEnforcedEgress(t *testing.T) {
	both := core.CapabilitySet{
		core.CapExecIsolation:         core.SupportSupported,
		core.CapExecEgressEnforcement: core.SupportSupported,
	}
	if !execution.UntrustedCodeAllowed(both) {
		t.Fatal("proven isolation with enforced egress may run untrusted code")
	}
	refused := map[string]core.CapabilitySet{
		"nothing declared": nil,
		"isolation only":   {core.CapExecIsolation: core.SupportSupported},
		"egress only":      {core.CapExecEgressEnforcement: core.SupportSupported},
		"isolation claimed but unproven": {
			core.CapExecIsolation:         core.SupportUnverified,
			core.CapExecEgressEnforcement: core.SupportSupported,
		},
		"egress cannot be enforced": {
			core.CapExecIsolation:         core.SupportSupported,
			core.CapExecEgressEnforcement: core.SupportNotSupported,
		},
	}
	for name, caps := range refused {
		if execution.UntrustedCodeAllowed(caps) {
			t.Errorf("%s: untrusted code must be refused", name)
		}
	}
}

func validSpec() execution.SandboxSpec {
	return execution.SandboxSpec{
		RunID:  "run-0001",
		Unit:   "runner",
		Image:  "ghcr.io/acme/runner@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Trust:  execution.TrustUntrusted,
		Egress: core.EgressPolicy{Mode: core.EgressAllowlist, Allow: []string{"github.com"}},
		Limits: execution.Limits{Timeout: 30 * time.Minute, MemoryBytes: 2 << 30, PIDs: 256},
	}
}

func TestSandboxSpecValidation(t *testing.T) {
	if err := validSpec().Validate(); err != nil {
		t.Fatalf("valid spec rejected: %v", err)
	}
	cases := map[string]func(*execution.SandboxSpec){
		"no run":            func(s *execution.SandboxSpec) { s.RunID = "" },
		"no unit":           func(s *execution.SandboxSpec) { s.Unit = "" },
		"no image":          func(s *execution.SandboxSpec) { s.Image = "" },
		"trust not stated":  func(s *execution.SandboxSpec) { s.Trust = "" },
		"no egress policy":  func(s *execution.SandboxSpec) { s.Egress = core.EgressPolicy{} },
		"no time limit":     func(s *execution.SandboxSpec) { s.Limits.Timeout = 0 },
		"negative memory":   func(s *execution.SandboxSpec) { s.Limits.MemoryBytes = -1 },
		"relative mount":    func(s *execution.SandboxSpec) { s.Mounts = []execution.Mount{{Source: "work", Target: "data"}} },
		"mount without src": func(s *execution.SandboxSpec) { s.Mounts = []execution.Mount{{Target: "/data"}} },
	}
	for name, mutate := range cases {
		spec := validSpec()
		mutate(&spec)
		if core.CodeOf(spec.Validate()) != core.CodeInvalidArgument {
			t.Errorf("%s: want %s, got %v", name, core.CodeInvalidArgument, spec.Validate())
		}
	}
}

func TestTrustAndSandboxStatesAreClosedSets(t *testing.T) {
	if !execution.TrustTrusted.Valid() || !execution.TrustUntrusted.Valid() || execution.Trust("semi").Valid() {
		t.Error("trust validation is wrong")
	}
	for _, s := range []execution.SandboxState{execution.SandboxCreating, execution.SandboxRunning,
		execution.SandboxStopped, execution.SandboxDestroyed} {
		if !s.Valid() {
			t.Errorf("sandbox state %q must be valid", s)
		}
	}
	if execution.SandboxState("zombie").Valid() {
		t.Error("unknown sandbox states must be rejected")
	}
}

// localStub proves the contract can be met with the exported types alone.
type localStub struct{}

func (localStub) Name() string { return "stub" }
func (localStub) Capabilities(context.Context) (core.CapabilitySet, error) {
	return core.CapabilitySet{core.CapExecSnapshot: core.SupportNotSupported}, nil
}
func (localStub) Health(context.Context) error { return nil }
func (localStub) CreateSandbox(_ context.Context, spec execution.SandboxSpec) (execution.SandboxInfo, error) {
	return execution.SandboxInfo{ID: "sb-1", RunID: spec.RunID, State: execution.SandboxRunning, Trust: spec.Trust}, nil
}
func (localStub) Exec(_ context.Context, _ execution.SandboxID, cmd execution.Command) (execution.ExecResult, error) {
	if cmd.Stdout != nil {
		_, _ = io.WriteString(cmd.Stdout, "ok\n")
	}
	return execution.ExecResult{ExitCode: 0}, nil
}
func (localStub) Files(context.Context, execution.SandboxID) (execution.FileSystem, error) {
	return stubFiles{}, nil
}
func (localStub) Snapshot(context.Context, execution.SandboxID) (execution.SnapshotRef, error) {
	return execution.SnapshotRef{}, core.NewError(core.CodeNotSupported, "this backend cannot snapshot")
}
func (localStub) Inspect(context.Context, execution.SandboxID) (execution.SandboxInfo, error) {
	return execution.SandboxInfo{}, core.NewError(core.CodeNotFound, "no such sandbox")
}
func (localStub) Destroy(context.Context, execution.SandboxID) error { return nil }

type stubFiles struct{}

func (stubFiles) ReadFile(context.Context, string) (io.ReadCloser, error) {
	return nil, core.NewError(core.CodeNotFound, "no such file")
}
func (stubFiles) WriteFile(context.Context, string, io.Reader, fs.FileMode) error { return nil }

func TestBackendContractIsImplementable(t *testing.T) {
	var backend execution.Backend = localStub{}
	ctx := context.Background()
	info, err := backend.CreateSandbox(ctx, validSpec())
	if err != nil || info.State != execution.SandboxRunning || info.Trust != execution.TrustUntrusted {
		t.Fatalf("CreateSandbox = %+v, %v", info, err)
	}
	if _, err := backend.Snapshot(ctx, info.ID); !core.HasCode(err, core.CodeNotSupported) {
		t.Fatalf("a missing ability must be refused as NOT_SUPPORTED, got %v", err)
	}
	files, err := backend.Files(ctx, info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := files.ReadFile(ctx, "/missing"); !core.HasCode(err, core.CodeNotFound) {
		t.Fatalf("ReadFile error = %v", err)
	}
}
