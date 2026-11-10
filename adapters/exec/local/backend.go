// Package local is the execution backend that runs commands as local
// processes, for trusted code on a single-user machine.
//
// It isolates nothing and cannot enforce the egress policy, and says so in
// its capabilities, so it refuses untrusted code: anything an agent built
// from an issue, a comment or an outside pull request. A command runs in
// the sandbox's own workspace directory with an environment built from
// nothing but a safe PATH and the variables the spec and the command name,
// so credentials in the factory's own environment never reach it.
package local

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/execution"
)

// DefaultPath is the PATH commands get when the configuration names none.
const DefaultPath = "/usr/local/bin:/usr/bin:/bin"

// DefaultMaxOutputBytes caps each captured output stream.
const DefaultMaxOutputBytes = 8 << 20

// Config is what the backend is built from. Every field is optional.
type Config struct {
	// Root holds the sandboxes' workspaces; a directory under the system's
	// temporary directory when empty.
	Root string
	// Path is the PATH commands get; DefaultPath when empty.
	Path string
	// Clock times commands and sandboxes; the wall clock when nil.
	Clock core.Clock
	// MaxOutputBytes caps each captured output stream.
	MaxOutputBytes int64
}

// Backend is the local execution backend. It is safe for concurrent use.
type Backend struct {
	cfg Config

	mu        sync.Mutex
	sandboxes map[execution.SandboxID]*sandbox
}

type sandbox struct {
	info     execution.SandboxInfo
	spec     execution.SandboxSpec
	dir      string
	deadline time.Time
}

var _ execution.Backend = (*Backend)(nil)

// New builds the backend and its workspace root.
func New(cfg Config) (*Backend, error) {
	if cfg.Root == "" {
		cfg.Root = filepath.Join(os.TempDir(), "sisf-local-sandboxes")
	}
	if cfg.Path == "" {
		cfg.Path = DefaultPath
	}
	if cfg.Clock == nil {
		cfg.Clock = core.SystemClock{}
	}
	if cfg.MaxOutputBytes <= 0 {
		cfg.MaxOutputBytes = DefaultMaxOutputBytes
	}
	if err := os.MkdirAll(cfg.Root, 0o700); err != nil {
		return nil, core.NewError(core.CodeUnavailable, "the sandbox root cannot be created").WithCause(err)
	}
	return &Backend{cfg: cfg, sandboxes: map[execution.SandboxID]*sandbox{}}, nil
}

// Name returns "local".
func (b *Backend) Name() string { return "local" }

// Capabilities declares that the backend runs, inspects and removes
// sandboxes, and that it neither isolates nor enforces egress, limits
// resources or takes snapshots.
func (b *Backend) Capabilities(context.Context) (core.CapabilitySet, error) {
	return core.CapabilitySet{
		core.CapLifecycle: core.SupportSupported, core.CapRollback: core.SupportSupported, core.CapObservability: core.SupportSupported,
		core.CapExecIsolation: core.SupportNotSupported, core.CapExecEgressEnforcement: core.SupportNotSupported,
		core.CapExecResourceLimits: core.SupportNotSupported, core.CapExecSnapshot: core.SupportNotSupported,
	}, nil
}

// Health returns nil when the workspace root is usable.
func (b *Backend) Health(context.Context) error {
	if info, err := os.Stat(b.cfg.Root); err != nil || !info.IsDir() {
		return core.NewError(core.CodeUnavailable, "the sandbox root is missing")
	}
	return nil
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

// CreateSandbox makes a workspace for a run of trusted code. Untrusted code
// is NOT_SUPPORTED, and so is anything the backend cannot honour: mounts,
// resource limits and secret grants.
func (b *Backend) CreateSandbox(ctx context.Context, spec execution.SandboxSpec) (execution.SandboxInfo, error) {
	if err := spec.Validate(); err != nil {
		return execution.SandboxInfo{}, err
	}
	caps, _ := b.Capabilities(ctx) // the local declaration never fails
	l := spec.Limits
	switch {
	case spec.Trust == execution.TrustUntrusted && !execution.UntrustedCodeAllowed(caps):
		return execution.SandboxInfo{}, core.NewError(core.CodeNotSupported,
			"the local backend runs trusted code only: it neither isolates code nor enforces the egress policy")
	case len(spec.Mounts) > 0:
		return execution.SandboxInfo{}, core.NewError(core.CodeNotSupported, "the local backend has no mounts")
	case l.CPUMillis != 0 || l.MemoryBytes != 0 || l.PIDs != 0 || l.DiskBytes != 0:
		return execution.SandboxInfo{}, core.NewError(core.CodeNotSupported, "the local backend cannot limit resources other than time")
	case len(spec.SecretGrantIDs) > 0:
		return execution.SandboxInfo{}, core.NewError(core.CodeNotSupported, "the local backend takes no secret grants")
	}
	if err := checkEnv(spec.Env); err != nil {
		return execution.SandboxInfo{}, err
	}
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return execution.SandboxInfo{}, core.NewError(core.CodeInternal, "no randomness for a sandbox ID").WithCause(err)
	}
	id := execution.SandboxID("local-" + hex.EncodeToString(raw))
	dir := filepath.Join(b.cfg.Root, string(id))
	if err := os.Mkdir(dir, 0o700); err != nil {
		return execution.SandboxInfo{}, core.NewError(core.CodeUnavailable, "the sandbox workspace cannot be created").WithCause(err)
	}
	now := b.cfg.Clock.Now()
	sb := &sandbox{
		info: execution.SandboxInfo{ID: id, RunID: spec.RunID, State: execution.SandboxRunning, Trust: spec.Trust, Image: spec.Image, CreatedAt: now},
		spec: spec, dir: dir, deadline: now.Add(spec.Limits.Timeout),
	}
	b.mu.Lock()
	b.sandboxes[id] = sb
	b.mu.Unlock()
	return sb.info, nil
}

