// Package docker is the execution backend that runs agent and QA runs in
// ephemeral Docker containers.
//
// It reaches the Docker API only through the Docker request filter
// (internal/dockerfilter), never a daemon socket, and every container it
// asks for is a runner of this factory on the sandbox network: no inbound
// connections, a read-only root file system, every capability dropped,
// no-new-privileges, a numeric non-root user, memory, CPU and process
// limits, tmpfs scratch space and nothing from the host. Each command of a
// sandbox runs in a fresh container that is removed when the command ends,
// within the sandbox's time limit. When the host has gVisor the containers
// run under runsc, and the backend says so in its capabilities.
//
// After creating a container the backend reads its configuration back and
// refuses to start it unless the daemon applied the hardening, so neither a
// misconfigured filter nor a daemon that ignores a field goes unnoticed.
//
// The backend has no exec, attach or archive access, so Files and Snapshot
// are NOT_SUPPORTED and a command cannot read standard input.
package docker

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/bestagentkits/sisf/internal/dockerfilter"
	"github.com/bestagentkits/sisf/internal/secretbroker"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/execution"
	"github.com/bestagentkits/sisf/pkg/secrets"
)

// CapGVisor is supported when runner containers run under gVisor (runsc).
const CapGVisor core.Capability = "execution.gvisor"

// Defaults of the backend.
const (
	DefaultAPIVersion     = "1.44"
	DefaultUser           = "65534:65534"
	DefaultMaxOutputBytes = 8 << 20
	gvisorRuntime         = "runsc"
	workspaceDir          = "/workspace"
)

// DefaultLimits apply where a sandbox spec leaves a limit at zero.
var DefaultLimits = execution.Limits{CPUMillis: 2000, MemoryBytes: 4 << 30, PIDs: 1024, DiskBytes: 2 << 30}

// SecretSource hands a sandbox the secrets granted to its run; the secret
// broker is one.
type SecretSource interface {
	Grant(ctx context.Context, grantID string) (secrets.Grant, error)
	RedeemFor(ctx context.Context, use secretbroker.Use) (core.SecretValue, error)
}

// Config is what the backend is built from.
type Config struct {
	// Endpoint is the base URL of the Docker request filter, for example
	// "http://docker-filter:2375", or "http://docker-filter" with a
	// Transport that dials the filter's socket.
	Endpoint string
	// Transport carries requests to the filter; the default transport when nil.
	Transport http.RoundTripper
	// APIVersion is the Docker API version used; DefaultAPIVersion when empty.
	APIVersion string
	// FactoryID is the value of the runner label, as the filter expects it.
	FactoryID core.FactoryID
	// RunnerLabel and NamePrefix default to the filter's defaults.
	RunnerLabel string
	NamePrefix  string
	// Network is the sandbox network.
	Network string
	// EgressProxy is the egress proxy's URL on the sandbox network. Without
	// it the backend cannot enforce the egress policy and says so.
	EgressProxy string
	// RequireGVisor declares isolation only when gVisor is present.
	RequireGVisor bool
	// User runs every command; DefaultUser when empty.
	User string
	// Limits fill the limits a spec leaves at zero; DefaultLimits when zero.
	Limits execution.Limits
	// Secrets redeems the grants a spec names. Without it a spec that names
	// grants is NOT_SUPPORTED.
	Secrets SecretSource
	// Clock times commands and sandboxes; the wall clock when nil.
	Clock core.Clock
	// MaxOutputBytes caps each captured output stream.
	MaxOutputBytes int64
}

var proxyURLPattern = regexp.MustCompile(`^http://[A-Za-z0-9.-]+(:[0-9]{1,5})?/?$`)

// Backend is the docker execution backend. It is safe for concurrent use.
type Backend struct {
	cfg    Config
	api    *client
	labels map[string]string

	mu        sync.Mutex
	sandboxes map[execution.SandboxID]*sandbox
	gvisor    *bool
}

var _ execution.Backend = (*Backend)(nil)

