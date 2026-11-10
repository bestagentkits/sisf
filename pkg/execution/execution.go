// Package execution defines the contract for the sandboxes that agent runs
// execute in.
//
// Being hosted by a provider is not an isolation guarantee. A backend states
// what it can prove in its capabilities, including whether it can enforce the
// egress policy, and untrusted code runs automatically only where both
// isolation and egress enforcement are verified.
package execution

import (
	"context"
	"io"
	"io/fs"
	"path"
	"time"

	"github.com/bestagentkits/sisf/internal/core"
)

// Trust says whether the code a sandbox will run is trusted. It is always
// stated: an unset value is invalid, so nothing is trusted by omission.
type Trust string

const (
	// TrustTrusted is code the Owner vouches for on a single-user machine.
	TrustTrusted Trust = "trusted"
	// TrustUntrusted is everything else, including any code an agent produced
	// from an issue, a comment or a pull request from outside.
	TrustUntrusted Trust = "untrusted"
)

// Valid reports whether t is a known trust level.
func (t Trust) Valid() bool { return t == TrustTrusted || t == TrustUntrusted }

// UntrustedCodeAllowed reports whether a backend with these capabilities may
// run untrusted code in automatic mode: isolation must be proven and the
// egress policy must be enforceable. A claimed but unverified ability does
// not count.
func UntrustedCodeAllowed(caps core.CapabilitySet) bool {
	return caps.Supports(core.CapExecIsolation) && caps.Supports(core.CapExecEgressEnforcement)
}

// Limits bound what a sandbox may consume. Zero means the backend default,
// except Timeout, which is always required.
type Limits struct {
	Timeout     time.Duration `json:"timeout"`
	CPUMillis   int64         `json:"cpu_millis,omitempty"`
	MemoryBytes int64         `json:"memory_bytes,omitempty"`
	DiskBytes   int64         `json:"disk_bytes,omitempty"`
	PIDs        int64         `json:"pids,omitempty"`
}

// Mount attaches a volume or directory to a sandbox.
type Mount struct {
	Source   string `json:"source"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"read_only,omitempty"`
}

// SandboxSpec describes the sandbox for one run. A sandbox accepts no inbound
// connections and holds only the secrets granted to its own run.
type SandboxSpec struct {
	RunID core.RunID `json:"run_id"`
	// Unit is the runtime unit the sandbox realizes: "runner" or "qa-runner".
	Unit string `json:"unit"`
	// Image is a digest-pinned image reference, or the backend's equivalent.
	Image  string            `json:"image"`
	Trust  Trust             `json:"trust"`
	Egress core.EgressPolicy `json:"egress"`
	Limits Limits            `json:"limits"`
	// Env carries non-secret settings only.
	Env map[string]string `json:"env,omitempty"`
	// SecretGrantIDs name per-run grants issued by the secret broker.
	SecretGrantIDs []string          `json:"secret_grant_ids,omitempty"`
	Mounts         []Mount           `json:"mounts,omitempty"`
	ReadOnlyRootFS bool              `json:"read_only_root_fs,omitempty"`
	Labels         map[string]string `json:"labels,omitempty"`
}

// Validate rejects a spec that leaves trust, egress or the time limit unstated.
func (s SandboxSpec) Validate() error {
	if err := s.RunID.Validate(); err != nil {
		return err
	}
	if err := s.Egress.Validate(); err != nil {
		return err
	}
	l := s.Limits
	switch {
	case s.Unit == "" || s.Image == "":
		return core.NewError(core.CodeInvalidArgument, "sandbox needs a unit and an image")
	case !s.Trust.Valid():
		return core.NewError(core.CodeInvalidArgument, "sandbox must state whether its code is trusted")
	case l.Timeout <= 0:
		return core.NewError(core.CodeInvalidArgument, "sandbox needs a positive timeout")
	case l.CPUMillis < 0 || l.MemoryBytes < 0 || l.DiskBytes < 0 || l.PIDs < 0:
		return core.NewError(core.CodeInvalidArgument, "sandbox limits must not be negative")
	}
	for _, m := range s.Mounts {
		if m.Source == "" || !path.IsAbs(m.Target) {
			return core.NewError(core.CodeInvalidArgument, "a mount needs a source and an absolute target path")
		}
	}
	return nil
}

// SandboxID is a backend's identifier for a sandbox. It is a runtime
// identifier and never doubles as a run or factory identity.
type SandboxID string

// SandboxState is where a sandbox stands in its life.
type SandboxState string

const (
	SandboxCreating  SandboxState = "creating"
	SandboxRunning   SandboxState = "running"
	SandboxStopped   SandboxState = "stopped"
	SandboxDestroyed SandboxState = "destroyed"
)

// Valid reports whether s is a known state.
func (s SandboxState) Valid() bool {
	return s == SandboxCreating || s == SandboxRunning || s == SandboxStopped || s == SandboxDestroyed
}

// SandboxInfo is what a backend observes about a sandbox.
type SandboxInfo struct {
	ID        SandboxID    `json:"id"`
	RunID     core.RunID   `json:"run_id"`
	State     SandboxState `json:"state"`
	Trust     Trust        `json:"trust"`
	Image     string       `json:"image,omitempty"`
	CreatedAt time.Time    `json:"created_at"`
}

// Command is one process to run inside a sandbox.
type Command struct {
	Argv  []string
	Dir   string
	Env   map[string]string
	Stdin io.Reader
	// Stdout and Stderr receive output as it is produced. When nil, output
	// is captured into the result.
	Stdout  io.Writer
	Stderr  io.Writer
	Timeout time.Duration
}

// ExecResult is the outcome of a command. A non-zero exit code is a result,
// not an error; an error means the command could not be run or observed.
type ExecResult struct {
	ExitCode int           `json:"exit_code"`
	Stdout   []byte        `json:"stdout,omitempty"`
	Stderr   []byte        `json:"stderr,omitempty"`
	Duration time.Duration `json:"duration"`
	TimedOut bool          `json:"timed_out,omitempty"`
}

// FileSystem reads and writes files inside one sandbox.
type FileSystem interface {
	ReadFile(ctx context.Context, name string) (io.ReadCloser, error)
	WriteFile(ctx context.Context, name string, content io.Reader, mode fs.FileMode) error
}

// SnapshotRef names a saved copy of a sandbox's file system.
type SnapshotRef struct {
	ID        string      `json:"id"`
	Digest    core.Digest `json:"digest,omitempty"`
	CreatedAt time.Time   `json:"created_at"`
}

// Backend creates and controls sandboxes. An ability a backend lacks is
// declared in Capabilities and refused with NOT_SUPPORTED.
type Backend interface {
	Name() string
	// Capabilities declares, among others, isolation and egress enforcement.
	Capabilities(ctx context.Context) (core.CapabilitySet, error)
	// Health returns nil only when the backend can create sandboxes now.
	Health(ctx context.Context) error
	CreateSandbox(ctx context.Context, spec SandboxSpec) (SandboxInfo, error)
	Exec(ctx context.Context, id SandboxID, cmd Command) (ExecResult, error)
	Files(ctx context.Context, id SandboxID) (FileSystem, error)
	Snapshot(ctx context.Context, id SandboxID) (SnapshotRef, error)
	Inspect(ctx context.Context, id SandboxID) (SandboxInfo, error)
	// Destroy removes the sandbox and everything it held. Destroying a
	// sandbox that is already gone succeeds.
	Destroy(ctx context.Context, id SandboxID) error
}