func checkEnv(env map[string]string) error {
	for name := range env {
		if !envName.MatchString(name) {
			return core.NewError(core.CodeInvalidArgument, "environment variable "+name+" is not a variable name")
		}
	}
	return nil
}

// running returns a sandbox that can still run commands.
func (b *Backend) running(id execution.SandboxID) (*sandbox, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	sb, ok := b.sandboxes[id]
	switch {
	case !ok:
		return nil, core.NewError(core.CodeNotFound, "no such sandbox")
	case sb.info.State != execution.SandboxRunning:
		return nil, core.NewError(core.CodeNotFound, "the sandbox was destroyed")
	}
	return sb, nil
}

// Exec runs one command in the sandbox's workspace, stopping it once its
// timeout or the sandbox's time has passed on the backend's clock.
func (b *Backend) Exec(ctx context.Context, id execution.SandboxID, cmd execution.Command) (execution.ExecResult, error) {
	sb, err := b.running(id)
	if err != nil {
		return execution.ExecResult{}, err
	}
	if len(cmd.Argv) == 0 {
		return execution.ExecResult{}, core.NewError(core.CodeInvalidArgument, "a command needs arguments")
	}
	if err := checkEnv(cmd.Env); err != nil {
		return execution.ExecResult{}, err
	}
	dir := sb.dir
	if cmd.Dir != "" {
		if !filepath.IsLocal(cmd.Dir) {
			return execution.ExecResult{}, core.NewError(core.CodeInvalidArgument, "the command's directory must stay inside the workspace")
		}
		dir = filepath.Join(sb.dir, cmd.Dir)
	}
	timeout := sb.deadline.Sub(b.cfg.Clock.Now())
	if timeout <= 0 {
		return execution.ExecResult{}, core.NewError(core.CodeLimitExceeded, "the sandbox has used up its time")
	}
	if cmd.Timeout > 0 && cmd.Timeout < timeout {
		timeout = cmd.Timeout
	}
	return b.run(ctx, sb, dir, cmd, timeout)
}

func (b *Backend) run(ctx context.Context, sb *sandbox, dir string, cmd execution.Command, timeout time.Duration) (execution.ExecResult, error) {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	proc := exec.CommandContext(runCtx, cmd.Argv[0], cmd.Argv[1:]...)
	proc.Dir, proc.Stdin, proc.WaitDelay = dir, cmd.Stdin, 5*time.Second
	proc.Env = []string{"PATH=" + b.cfg.Path, "HOME=" + sb.dir, "TMPDIR=" + sb.dir, "LANG=C.UTF-8"}
	for _, layer := range []map[string]string{sb.spec.Env, cmd.Env} {
		for k, v := range layer {
			proc.Env = append(proc.Env, k+"="+v)
		}
	}
	var outBuf, errBuf bytes.Buffer
	proc.Stdout, proc.Stderr = cmd.Stdout, cmd.Stderr
	if proc.Stdout == nil {
		proc.Stdout = &capped{w: &outBuf, left: b.cfg.MaxOutputBytes}
	}
	if proc.Stderr == nil {
		proc.Stderr = &capped{w: &errBuf, left: b.cfg.MaxOutputBytes}
	}
	started := b.cfg.Clock.Now()
	if err := proc.Start(); err != nil {
		return execution.ExecResult{}, core.NewError(core.CodeInvalidArgument, "the command could not be started").WithCause(err)
	}
	var timedOut bool
	var mu sync.Mutex
	stop := make(chan struct{})
	go func() {
		select {
		case <-b.cfg.Clock.After(timeout):
			mu.Lock()
			timedOut = true
			mu.Unlock()
			cancel()
		case <-stop:
		}
	}()
	err := proc.Wait()
	close(stop)
	mu.Lock()
	result := execution.ExecResult{TimedOut: timedOut, Duration: b.cfg.Clock.Now().Sub(started), ExitCode: proc.ProcessState.ExitCode()}
	mu.Unlock()
	if cmd.Stdout == nil {
		result.Stdout = outBuf.Bytes()
	}
	if cmd.Stderr == nil {
		result.Stderr = errBuf.Bytes()
	}
	var exitErr *exec.ExitError
	switch {
	case err == nil || errors.As(err, &exitErr) || result.TimedOut:
		return result, nil
	case ctx.Err() != nil:
		return result, ctx.Err()
	}
	return result, core.NewError(core.CodeInternal, "the command could not be observed").WithCause(err)
}

// capped keeps the first left bytes written and drops the rest.
type capped struct {
	w    io.Writer
	left int64
}

func (c *capped) Write(p []byte) (int, error) {
	n := len(p)
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	c.left -= int64(len(p))
	if _, err := c.w.Write(p); err != nil {
		return 0, err
	}
	return n, nil
}