// New builds the backend, refusing an incomplete configuration.
func New(cfg Config) (*Backend, error) {
	if cfg.APIVersion == "" {
		cfg.APIVersion = DefaultAPIVersion
	}
	if cfg.RunnerLabel == "" {
		cfg.RunnerLabel = dockerfilter.DefaultRunnerLabel
	}
	if cfg.NamePrefix == "" {
		cfg.NamePrefix = dockerfilter.DefaultNamePrefix
	}
	if cfg.User == "" {
		cfg.User = DefaultUser
	}
	if cfg.Limits == (execution.Limits{}) {
		cfg.Limits = DefaultLimits
	}
	if cfg.Clock == nil {
		cfg.Clock = core.SystemClock{}
	}
	if cfg.MaxOutputBytes <= 0 {
		cfg.MaxOutputBytes = DefaultMaxOutputBytes
	}
	base, err := url.Parse(cfg.Endpoint)
	switch {
	case err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "":
		return nil, invalid("the backend needs the Docker filter's base URL")
	case cfg.Network == "" || strings.ContainsAny(cfg.Network, " /:"):
		return nil, invalid("the backend needs the sandbox network's name")
	case cfg.EgressProxy != "" && !proxyURLPattern.MatchString(cfg.EgressProxy):
		return nil, invalid("the egress proxy must be a plain http URL of a host on the sandbox network")
	case cfg.Limits.CPUMillis <= 0 || cfg.Limits.MemoryBytes <= 0 || cfg.Limits.PIDs <= 0 || cfg.Limits.DiskBytes <= 0:
		return nil, invalid("default limits must all be positive")
	}
	if err := cfg.FactoryID.Validate(); err != nil {
		return nil, err
	}
	transport := cfg.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	return &Backend{
		cfg:       cfg,
		api:       &client{base: strings.TrimSuffix(cfg.Endpoint, "/"), version: cfg.APIVersion, http: &http.Client{Transport: transport}},
		labels:    map[string]string{cfg.RunnerLabel: string(cfg.FactoryID)},
		sandboxes: map[execution.SandboxID]*sandbox{},
	}, nil
}

// Name returns "docker".
func (b *Backend) Name() string { return "docker" }

// Health returns nil when the filter and the daemon behind it answer.
func (b *Backend) Health(ctx context.Context) error { return b.api.ping(ctx) }

// Capabilities declares what the backend proves. Isolation rests on the
// filter-enforced and read-back hardening, and on gVisor when the backend
// was told to require it; egress enforcement on the egress proxy being the
// sandbox network's only way out.
func (b *Backend) Capabilities(ctx context.Context) (core.CapabilitySet, error) {
	gvisor, err := b.hasGVisor(ctx)
	if err != nil {
		return nil, err
	}
	caps := core.CapabilitySet{
		core.CapLifecycle: core.SupportSupported, core.CapRollback: core.SupportSupported,
		core.CapObservability: core.SupportSupported, core.CapExecResourceLimits: core.SupportSupported,
		core.CapExecIsolation: core.SupportSupported, core.CapExecEgressEnforcement: core.SupportSupported,
		core.CapExecSnapshot: core.SupportNotSupported, CapGVisor: core.SupportNotSupported,
	}
	if gvisor {
		caps[CapGVisor] = core.SupportSupported
	} else if b.cfg.RequireGVisor {
		caps[core.CapExecIsolation] = core.SupportNotSupported
	}
	if b.cfg.EgressProxy == "" {
		caps[core.CapExecEgressEnforcement] = core.SupportNotSupported
	}
	return caps, nil
}

// hasGVisor asks once whether the host has runsc and remembers the answer.
func (b *Backend) hasGVisor(ctx context.Context) (bool, error) {
	b.mu.Lock()
	known := b.gvisor
	b.mu.Unlock()
	if known != nil {
		return *known, nil
	}
	runtimes, err := b.api.runtimes(ctx)
	if err != nil {
		return false, core.NewError(core.CodeUnavailable, "the backend cannot tell which runtimes the host has").WithCause(err)
	}
	_, ok := runtimes[gvisorRuntime]
	b.mu.Lock()
	b.gvisor = &ok
	b.mu.Unlock()
	return ok, nil
}

// Files is NOT_SUPPORTED: the filter allows no archive access.
func (b *Backend) Files(context.Context, execution.SandboxID) (execution.FileSystem, error) {
	return nil, core.NewError(core.CodeNotSupported, "the docker backend has no file access into a sandbox")
}

// Snapshot is NOT_SUPPORTED: the filter allows no commit.
func (b *Backend) Snapshot(context.Context, execution.SandboxID) (execution.SnapshotRef, error) {
	return execution.SnapshotRef{}, core.NewError(core.CodeNotSupported, "the docker backend takes no snapshots")
}

func invalid(message string) *core.Error { return core.NewError(core.CodeInvalidArgument, message) }

// now is the backend's clock.
func (b *Backend) now() time.Time { return b.cfg.Clock.Now() }
