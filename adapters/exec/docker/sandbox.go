package docker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/internal/secretbroker"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/execution"
)

// Units a sandbox may realize.
var units = []string{"runner", "qa-runner"}

var (
	digestImage   = regexp.MustCompile(`^[a-z0-9]+(?:[._/:-][a-z0-9]+)*@sha256:[a-f0-9]{64}$`)
	envName       = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
	labelKey      = regexp.MustCompile(`^sisf\.[a-z0-9][a-z0-9_.-]{0,62}$`)
	reservedLabel = []string{"sisf.run_id", "sisf.unit", "sisf.sandbox"}
)

// proxyEnv are the variables that point a command at the egress proxy. A
// spec or a command may not set them.
var proxyEnv = []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy", "NO_PROXY", "no_proxy", "ALL_PROXY", "all_proxy"}

// sandbox is one sandbox and the containers its commands run in.
type sandbox struct {
	info     execution.SandboxInfo
	spec     execution.SandboxSpec
	limits   execution.Limits
	deadline time.Time
	runtime  string
	// grants maps each granted secret to the variable it is delivered in.
	grants  map[string]string
	execs   int
	running map[string]bool
}

// CreateSandbox records a sandbox for a run. Untrusted code is refused with
// NOT_SUPPORTED unless isolation and egress enforcement are both proven;
// host mounts are refused; every secret grant must belong to the run and
// name the variable it is delivered in.
func (b *Backend) CreateSandbox(ctx context.Context, spec execution.SandboxSpec) (execution.SandboxInfo, error) {
	if err := spec.Validate(); err != nil {
		return execution.SandboxInfo{}, err
	}
	if err := b.checkSpec(spec); err != nil {
		return execution.SandboxInfo{}, err
	}
	caps, err := b.Capabilities(ctx)
	if err != nil {
		return execution.SandboxInfo{}, err
	}
	if spec.Trust == execution.TrustUntrusted && !execution.UntrustedCodeAllowed(caps) {
		return execution.SandboxInfo{}, core.NewError(core.CodeNotSupported,
			"untrusted code needs proven isolation and an enforced egress policy, and this docker backend lacks one")
	}
	grants, err := b.checkGrants(ctx, spec)
	if err != nil {
		return execution.SandboxInfo{}, err
	}
	id, err := newSandboxID()
	if err != nil {
		return execution.SandboxInfo{}, err
	}
	now := b.now()
	sb := &sandbox{
		info: execution.SandboxInfo{ID: id, RunID: spec.RunID, State: execution.SandboxRunning, Trust: spec.Trust, Image: spec.Image, CreatedAt: now},
		spec: spec, limits: b.limitsFor(spec.Limits), deadline: now.Add(spec.Limits.Timeout),
		grants: grants, running: map[string]bool{},
	}
	if caps.Supports(CapGVisor) {
		sb.runtime = gvisorRuntime
	}
	b.mu.Lock()
	b.sandboxes[id] = sb
	b.mu.Unlock()
	return sb.info, nil
}

func (b *Backend) checkSpec(spec execution.SandboxSpec) error {
	switch {
	case !slices.Contains(units, spec.Unit):
		return invalid("a docker sandbox realizes the runner or the qa-runner unit")
	case !digestImage.MatchString(spec.Image):
		return invalid("the sandbox image must be pinned by digest")
	case len(spec.Mounts) > 0:
		return core.NewError(core.CodeNotSupported, "the docker backend mounts nothing from the host into a sandbox")
	}
	for key := range spec.Labels {
		if !labelKey.MatchString(key) || key == b.cfg.RunnerLabel || slices.Contains(reservedLabel, key) {
			return invalid("sandbox label " + key + " is reserved or outside the sisf. namespace")
		}
	}
	return checkEnv(spec.Env)
}

func checkEnv(env map[string]string) error {
	for name, value := range env {
		if !envName.MatchString(name) || slices.Contains(proxyEnv, name) || strings.ContainsRune(value, 0) {
			return invalid("environment variable " + name + " may not be set")
		}
	}
	return nil
}

// checkGrants maps each grant to its variable, refusing a grant of another
// run, one not made for a sandbox environment and two grants for one
// variable.
func (b *Backend) checkGrants(ctx context.Context, spec execution.SandboxSpec) (map[string]string, error) {
	if len(spec.SecretGrantIDs) == 0 {
		return nil, nil
	}
	if b.cfg.Secrets == nil {
		return nil, core.NewError(core.CodeNotSupported, "this docker backend was given no secret broker")
	}
	grants := map[string]string{}
	taken := map[string]bool{}
	for _, id := range spec.SecretGrantIDs {
		g, err := b.cfg.Secrets.Grant(ctx, id)
		if err != nil {
			return nil, err
		}
		name := secretbroker.EnvName(g)
		switch {
		case g.RunID != spec.RunID:
			return nil, core.NewError(core.CodePermissionDenied, "a secret grant of another run cannot enter this sandbox")
		case name == "":
			return nil, invalid("a secret grant must be made for a sandbox environment variable")
		case taken[name] || spec.Env[name] != "" || slices.Contains(proxyEnv, name):
			return nil, invalid("environment variable " + name + " is given twice")
		}
		taken[name], grants[id] = true, name
	}
	return grants, nil
}

// limitsFor fills the limits a spec leaves at zero.
func (b *Backend) limitsFor(l execution.Limits) execution.Limits {
	d := b.cfg.Limits
	if l.CPUMillis == 0 {
		l.CPUMillis = d.CPUMillis
	}
	if l.MemoryBytes == 0 {
		l.MemoryBytes = d.MemoryBytes
	}
	if l.PIDs == 0 {
		l.PIDs = d.PIDs
	}
	if l.DiskBytes == 0 {
		l.DiskBytes = d.DiskBytes
	}
	return l
}

// Inspect returns what the backend knows of a sandbox; NOT_FOUND when it
// knows nothing.
func (b *Backend) Inspect(_ context.Context, id execution.SandboxID) (execution.SandboxInfo, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	sb, ok := b.sandboxes[id]
	if !ok {
		return execution.SandboxInfo{}, core.NewError(core.CodeNotFound, "no such sandbox")
	}
	return sb.info, nil
}

// Destroy stops and removes every container of the sandbox. Destroying a
// sandbox that is already gone, or was never known, succeeds.
func (b *Backend) Destroy(ctx context.Context, id execution.SandboxID) error {
	b.mu.Lock()
	sb, ok := b.sandboxes[id]
	var containers []string
	if ok {
		sb.info.State = execution.SandboxDestroyed
		containers = slices.Collect(maps.Keys(sb.running))
	}
	b.mu.Unlock()
	for _, c := range containers {
		if err := b.api.remove(context.WithoutCancel(ctx), c); err != nil {
			return err
		}
		b.mu.Lock()
		delete(sb.running, c)
		b.mu.Unlock()
	}
	return nil
}

func newSandboxID() (execution.SandboxID, error) {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return "", core.NewError(core.CodeInternal, "no randomness for a sandbox ID").WithCause(err)
	}
	return execution.SandboxID("sbx-" + hex.EncodeToString(raw)), nil
}
